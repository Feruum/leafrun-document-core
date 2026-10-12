package documentcore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// FolderSource selects a local project directory for immutable capture. Root
// must be explicit; a selected root alias is resolved before reads are pinned.
// Capture excludes .git metadata at any depth, the root output directory,
// ordinary .leafrun-* staging files, and SnapshotOptions.Output. Other resource
// files and directories, including nested output directories, remain inputs.
type FolderSource struct {
	Root    string
	options SnapshotOptions
}

func (source FolderSource) withSnapshotOptions(options SnapshotOptions) ProjectSource {
	options.FontPaths = append([]string(nil), options.FontPaths...)
	source.options = options
	return source
}

// Open acquires bounded native reads. Close releases only handles owned by this
// source; it never deletes or modifies the caller's project or excluded files.
func (source FolderSource) Open(ctx context.Context) (SourceTree, error) {
	if err := ctx.Err(); err != nil {
		return SourceTree{}, err
	}
	if source.Root == "" {
		return SourceTree{}, errors.New("folder project root is required")
	}
	native, err := openSnapshotNativeFS(source.Root)
	if err != nil {
		return SourceTree{}, err
	}
	owner := &folderOwner{ctx: ctx, root: native.root, fontPaths: append([]string(nil), source.options.FontPaths...), trees: make(map[string]*folderFS)}
	tree := SourceTree{Close: owner.close}
	project := &folderFS{native: native, owner: owner, identities: make(map[string]fs.FileInfo)}
	owner.trees[native.root] = project
	results := []string{}
	if source.options.Output != "" {
		absolute, err := absoluteFrom(native.root, source.options.Output)
		if err != nil {
			return tree, err
		}
		resolved, err := resolveFolderResult(absolute)
		if err != nil {
			return tree, err
		}
		results = append(results, absolute, resolved)
	}
	if err := ctx.Err(); err != nil {
		return tree, err
	}
	owner.results = results
	tree.FS = project
	tree.Origin = SourceOrigin{Kind: "folder", Location: native.root, SourceRoot: native.root}
	tree.ProtectedPaths = []string{native.root}
	return tree, nil
}

type folderFS struct {
	native     *snapshotNativeFS
	owner      *folderOwner
	mu         sync.Mutex
	identities map[string]fs.FileInfo
}

type folderOwner struct {
	ctx       context.Context
	root      string
	results   []string
	fontPaths []string
	fontRoots []string
	mu        sync.Mutex
	trees     map[string]*folderFS
	closed    bool
	closeErr  error
}

func (owner *folderOwner) close() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return owner.closeErr
	}
	owner.closed = true
	for _, tree := range owner.trees {
		owner.closeErr = errors.Join(owner.closeErr, tree.native.Close())
	}
	return owner.closeErr
}

func (folder *folderFS) snapshotFontFS(root string) (fs.FS, error) {
	owner := folder.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil, fs.ErrClosed
	}
	if err := owner.ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := strictSnapshotNativeDirectory(root)
	if err != nil {
		return nil, err
	}
	owner.fontRoots = append(owner.fontRoots, canonical)
	for _, tree := range owner.trees {
		if folderSamePath(tree.native.root, canonical) {
			return tree, nil
		}
	}
	native, err := openSnapshotNativeFS(canonical)
	if err != nil {
		return nil, err
	}
	if err := owner.ctx.Err(); err != nil {
		return nil, errors.Join(err, native.Close())
	}
	fonts := &folderFS{native: native, owner: owner, identities: make(map[string]fs.FileInfo)}
	owner.trees[native.root] = fonts
	return fonts, nil
}

