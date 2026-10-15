package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
)

// CreateInputSnapshotOptions selects private storage and capture limits. Zero
// limit fields use the same defaults as CreateSnapshot and RestoreSnapshot.
type CreateInputSnapshotOptions struct {
	TempDir string
	Limits  SnapshotLimits
}

// CreateInputSnapshot creates an independent snapshot with input.json overlaid
// at the project root. The input bytes are copied exactly as provided. The base
// snapshot remains open and unchanged; the returned snapshot owns its storage.
func CreateInputSnapshot(ctx context.Context, base *Snapshot, input []byte, options CreateInputSnapshotOptions) (*Snapshot, error) {
	return createInputOverlaySnapshot(ctx, base, map[string][]byte{"input.json": bytes.Clone(input)}, options)
}

func createInputOverlaySnapshot(ctx context.Context, base *Snapshot, overlayFiles map[string][]byte, options CreateInputSnapshotOptions) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", err)
	}
	if base == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("base snapshot is required"))
	}
	limits, err := snapshotLimits(options.Limits)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	if len(overlayFiles) == 0 {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("at least one input overlay file is required"))
	}
	for name, data := range overlayFiles {
		if !validSnapshotPath(name) || name == "." {
			return nil, snapshotFailure(SnapshotStageValidate, name, errors.New("input overlay path must be a logical snapshot file"))
		}
		if int64(len(data)) > limits.MaxFileBytes || int64(len(data)) > limits.MaxTotalBytes {
			return nil, snapshotFailure(SnapshotStageValidate, name, ErrSnapshotLimit)
		}
	}

	manifest := base.Manifest()
	base.mu.Lock()
	if base.closed || base.root == "" {
		base.mu.Unlock()
		return nil, snapshotFailure(SnapshotStageAcquire, "", ErrSnapshotClosed)
	}
	inputs := slices.Clone(base.inputs)
	protectedDirectories := slices.Clone(base.protectedDirectories)
	baseRoot := base.root
	base.mu.Unlock()
	tempDir := options.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tempDir, err = canonicalDirectory(tempDir)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	for _, root := range append(slices.Clone(protectedDirectories), baseRoot) {
		if isContained(root, tempDir) {
			return nil, snapshotFailure(SnapshotStageValidate, "", fmt.Errorf("snapshot temporary storage must be outside captured input directory %q", root))
		}
	}

	baseScan := manifestSnapshotScan(manifest.Files, manifest.Directories)
	manifestFilesByPath := make(map[string]SnapshotFile, len(manifest.Files)+len(overlayFiles))
	for _, file := range manifest.Files {
		manifestFilesByPath[file.Path] = file
	}
	directorySet := make(map[string]bool, len(manifest.Directories)+len(overlayFiles))
	for _, directory := range manifest.Directories {
		directorySet[directory] = true
	}
	for name, data := range overlayFiles {
		if hasSnapshotDir(baseScan, name) {
			return nil, snapshotFailure(SnapshotStageValidate, name, errors.New("input overlay file collides with a project directory"))
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := manifestFilesByPath[parent]; exists {
				return nil, snapshotFailure(SnapshotStageValidate, parent, errors.New("input overlay directory collides with a project file"))
			}
			directorySet[parent] = true
		}
		inputHash := sha256.Sum256(data)
		manifestFilesByPath[name] = SnapshotFile{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(inputHash[:])}
	}
	files := make([]SnapshotFile, 0, len(manifestFilesByPath))
	for _, file := range manifestFilesByPath {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	manifest.Files = files
	manifest.Directories = directories

	project := manifestSnapshotScan(manifest.Files, manifest.Directories)
	for index := range manifest.Fonts {
		font := &manifest.Fonts[index]
		if !font.InProject {
			continue
		}
		tree := snapshotSubtree(project, font.Path)
		font.Files = manifestFiles(tree)
		font.Directories = manifestDirs(tree)
	}
	id, err := SnapshotContentID(manifest)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "manifest", err)
	}
	manifest.ID = id

	externalFonts := make(map[int]fs.FS)
	for index, font := range manifest.Fonts {
		if font.InProject {
			continue
		}
		fontFS, err := base.FontFS(index)
		if err != nil {
			return nil, snapshotFailure(SnapshotStageAcquire, path.Join("fonts", strconv.Itoa(index)), err)
		}
		externalFonts[index] = fontFS
	}
	bundle := SnapshotBundle{
		Manifest:       manifest,
		ProjectFS:      newSnapshotInputOverlayFS(base.FS(), overlayFiles),
		ExternalFontFS: externalFonts,
	}
	snapshot, err := RestoreSnapshot(ctx, bundle, RestoreSnapshotOptions{TempDir: tempDir, Limits: options.Limits})
	if err != nil {
		return nil, err
	}
	snapshot.inputs = inputs
	snapshot.protectedDirectories = protectedDirectories
	return snapshot, nil
}

