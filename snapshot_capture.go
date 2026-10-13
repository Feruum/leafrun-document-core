package documentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type snapshotScanFile struct {
	SnapshotFile
	mtime time.Time
}

type snapshotScanDir struct {
	path  string
	mtime time.Time
}
type snapshotScan struct {
	files []snapshotScanFile
	dirs  []snapshotScanDir
}
type snapshotCaptureTree struct {
	fsys       fs.FS
	storage    string
	scan       snapshotScan
	nativeRoot string
}
type snapshotBudget struct {
	limits SnapshotLimits
	files  int
	bytes  int64
}

type snapshotIOError struct {
	write bool
	cause error
}

func (failure *snapshotIOError) Error() string { return failure.cause.Error() }
func (failure *snapshotIOError) Unwrap() error { return failure.cause }

func captureSnapshot(ctx context.Context, source SourceTree, options SnapshotOptions) (snapshot *Snapshot, err error) {
	var nativeFonts []*snapshotNativeFS
	defer func() {
		for _, native := range nativeFonts {
			if closeErr := native.Close(); closeErr != nil {
				err = errors.Join(err, snapshotFailure(SnapshotStageRelease, native.root, closeErr))
			}
		}
		if err != nil && snapshot != nil {
			err = errors.Join(err, snapshot.Close())
			snapshot = nil
		}
	}()
	limits, err := snapshotLimits(options.Limits)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	budget := &snapshotBudget{limits: limits}
	project, err := scanSnapshotTree(ctx, source.FS, budget)
	if err != nil {
		return nil, err
	}
	trees := []snapshotCaptureTree{{fsys: source.FS, storage: "project", scan: project}}
	manifest := SnapshotManifest{FormatVersion: 1, Origin: source.Origin, Files: manifestFiles(project), Directories: manifestDirs(project), Fonts: []SnapshotFont{}}
	fontPaths := append([]string(nil), options.FontPaths...)
	if hasSnapshotDir(project, "fonts") {
		fontPaths = append([]string{"fonts"}, fontPaths...)
	} else if hasSnapshotFile(project, "fonts") {
		return nil, snapshotFailure(SnapshotStageValidate, "fonts", errors.New("default font path must be a directory"))
	}
	for _, fontPath := range fontPaths {
		if err := ctx.Err(); err != nil {
			return nil, snapshotFailure(SnapshotStageScan, fontPath, err)
		}
		if fontPath == "" {
			return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("font directory must not be empty"))
		}
		if !filepath.IsAbs(fontPath) {
			logical := filepath.ToSlash(fontPath)
			if !validSnapshotPath(logical) || !hasSnapshotDir(project, logical) {
				return nil, snapshotFailure(SnapshotStageValidate, fontPath, errors.New("font path must be a valid source filesystem directory"))
			}
			fontScan := snapshotSubtree(project, logical)
			manifest.Fonts = append(manifest.Fonts, SnapshotFont{Path: logical, InProject: true, Files: manifestFiles(fontScan), Directories: manifestDirs(fontScan)})
			continue
		}
		fontRoot, err := strictSnapshotNativeDirectory(fontPath)
		if err != nil {
			return nil, snapshotFailure(SnapshotStageValidate, fontPath, err)
		}
		var fontFS fs.FS
		if provider, ok := source.FS.(snapshotNativeFontSource); ok {
			fontFS, err = provider.snapshotFontFS(fontRoot)
			if err != nil {
				return nil, snapshotFailure(SnapshotStageValidate, fontPath, err)
			}
			if fontFS == nil {
				return nil, snapshotFailure(SnapshotStageValidate, fontPath, errors.New("font filesystem is required"))
			}
		} else {
			native, nativeErr := openSnapshotNativeFS(fontRoot)
			if nativeErr != nil {
				return nil, snapshotFailure(SnapshotStageValidate, fontPath, nativeErr)
			}
			nativeFonts = append(nativeFonts, native)
			fontFS = native
		}
		fontScan, err := scanSnapshotTree(ctx, fontFS, budget)
		if err != nil {
			return nil, snapshotFailure(SnapshotStageScan, fontPath, err)
		}
		storage := path.Join("fonts", fmt.Sprint(len(manifest.Fonts)))
		trees = append(trees, snapshotCaptureTree{fsys: fontFS, storage: storage, scan: fontScan, nativeRoot: fontRoot})
		manifest.Fonts = append(manifest.Fonts, SnapshotFont{Path: ".", SourcePath: fontRoot, Files: manifestFiles(fontScan), Directories: manifestDirs(fontScan)})
	}
	tempDir := options.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tempDir, err = canonicalDirectory(tempDir)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	guardInputs, protected, err := retainNativeProtection(source.NativeProtection, source.ProtectedPaths, tempDir)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "native protection", err)
	}
	if err := rejectSnapshotStorage(tempDir, protected, trees); err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	inputs, err := retainSnapshotInputs(ctx, protected, project, false)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageScan, "", err)
	}
	inputs = append(inputs, guardInputs...)
	for _, tree := range trees[1:] {
		fontInputs, err := retainSnapshotInputs(ctx, []string{tree.nativeRoot}, tree.scan, true)
		if err != nil {
			return nil, snapshotFailure(SnapshotStageScan, tree.nativeRoot, err)
		}
		inputs = append(inputs, fontInputs...)
	}
	if provider, ok := source.FS.(snapshotInputMetadataSource); ok {
		metadata, metadataErr := provider.snapshotInputMetadata(ctx)
		if metadataErr != nil {
			return nil, snapshotFailure(SnapshotStageScan, "", metadataErr)
		}
		inputs = append(inputs, metadata...)
	}
	// Allocate and retain canonical paths so later alias or environment changes
	// cannot redirect accepted snapshot bytes, cleanup, or compiler storage.
	root, err := os.MkdirTemp(tempDir, "leafrun-snapshot-*")
	if err != nil {
		return nil, snapshotFailure(SnapshotStageStorage, "", err)
	}
	defer func() {
		if err != nil {
			if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
				err = errors.Join(err, snapshotFailure(SnapshotStageStorage, "", cleanupErr))
			}
		}
	}()
	captureBudget := &snapshotBudget{limits: limits}
	var copyErr error
	for _, tree := range trees {
		if copyErr = copySnapshotTree(ctx, tree.fsys, tree.scan, filepath.Join(root, filepath.FromSlash(tree.storage)), captureBudget); copyErr != nil {
			break
		}
	}
	// Verification is deliberately unconditional, even when capture failed or
	// metadata remained unchanged. A canceled context still cancels its reads.
	verifyBudget := &snapshotBudget{limits: limits}
	var verifyErr error
	for _, tree := range trees {
		after, scanErr := scanSnapshotTree(ctx, tree.fsys, verifyBudget)
		if scanErr != nil {
			verifyErr = errors.Join(verifyErr, snapshotFailure(SnapshotStageVerify, tree.storage, errors.Join(ErrSourceChanged, scanErr)))
			continue
		}
		if !sameSnapshotScan(tree.scan, after) {
			verifyErr = errors.Join(verifyErr, snapshotFailure(SnapshotStageVerify, tree.storage, ErrSourceChanged))
		}
	}
	if originalErr := verifySnapshotInputs(ctx, inputs); originalErr != nil {
		verifyErr = errors.Join(verifyErr, snapshotFailure(SnapshotStageVerify, "", originalErr))
	}
	if verifier, ok := source.FS.(snapshotSourceVerifier); ok {
		if sourceErr := verifier.snapshotVerifySource(ctx); sourceErr != nil {
			verifyErr = errors.Join(verifyErr, snapshotFailure(SnapshotStageVerify, "", sourceErr))
		}
	}
	if err = errors.Join(copyErr, verifyErr, ctx.Err()); err != nil {
		return nil, err
	}
	manifest.ID, err = snapshotContentID(manifest)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageStorage, "", err)
	}
	if manifest.Origin.Kind == "folder" {
		manifest.Origin.Revision = manifest.ID
	}
	return &Snapshot{root: root, tempDir: tempDir, manifest: manifest, cleanup: os.RemoveAll, files: make(map[*snapshotOpenFile]struct{}), inputs: inputs, limits: limits, fsInfo: snapshotReadInfo(project)}, nil
}

