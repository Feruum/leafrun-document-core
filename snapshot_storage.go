package documentcore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type snapshotOpenFile struct {
	snapshot *Snapshot
	file     *os.File
	name     string
	closed   bool
}

func (view snapshotView) Open(name string) (fs.File, error) {
	snapshot := view.snapshot
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed || snapshot.root == "" {
		return nil, snapshotFSError("open", name, ErrSnapshotClosed)
	}
	if !validSnapshotPath(name) {
		return nil, snapshotFSError("open", name, fs.ErrInvalid)
	}
	localized, err := filepath.Localize(name)
	if err != nil {
		return nil, snapshotFSError("open", name, err)
	}
	file, err := os.Open(filepath.Join(snapshot.root, "project", localized))
	if err != nil {
		return nil, snapshotFSError("open", name, err)
	}
	opened := &snapshotOpenFile{snapshot: snapshot, file: file, name: name}
	snapshot.files[opened] = struct{}{}
	return opened, nil
}

func (file *snapshotOpenFile) operationError(op string) error {
	if file.snapshot.closed {
		return snapshotFSError(op, file.name, ErrSnapshotClosed)
	}
	if file.closed {
		return snapshotFSError(op, file.name, fs.ErrClosed)
	}
	return nil
}

func (file *snapshotOpenFile) Read(buffer []byte) (int, error) {
	file.snapshot.mu.Lock()
	defer file.snapshot.mu.Unlock()
	if err := file.operationError("read"); err != nil {
		return 0, err
	}
	n, err := file.file.Read(buffer)
	if err != nil {
		return n, snapshotFSError("read", file.name, err)
	}
	return n, nil
}

func (file *snapshotOpenFile) Stat() (fs.FileInfo, error) {
	file.snapshot.mu.Lock()
	defer file.snapshot.mu.Unlock()
	if err := file.operationError("stat"); err != nil {
		return nil, err
	}
	info, ok := file.snapshot.fsInfo[file.name]
	if !ok {
		return nil, snapshotFSError("stat", file.name, fs.ErrNotExist)
	}
	return info, nil
}

func (file *snapshotOpenFile) ReadDir(n int) ([]fs.DirEntry, error) {
	file.snapshot.mu.Lock()
	defer file.snapshot.mu.Unlock()
	if err := file.operationError("readdir"); err != nil {
		return nil, err
	}
	entries, readErr := file.file.ReadDir(n)
	public := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, ok := file.snapshot.fsInfo[path.Join(file.name, entry.Name())]
		if !ok {
			return public, snapshotFSError("readdir", file.name, fs.ErrNotExist)
		}
		public = append(public, snapshotDirEntry{info})
	}
	if readErr != nil {
		readErr = snapshotFSError("readdir", file.name, readErr)
	}
	return public, readErr
}

func (file *snapshotOpenFile) Close() error {
	file.snapshot.mu.Lock()
	defer file.snapshot.mu.Unlock()
	if file.closed {
		return nil
	}
	file.closed = true
	delete(file.snapshot.files, file)
	if err := file.file.Close(); err != nil {
		return snapshotFSError("close", file.name, err)
	}
	return nil
}