type snapshotInputOverlayFS struct {
	base        fs.FS
	files       map[string][]byte
	directories map[string]bool
}

func newSnapshotInputOverlayFS(base fs.FS, files map[string][]byte) snapshotInputOverlayFS {
	clonedFiles := make(map[string][]byte, len(files))
	directories := map[string]bool{".": true}
	for name, data := range files {
		clonedFiles[name] = bytes.Clone(data)
		for parent := path.Dir(name); ; parent = path.Dir(parent) {
			directories[parent] = true
			if parent == "." {
				break
			}
		}
	}
	return snapshotInputOverlayFS{base: base, files: clonedFiles, directories: directories}
}

func (overlay snapshotInputOverlayFS) Open(name string) (fs.File, error) {
	if data, exists := overlay.files[name]; exists {
		info := snapshotFileInfo{name: path.Base(name), size: int64(len(data)), mode: 0o444}
		return &snapshotInputOverlayFile{reader: bytes.NewReader(data), info: info}, nil
	}
	return overlay.base.Open(name)
}

func (overlay snapshotInputOverlayFS) Stat(name string) (fs.FileInfo, error) {
	if data, exists := overlay.files[name]; exists {
		return snapshotFileInfo{name: path.Base(name), size: int64(len(data)), mode: 0o444}, nil
	}
	if overlay.directories[name] {
		return snapshotFileInfo{name: path.Base(name), mode: fs.ModeDir | 0o555}, nil
	}
	return fs.Stat(overlay.base, name)
}

func (overlay snapshotInputOverlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(overlay.base, name)
	if err != nil && !overlay.directories[name] {
		return nil, err
	}
	if err != nil {
		entries = nil
	}
	merged := make(map[string]fs.DirEntry, len(entries)+len(overlay.files))
	for _, entry := range entries {
		merged[entry.Name()] = entry
	}
	for directory := range overlay.directories {
		if directory == "." || path.Dir(directory) != name {
			continue
		}
		info := snapshotFileInfo{name: path.Base(directory), mode: fs.ModeDir | 0o555}
		merged[info.name] = snapshotDirEntry{info: info}
	}
	for filePath, data := range overlay.files {
		if path.Dir(filePath) != name {
			continue
		}
		info := snapshotFileInfo{name: path.Base(filePath), size: int64(len(data)), mode: 0o444}
		merged[info.name] = snapshotDirEntry{info: info}
	}
	result := make([]fs.DirEntry, 0, len(merged))
	for _, entry := range merged {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

type snapshotInputOverlayFile struct {
	reader *bytes.Reader
	info   snapshotFileInfo
	closed bool
}

func (file *snapshotInputOverlayFile) Read(buffer []byte) (int, error) {
	if file.closed {
		return 0, fs.ErrClosed
	}
	return file.reader.Read(buffer)
}

func (file *snapshotInputOverlayFile) Stat() (fs.FileInfo, error) {
	if file.closed {
		return nil, fs.ErrClosed
	}
	return file.info, nil
}

func (file *snapshotInputOverlayFile) Close() error {
	if file.closed {
		return nil
	}
	file.closed = true
	return nil
}

var _ fs.StatFS = snapshotInputOverlayFS{}
var _ fs.ReadDirFS = snapshotInputOverlayFS{}
var _ fs.File = (*snapshotInputOverlayFile)(nil)
