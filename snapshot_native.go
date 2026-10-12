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
	file, err := native.handle.Open(localized)
	if err != nil {
		return nil, err
	}
	if _, err := native.nativePath(name); err != nil {
		return nil, errors.Join(err, file.Close())
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
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	absolute, err := canonicalDirectory(tempDir)
	if err != nil {
		return err
	}
	roots := append([]string(nil), protected...)
	for _, tree := range trees {
		if tree.nativeRoot != "" {
			roots = append(roots, tree.nativeRoot)
		}
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			continue
		}
		root, err = canonicalDirectory(root)
		if err != nil {
			return err
		}
		if isContained(root, absolute) {
			return fmt.Errorf("snapshot temporary storage must be outside input directory %q", root)
		}
	}
	return nil
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
		return nil, err
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
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	info, statErr := file.Stat()
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
		if input.excluded {
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