func snapshotReadInfo(scan snapshotScan) map[string]snapshotFileInfo {
	info := make(map[string]snapshotFileInfo, len(scan.files)+len(scan.dirs))
	for _, file := range scan.files {
		info[file.Path] = snapshotFileInfo{name: path.Base(file.Path), size: file.Size, mode: 0o444, mtime: file.mtime}
	}
	for _, dir := range scan.dirs {
		info[dir.path] = snapshotFileInfo{name: path.Base(dir.path), mode: fs.ModeDir | 0o555, mtime: dir.mtime}
	}
	return info
}

func snapshotLimits(limits SnapshotLimits) (SnapshotLimits, error) {
	if limits.MaxFiles < 0 || limits.MaxTotalBytes < 0 || limits.MaxFileBytes < 0 {
		return limits, errors.New("snapshot limits must not be negative")
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = 10000
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = 512 << 20
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = 128 << 20
	}
	return limits, nil
}

func validSnapshotPath(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, `\`) {
		return false
	}
	if runtime.GOOS == "windows" && name != "." {
		for _, component := range strings.Split(name, "/") {
			// Win32 strips trailing dots/spaces and rejects these characters.
			// Such logical entries cannot be materialized without divergence.
			if strings.TrimRight(component, ". ") != component || strings.ContainsAny(component, `<>:"|?*`) {
				return false
			}
			for _, character := range component {
				if character < 32 {
					return false
				}
			}
		}
	}
	_, err := filepath.Localize(name)
	return err == nil
}