func (folder *folderFS) snapshotInputMetadata(ctx context.Context) ([]snapshotInput, error) {
	owner := folder.owner
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(owner.results) == 0 {
		return nil, nil
	}
	selected := owner.results[len(owner.results)-1]
	info, err := os.Lstat(selected)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("selected folder result must be an ordinary file")
	}
	// Freeze only the current result's native identity. Excluded output contents
	// and times can change while project inputs are being captured.
	info, err = snapshotNativeFileInfo(selected)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("selected folder result must be an ordinary file")
	}
	input := snapshotInput{path: selected, info: info, excluded: true}
	if isContained(owner.root, selected) {
		relative, err := filepath.Rel(owner.root, selected)
		if err != nil {
			return nil, err
		}
		input.projectPath = filepath.ToSlash(relative)
	}
	switch strings.ToLower(filepath.Ext(selected)) {
	case ".ttf", ".ttc", ".otf", ".otc":
		roots := []string{filepath.Join(owner.root, "fonts")}
		for _, relative := range owner.fontPaths {
			if !filepath.IsAbs(relative) {
				roots = append(roots, filepath.Join(owner.root, filepath.FromSlash(relative)))
			}
		}
		owner.mu.Lock()
		roots = append(roots, owner.fontRoots...)
		owner.mu.Unlock()
		for _, root := range roots {
			if isContained(root, selected) {
				input.font = true
				break
			}
		}
	}
	return []snapshotInput{input}, ctx.Err()
}

func (folder *folderFS) snapshotVerifySource(ctx context.Context) error {
	folder.owner.mu.Lock()
	trees := make([]*folderFS, 0, len(folder.owner.trees))
	for _, tree := range folder.owner.trees {
		trees = append(trees, tree)
	}
	folder.owner.mu.Unlock()
	sort.Slice(trees, func(i, j int) bool { return trees[i].native.root < trees[j].native.root })
	for _, tree := range trees {
		// Closing a directory during the final scan can precede later hashes.
		// Recheck its retained native identity before accepting the capture.
		tree.mu.Lock()
		directories := make(map[string]fs.FileInfo)
		names := []string{}
		for name, info := range tree.identities {
			if info.IsDir() {
				directories[name] = info
				names = append(names, name)
			}
		}
		tree.mu.Unlock()
		sort.Strings(names)
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := tree.native.Stat(name)
			if err != nil {
				return &fs.PathError{Op: "verify", Path: filepath.Join(tree.native.root, filepath.FromSlash(name)), Err: errors.Join(ErrSourceChanged, err)}
			}
			if info.Mode().Type() != fs.ModeDir || !os.SameFile(directories[name], info) {
				return &fs.PathError{Op: "verify", Path: filepath.Join(tree.native.root, filepath.FromSlash(name)), Err: ErrSourceChanged}
			}
		}
	}
	// Excluded leaves can change after their last directory scan. Recheck only
	// their kind; ordinary output replacements and removals remain permitted.
	for _, selected := range folder.owner.results {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateFolderResultFile(selected); err != nil {
			return &fs.PathError{Op: "verify", Path: selected, Err: errors.Join(ErrSourceChanged, err)}
		}
	}
	return ctx.Err()
}

// Resolve parent directory aliases, then append the ordinary result or absent
// suffix. The selected file itself must never be a link or special file.
func resolveFolderResult(absolute string) (string, error) {
	if err := validateFolderResultFile(absolute); err != nil {
		return "", err
	}
	suffix := []string{filepath.Base(absolute)}
	for parent := filepath.Dir(absolute); ; parent = filepath.Dir(parent) {
		canonical, parentErr := canonicalDirectory(parent)
		if parentErr == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				canonical = filepath.Join(canonical, suffix[i])
			}
			if err := validateFolderResultFile(canonical); err != nil {
				return "", err
			}
			return canonical, nil
		}
		if !errors.Is(parentErr, fs.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", parentErr
		}
		suffix = append(suffix, filepath.Base(parent))
	}
}

func validateFolderResultFile(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return nil
	}
	var targetErr error
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		// Preserve the missing-target cause for a dangling link while refusing
		// to treat a link's target as the selected ordinary result.
		_, targetErr = os.Stat(name)
	}
	return errors.Join(errors.New("selected folder result must be an ordinary file without links"), targetErr)
}

