package documentcore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// snapshotNativeFS pins a canonical selected root and refuses descendant links.
// Root.Open bounds reads even if a checked component is replaced by a link.
// Source-specific wrappers may add exclusions through snapshotNativeFontSource.
type snapshotNativeFS struct {
	root     string
	handle   *os.Root
	rootInfo fs.FileInfo
}

func openSnapshotNativeFS(directory string) (*snapshotNativeFS, error) {
	canonical, err := strictSnapshotNativeDirectory(directory)
	if err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	file, err := handle.Open(".")
	if err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	info, statErr := file.Stat()
	if err := errors.Join(statErr, file.Close()); err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	native := &snapshotNativeFS{root: canonical, handle: handle, rootInfo: info}
	if _, err := native.nativePath("."); err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	return native, nil
}

func (native *snapshotNativeFS) Close() error { return native.handle.Close() }

func strictSnapshotNativeDirectory(directory string) (string, error) {
	canonical, err := canonicalDirectory(directory)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", err
	}
	if info.Mode().Type() != fs.ModeDir {
		return "", fmt.Errorf("%q must be an ordinary selected directory", canonical)
	}
	return canonical, nil
}

func (native snapshotNativeFS) nativePath(name string) (string, error) {
	if !validSnapshotPath(name) {
		return "", &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	localized, err := filepath.Localize(name)
	if err != nil {
		return "", err
	}
	full := filepath.Join(native.root, localized)
	for current := full; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if !ordinarySnapshotMode(info.Mode()) {
			return "", fmt.Errorf("%q must be an ordinary file or directory without links", current)
		}
		if current == native.root {
			if native.rootInfo != nil && !os.SameFile(native.rootInfo, info) {
				return "", ErrSourceChanged
			}
			break
		}
		if filepath.Dir(current) == current {
			return "", fs.ErrInvalid
		}
	}
	return full, nil
}

func (native *snapshotNativeFS) Open(name string) (fs.File, error) {
	_, err := native.nativePath(name)
	if err != nil {
		return nil, err
	}
	localized, err := filepath.Localize(name)
	if err != nil {
		return nil, err
	}
	file, err := openSnapshotNativeHandle(native.handle, localized)
	if err != nil {
		return nil, errors.Join(ErrSourceChanged, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !ordinarySnapshotMode(info.Mode()) {
		return nil, errors.Join(ErrSourceChanged, fmt.Errorf("%q must be an ordinary file or directory without links", name), file.Close())
	}
	if _, err := native.nativePath(name); err != nil {
		return nil, errors.Join(ErrSourceChanged, err, file.Close())
	}
	return file, nil
}

func (native *snapshotNativeFS) Stat(name string) (info fs.FileInfo, err error) {
	file, err := native.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.Stat()
}

func (native *snapshotNativeFS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	file, err := native.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.(fs.ReadDirFile).ReadDir(-1)
}

func rejectSnapshotStorage(tempDir string, protected []string, trees []snapshotCaptureTree) error {
	_, err := snapshotStorageDirectories(tempDir, protected, trees)
	return err
}

func snapshotStorageDirectories(tempDir string, protected []string, trees []snapshotCaptureTree) ([]string, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	absolute, err := canonicalDirectory(tempDir)
	if err != nil {
		return nil, err
	}
	roots := append([]string(nil), protected...)
	for _, tree := range trees {
		if tree.nativeRoot != "" {
			roots = append(roots, tree.nativeRoot)
		}
	}
	directories := []string{}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		root, err = canonicalDirectory(root)
		if err != nil {
			return nil, err
		}
		if isContained(root, absolute) {
			return nil, fmt.Errorf("snapshot temporary storage must be outside input directory %q", root)
		}
		directories = append(directories, root)
	}
	return directories, nil
}

func retainSnapshotInputs(ctx context.Context, protected []string, scan snapshotScan, font bool) (inputs []snapshotInput, err error) {
	defer func() {
		if errors.Is(err, fs.ErrNotExist) {
			err = errors.Join(ErrSourceChanged, err)
		}
	}()
	inputs = []snapshotInput{}
	for _, original := range protected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(original)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !ordinarySnapshotMode(info.Mode()) {
			return nil, fmt.Errorf("protected input %q must be an ordinary file or directory", original)
		}
		if info.Mode().IsRegular() {
			linkInfo, err := os.Lstat(absolute)
			if err != nil {
				return nil, err
			}
			if !linkInfo.Mode().IsRegular() {
				return nil, fmt.Errorf("protected file %q must not be a link", absolute)
			}
			if _, err := strictSnapshotNativeDirectory(filepath.Dir(absolute)); err != nil {
				return nil, err
			}
			info, err = snapshotNativeIdentity(absolute, info)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, snapshotInput{path: absolute, font: font, info: info})
			continue
		}
		absolute, err = strictSnapshotNativeDirectory(absolute)
		if err != nil {
			return nil, err
		}
		for _, file := range scan.files {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			localized, err := filepath.Localize(file.Path)
			if err != nil {
				return nil, err
			}
			full := filepath.Join(absolute, localized)
			// Validate every ancestor, including links added since the source scan.
			if _, err := (snapshotNativeFS{root: absolute}).nativePath(file.Path); err != nil {
				return nil, err
			}
			fileInfo, err := os.Lstat(full)
			if err != nil {
				return nil, err
			}
			if !fileInfo.Mode().IsRegular() {
				return nil, fmt.Errorf("protected input %q must be an ordinary file", full)
			}
			fileInfo, err = snapshotNativeIdentity(full, fileInfo)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, snapshotInput{path: full, projectPath: file.Path, font: font, info: fileInfo})
		}
	}
	return inputs, nil
}