func ordinarySnapshotMode(mode fs.FileMode) bool {
	return mode.Type() == 0 || mode.Type() == fs.ModeDir
}

func scanSnapshotTree(ctx context.Context, fsys fs.FS, budget *snapshotBudget) (snapshotScan, error) {
	scan := snapshotScan{files: []snapshotScanFile{}, dirs: []snapshotScanDir{}}
	var walk func(string, fs.FileInfo) error
	walk = func(name string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return snapshotFailure(SnapshotStageScan, name, err)
		}
		if info == nil || !ordinarySnapshotMode(info.Mode()) || !info.IsDir() {
			return snapshotFailure(SnapshotStageValidate, name, errors.New("source directory must be an ordinary directory"))
		}
		scan.dirs = append(scan.dirs, snapshotScanDir{path: name, mtime: info.ModTime()})
		entries, err := readSnapshotDirectory(fsys, name)
		if err != nil {
			return snapshotFailure(SnapshotStageScan, name, err)
		}
		for _, entry := range entries {
			if entry == nil {
				return snapshotFailure(SnapshotStageValidate, name, errors.New("source returned a nil directory entry"))
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		previous := ""
		nativeNames := make(map[string]bool, len(entries))
		for i, entry := range entries {
			entryName := entry.Name()
			if entryName == "." || entryName == ".." || strings.ContainsAny(entryName, `/\`) || !validSnapshotPath(entryName) || (i > 0 && entryName == previous) {
				return snapshotFailure(SnapshotStageValidate, name, fmt.Errorf("invalid or duplicate directory entry %q", entryName))
			}
			previous = entryName
			if runtime.GOOS == "windows" {
				nativeName := strings.ToUpper(entryName)
				if nativeNames[nativeName] {
					return snapshotFailure(SnapshotStageValidate, name, fmt.Errorf("directory entry %q collides on Windows", entryName))
				}
				nativeNames[nativeName] = true
			}
			child := path.Join(name, entryName)
			if !validSnapshotPath(child) || !ordinarySnapshotMode(entry.Type()) {
				return snapshotFailure(SnapshotStageValidate, child, errors.New("source entry must be an ordinary file or directory"))
			}
			childInfo, err := entry.Info()
			if err != nil {
				return snapshotFailure(SnapshotStageScan, child, err)
			}
			if childInfo == nil || !ordinarySnapshotMode(childInfo.Mode()) || childInfo.Name() != entryName || childInfo.IsDir() != entry.IsDir() || entry.Type() != childInfo.Mode().Type() {
				return snapshotFailure(SnapshotStageValidate, child, errors.New("source entry metadata is invalid"))
			}
			if childInfo.IsDir() {
				if err := walk(child, childInfo); err != nil {
					return err
				}
				continue
			}
			if err := budget.addFile(childInfo.Size()); err != nil {
				return snapshotFailure(SnapshotStageScan, child, err)
			}
			file := snapshotScanFile{SnapshotFile: SnapshotFile{Path: child, Size: childInfo.Size()}, mtime: childInfo.ModTime()}
			size, digest, err := readSnapshotFile(ctx, fsys, file, io.Discard, budget)
			if err != nil {
				return snapshotFailure(SnapshotStageScan, child, err)
			}
			file.Size, file.SHA256 = size, digest
			scan.files = append(scan.files, file)
		}
		return nil
	}
	root, err := statSnapshotRoot(fsys)
	if err != nil {
		return scan, snapshotFailure(SnapshotStageScan, ".", err)
	}
	if err := walk(".", root); err != nil {
		return scan, err
	}
	sort.Slice(scan.files, func(i, j int) bool { return scan.files[i].Path < scan.files[j].Path })
	sort.Slice(scan.dirs, func(i, j int) bool { return scan.dirs[i].path < scan.dirs[j].path })
	return scan, nil
}

func statSnapshotRoot(fsys fs.FS) (info fs.FileInfo, err error) {
	if statter, ok := fsys.(fs.StatFS); ok {
		return statter.Stat(".")
	}
	file, err := fsys.Open(".")
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errors.New("source returned a nil root file")
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.Stat()
}

func readSnapshotDirectory(fsys fs.FS, name string) (entries []fs.DirEntry, err error) {
	if reader, ok := fsys.(fs.ReadDirFS); ok {
		return reader.ReadDir(name)
	}
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errors.New("source returned a nil directory file")
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("source directory does not implement fs.ReadDirFile")
	}
	return directory.ReadDir(-1)
}

func (budget *snapshotBudget) addFile(size int64) error {
	budget.files++
	if budget.files > budget.limits.MaxFiles {
		return fmt.Errorf("%w: maximum %d files", ErrSnapshotLimit, budget.limits.MaxFiles)
	}
	if size < 0 {
		return errors.New("source file has a negative size")
	}
	if size > budget.limits.MaxFileBytes {
		return fmt.Errorf("%w: maximum %d bytes per file", ErrSnapshotLimit, budget.limits.MaxFileBytes)
	}
	return nil
}

func readSnapshotFile(ctx context.Context, fsys fs.FS, expected snapshotScanFile, destination io.Writer, budget *snapshotBudget) (size int64, digest string, err error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	file, err := fsys.Open(expected.Path)
	if err != nil {
		return 0, "", snapshotSourceReadFailure(expected, err)
	}
	if file == nil {
		return 0, "", errors.New("source returned a nil input file")
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return 0, "", snapshotSourceReadFailure(expected, err)
	}
	if info == nil || !info.Mode().IsRegular() {
		return 0, "", errors.New("opened input must be an ordinary file")
	}
	if info.Size() != expected.Size || !info.ModTime().Equal(expected.mtime) {
		return 0, "", ErrSourceChanged
	}
	digester := sha256.New()
	size, err = copySnapshotBytes(ctx, destination, digester, file, budget)
	if err != nil {
		var inputError *snapshotIOError
		if errors.As(err, &inputError) && !inputError.write {
			err = snapshotSourceReadFailure(expected, err)
		}
		return size, "", err
	}
	if size != expected.Size {
		return size, "", ErrSourceChanged
	}
	return size, hex.EncodeToString(digester.Sum(nil)), nil
}

func snapshotSourceReadFailure(expected snapshotScanFile, cause error) error {
	if expected.SHA256 != "" && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(ErrSourceChanged, cause)
	}
	return cause
}

func copySnapshotBytes(ctx context.Context, destination io.Writer, digester hash.Hash, source io.Reader, budget *snapshotBudget) (int64, error) {
	var buffer [32 << 10]byte
	var size int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return size, err
		}
		n, readErr := source.Read(buffer[:])
		if n < 0 || n > len(buffer) {
			return size, &snapshotIOError{cause: errors.New("source reader returned an invalid byte count")}
		}
		if n > 0 {
			emptyReads = 0
			if int64(n) > budget.limits.MaxFileBytes-size || int64(n) > budget.limits.MaxTotalBytes-budget.bytes {
				return size, ErrSnapshotLimit
			}
			size += int64(n)
			budget.bytes += int64(n)
			if _, err := digester.Write(buffer[:n]); err != nil {
				return size, err
			}
			written, err := destination.Write(buffer[:n])
			if err != nil {
				return size, &snapshotIOError{write: true, cause: err}
			}
			if written != n {
				return size, &snapshotIOError{write: true, cause: io.ErrShortWrite}
			}
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return size, &snapshotIOError{cause: io.ErrNoProgress}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return size, ctx.Err()
			}
			return size, &snapshotIOError{cause: readErr}
		}
	}
}

func copySnapshotTree(ctx context.Context, fsys fs.FS, scan snapshotScan, destination string, budget *snapshotBudget) error {
	for _, dir := range scan.dirs {
		if err := ctx.Err(); err != nil {
			return snapshotFailure(SnapshotStageCapture, dir.path, err)
		}
		if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(dir.path)), 0o700); err != nil {
			return snapshotFailure(SnapshotStageStorage, dir.path, err)
		}
	}
	for _, expected := range scan.files {
		if err := budget.addFile(expected.Size); err != nil {
			return snapshotFailure(SnapshotStageCapture, expected.Path, err)
		}
		file, err := os.OpenFile(filepath.Join(destination, filepath.FromSlash(expected.Path)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return snapshotFailure(SnapshotStageStorage, expected.Path, err)
		}
		size, digest, readErr := readSnapshotFile(ctx, fsys, expected, file, budget)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			var writeErr *snapshotIOError
			if closeErr != nil || (errors.As(readErr, &writeErr) && writeErr.write) {
				return snapshotFailure(SnapshotStageStorage, expected.Path, err)
			}
			return snapshotFailure(SnapshotStageCapture, expected.Path, err)
		}
		if size != expected.Size || digest != expected.SHA256 {
			return snapshotFailure(SnapshotStageCapture, expected.Path, ErrSourceChanged)
		}
	}
	return nil
}

func manifestFiles(scan snapshotScan) []SnapshotFile {
	files := make([]SnapshotFile, len(scan.files))
	for i := range scan.files {
		files[i] = scan.files[i].SnapshotFile
	}
	return files
}

func manifestDirs(scan snapshotScan) []string {
	dirs := []string{}
	for _, dir := range scan.dirs {
		if dir.path != "." {
			dirs = append(dirs, dir.path)
		}
	}
	return dirs
}

func hasSnapshotDir(scan snapshotScan, name string) bool {
	for _, dir := range scan.dirs {
		if dir.path == name {
			return true
		}
	}
	return false
}
func hasSnapshotFile(scan snapshotScan, name string) bool {
	for _, file := range scan.files {
		if file.Path == name {
			return true
		}
	}
	return false
}

func snapshotSubtree(scan snapshotScan, root string) snapshotScan {
	subtree := snapshotScan{files: []snapshotScanFile{}, dirs: []snapshotScanDir{}}
	for _, dir := range scan.dirs {
		if dir.path == root {
			dir.path = "."
			subtree.dirs = append(subtree.dirs, dir)
		} else if root == "." || strings.HasPrefix(dir.path, root+"/") {
			if root != "." {
				dir.path = strings.TrimPrefix(dir.path, root+"/")
			}
			subtree.dirs = append(subtree.dirs, dir)
		}
	}
	for _, file := range scan.files {
		if root == "." || strings.HasPrefix(file.Path, root+"/") {
			if root != "." {
				file.Path = strings.TrimPrefix(file.Path, root+"/")
			}
			subtree.files = append(subtree.files, file)
		}
	}
	return subtree
}

func sameSnapshotScan(before, after snapshotScan) bool {
	if len(before.files) != len(after.files) || len(before.dirs) != len(after.dirs) {
		return false
	}
	for i, first := range before.files {
		second := after.files[i]
		if first.SnapshotFile != second.SnapshotFile || !first.mtime.Equal(second.mtime) {
			return false
		}
	}
	for i, first := range before.dirs {
		second := after.dirs[i]
		if first.path != second.path || !first.mtime.Equal(second.mtime) {
			return false
		}
	}
	return true
}

func snapshotContentID(manifest SnapshotManifest) (string, error) {
	type contentFont struct {
		Path        string
		Files       []SnapshotFile
		Directories []string
	}
	content := struct {
		FormatVersion int
		Files         []SnapshotFile
		Directories   []string
		Fonts         []contentFont
	}{manifest.FormatVersion, manifest.Files, manifest.Directories, make([]contentFont, len(manifest.Fonts))}
	for i, font := range manifest.Fonts {
		content.Fonts[i] = contentFont{font.Path, font.Files, font.Directories}
	}
	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