func folderSamePath(first, second string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func (folder *folderFS) excluded(name string, mode fs.FileMode) bool {
	absolute := filepath.Join(folder.native.root, filepath.FromSlash(name))
	logical := name
	inProject := isContained(folder.owner.root, absolute)
	if inProject {
		relative, err := filepath.Rel(folder.owner.root, absolute)
		if err != nil {
			return false
		}
		logical = filepath.ToSlash(relative)
	}
	components := strings.Split(logical, "/")
	for i, component := range components {
		if folderSamePath(component, ".git") {
			return true
		}
		if inProject && i == 0 && folderSamePath(component, "output") && (len(components) > 1 || mode.IsDir()) {
			return true
		}
	}
	basename := path.Base(name)
	if runtime.GOOS == "windows" {
		basename = strings.ToLower(basename)
	}
	if mode.IsRegular() && strings.HasPrefix(basename, ".leafrun-") {
		return true
	}
	for _, selected := range folder.owner.results {
		if folderSamePath(absolute, selected) && mode.IsRegular() {
			return true
		}
	}
	return false
}

func (folder *folderFS) Open(name string) (fs.File, error) {
	if err := folder.owner.ctx.Err(); err != nil {
		return nil, err
	}
	if !validSnapshotPath(name) {
		return nil, snapshotFSError("open", name, fs.ErrInvalid)
	}
	file, err := folder.native.Open(name)
	if err != nil {
		return nil, folderReadError(err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if folder.excluded(name, info.Mode()) {
		return nil, errors.Join(snapshotFSError("open", name, fs.ErrNotExist), file.Close())
	}
	folder.mu.Lock()
	original, exists := folder.identities[name]
	if !exists {
		folder.identities[name] = info
	}
	folder.mu.Unlock()
	if exists && !os.SameFile(original, info) {
		return nil, errors.Join(ErrSourceChanged, file.Close())
	}
	return &folderFile{File: file, folder: folder, name: name}, nil
}

func (folder *folderFS) Stat(name string) (info fs.FileInfo, err error) {
	file, err := folder.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.Stat()
}

func (folder *folderFS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	file, err := folder.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	entries, err = file.(fs.ReadDirFile).ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

type folderFile struct {
	fs.File
	folder *folderFS
	name   string
}

func (file *folderFile) Read(buffer []byte) (int, error) {
	n, err := file.File.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(ErrSourceChanged, err)
	}
	return n, err
}

func (file *folderFile) Stat() (fs.FileInfo, error) {
	info, err := file.File.Stat()
	if err != nil {
		return nil, folderReadError(err)
	}
	if info.IsDir() {
		return folderDirectoryInfo{info}, nil
	}
	return info, nil
}

// Directory times include writes to excluded siblings. Included inventories,
// full content hashes, and the separately retained native identities detect
// changes without treating generated output activity as a source mutation.
type folderDirectoryInfo struct{ fs.FileInfo }

func (folderDirectoryInfo) ModTime() time.Time { return time.Time{} }

type folderDirectoryEntry struct{ fs.DirEntry }

func (entry folderDirectoryEntry) Info() (fs.FileInfo, error) {
	info, err := entry.DirEntry.Info()
	if err != nil {
		return nil, folderReadError(err)
	}
	if info.IsDir() {
		return folderDirectoryInfo{info}, nil
	}
	return info, nil
}

func folderReadError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.Join(ErrSourceChanged, err)
	}
	return err
}

func (file *folderFile) ReadDir(n int) ([]fs.DirEntry, error) {
	reader, ok := file.File.(fs.ReadDirFile)
	if !ok {
		return nil, snapshotFSError("readdir", file.name, fs.ErrInvalid)
	}
	entries := []fs.DirEntry{}
	for n <= 0 || len(entries) < n {
		count := n
		if n > 0 {
			count -= len(entries)
		}
		batch, err := reader.ReadDir(count)
		for _, entry := range batch {
			if !file.folder.excluded(path.Join(file.name, entry.Name()), entry.Type()) {
				entries = append(entries, folderDirectoryEntry{entry})
			}
		}
		if err != nil {
			if n > 0 && len(entries) > 0 && errors.Is(err, io.EOF) {
				return entries, nil
			}
			return entries, err
		}
		if n <= 0 || len(batch) == 0 {
			break
		}
	}
	return entries, nil
}