func snapshotFSError(op, name string, err error) error {
	if err == io.EOF {
		return io.EOF
	}
	var nativeError *fs.PathError
	if errors.As(err, &nativeError) {
		err = nativeError.Err
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}

type snapshotFileInfo struct {
	name  string
	size  int64
	mode  fs.FileMode
	mtime time.Time
}

func (info snapshotFileInfo) Name() string       { return info.name }
func (info snapshotFileInfo) Size() int64        { return info.size }
func (info snapshotFileInfo) Mode() fs.FileMode  { return info.mode }
func (info snapshotFileInfo) ModTime() time.Time { return info.mtime }
func (info snapshotFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info snapshotFileInfo) Sys() any           { return nil }

type snapshotDirEntry struct{ info snapshotFileInfo }

func (entry snapshotDirEntry) Name() string               { return entry.info.Name() }
func (entry snapshotDirEntry) IsDir() bool                { return entry.info.IsDir() }
func (entry snapshotDirEntry) Type() fs.FileMode          { return entry.info.Mode().Type() }
func (entry snapshotDirEntry) Info() (fs.FileInfo, error) { return entry.info, nil }

type snapshotWorkingCopy struct {
	root, storage string
	fonts         []string
	inputs        []snapshotInput
	mu            sync.Mutex
	closed        bool
	closeErr      error
	cleanup       func(string) error
}

// prepareWorkingCopy holds the snapshot lock only until independent compiler
// inputs exist. Close can then delete the snapshot without affecting the copy.
func (snapshot *Snapshot) prepareWorkingCopy(ctx context.Context) (working *snapshotWorkingCopy, err error) {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed || snapshot.root == "" {
		return nil, snapshotFailure(SnapshotStagePrepare, "", ErrSnapshotClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStagePrepare, "", err)
	}
	tempDir, err := canonicalDirectory(snapshot.tempDir)
	if err != nil {
		return nil, snapshotFailure(SnapshotStagePrepare, "", err)
	}
	parentRelative, err := filepath.Rel(snapshot.tempDir, tempDir)
	if err != nil || parentRelative != "." {
		return nil, snapshotFailure(SnapshotStagePrepare, "", errors.Join(errors.New("snapshot temporary storage parent changed"), err))
	}
	storage, err := os.MkdirTemp(tempDir, "leafrun-work-*")
	if err != nil {
		return nil, snapshotFailure(SnapshotStagePrepare, "", err)
	}
	defer func() {
		if err != nil {
			if cleanupErr := os.RemoveAll(storage); cleanupErr != nil {
				err = errors.Join(err, snapshotFailure(SnapshotStagePrepare, "", cleanupErr))
			}
		}
	}()
	working = &snapshotWorkingCopy{storage: storage, root: filepath.Join(storage, "project"), fonts: []string{}, inputs: append([]snapshotInput(nil), snapshot.inputs...), cleanup: os.RemoveAll}
	budget := &snapshotBudget{limits: snapshot.limits}
	if err := copyStoredSnapshotTree(ctx, filepath.Join(snapshot.root, "project"), working.root, snapshot.manifest.Files, snapshot.manifest.Directories, budget); err != nil {
		return nil, snapshotFailure(SnapshotStagePrepare, "project", err)
	}
	for i, font := range snapshot.manifest.Fonts {
		if font.InProject {
			working.fonts = append(working.fonts, filepath.Join(working.root, filepath.FromSlash(font.Path)))
			continue
		}
		relative := filepath.Join("fonts", strconv.Itoa(i))
		fontRoot := filepath.Join(storage, relative)
		if err := copyStoredSnapshotTree(ctx, filepath.Join(snapshot.root, relative), fontRoot, font.Files, font.Directories, budget); err != nil {
			return nil, snapshotFailure(SnapshotStagePrepare, relative, err)
		}
		working.fonts = append(working.fonts, fontRoot)
	}
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStagePrepare, "", err)
	}
	return working, nil
}

func copyStoredSnapshotTree(ctx context.Context, source, destination string, files []SnapshotFile, dirs []string, budget *snapshotBudget) error {
	scan := snapshotScan{files: make([]snapshotScanFile, 0, len(files)), dirs: []snapshotScanDir{{path: "."}}}
	for _, dir := range dirs {
		scan.dirs = append(scan.dirs, snapshotScanDir{path: dir})
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(source, filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		scan.files = append(scan.files, snapshotScanFile{SnapshotFile: file, mtime: info.ModTime()})
	}
	return copySnapshotTree(ctx, os.DirFS(source), scan, destination, budget)
}

func (working *snapshotWorkingCopy) Close() error {
	working.mu.Lock()
	defer working.mu.Unlock()
	if working.closed {
		return working.closeErr
	}
	working.closed = true
	if working.storage != "" {
		cleanup := working.cleanup
		if cleanup == nil {
			cleanup = os.RemoveAll
		}
		if err := cleanup(working.storage); err != nil {
			working.closeErr = snapshotFailure(SnapshotStageClose, "working copy", err)
		}
	}
	return working.closeErr
}