// File.Stat acquires the native identity immediately. On Windows, path-based
// Stat/Lstat defer it until SameFile, which may run after release deletes a
// checkout or replaces an archive at the original path.
func snapshotNativeIdentity(path string, before fs.FileInfo) (fs.FileInfo, error) {
	info, err := snapshotNativeFileInfo(path)
	if err != nil {
		return nil, errors.Join(ErrSourceChanged, err)
	}
	if !info.Mode().IsRegular() || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		return nil, ErrSourceChanged
	}
	return info, nil
}

func snapshotNativeFileInfo(path string) (fs.FileInfo, error) {
	parent, err := canonicalDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	file, err := openSnapshotNativeHandle(root, filepath.Base(path))
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	info, statErr := file.Stat()
	if statErr == nil && !ordinarySnapshotMode(info.Mode()) {
		statErr = errors.Join(ErrSourceChanged, fmt.Errorf("%q must be an ordinary file or directory without links", path))
	}
	if err := errors.Join(statErr, file.Close(), root.Close()); err != nil {
		return nil, err
	}
	return info, nil
}

func verifySnapshotInputs(ctx context.Context, inputs []snapshotInput) error {
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if input.excluded || input.guardOnly {
			continue
		}
		info, err := os.Lstat(input.path)
		if err != nil {
			return errors.Join(ErrSourceChanged, err)
		}
		if !info.Mode().IsRegular() || !os.SameFile(info, input.info) || info.Size() != input.info.Size() || !info.ModTime().Equal(input.info.ModTime()) {
			return ErrSourceChanged
		}
	}
	return nil
}

// Eager guards take precedence over reopening a duplicate protected path. They
// protect identities only; mutable provider metadata is never a consistency
// input and need not correspond to paths in the immutable exported FS.
func retainNativeProtection(guards []NativeProtection, protected []string, tempDir string) ([]snapshotInput, []string, error) {
	inputs := make([]snapshotInput, 0, len(guards))
	for _, guard := range guards {
		if !filepath.IsAbs(guard.Path) || filepath.Clean(guard.Path) != guard.Path || guard.Info == nil || !ordinarySnapshotMode(guard.Info.Mode()) || guard.Info.Sys() == nil || !os.SameFile(guard.Info, guard.Info) {
			return nil, nil, fmt.Errorf("native protection requires an absolute canonical path and ordinary native handle identity")
		}
		// Resolve only the parent association, including its nearest existing
		// ancestor. Never reopen the accepted file or metadata directory itself.
		parent, err := resolveGuardParent(filepath.Dir(guard.Path))
		if err != nil || !folderSamePath(parent, filepath.Dir(guard.Path)) {
			return nil, nil, errors.Join(errors.New("native protection parent association is not canonical"), err)
		}
		if guard.Info.IsDir() && isContained(guard.Path, tempDir) {
			return nil, nil, fmt.Errorf("snapshot temporary storage must be outside protected directory %q", guard.Path)
		}
		inputs = append(inputs, snapshotInput{path: guard.Path, info: guard.Info, guardOnly: true})
	}
	remaining := []string{}
	for _, original := range protected {
		absolute, err := filepath.Abs(original)
		if err != nil {
			return nil, nil, err
		}
		eager := false
		for _, guard := range guards {
			if folderSamePath(absolute, guard.Path) {
				eager = true
				break
			}
		}
		if !eager {
			remaining = append(remaining, original)
		}
	}
	return inputs, remaining, nil
}

func resolveGuardParent(parent string) (string, error) {
	current := parent
	for {
		canonical, err := resolvePath(current)
		if err == nil {
			relative, err := filepath.Rel(current, parent)
			if err != nil {
				return "", err
			}
			return filepath.Join(canonical, relative), nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(current) == current {
			return "", err
		}
		current = filepath.Dir(current)
	}
}
