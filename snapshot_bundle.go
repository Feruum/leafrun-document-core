package documentcore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// SnapshotBundle transports a manifest and logical trees without native source
// paths. ExternalFontFS contains exactly the !InProject Manifest.Fonts indices.
// RestoreSnapshot borrows the filesystems and closes only files it opens.
type SnapshotBundle struct {
	Manifest       SnapshotManifest
	ProjectFS      fs.FS
	ExternalFontFS map[int]fs.FS
}

// RestoreSnapshotOptions selects private copied storage and capture limits.
// Zero limit fields use the same defaults as CreateSnapshot.
type RestoreSnapshotOptions struct {
	TempDir string
	Limits  SnapshotLimits
}

// ValidateSnapshotManifest validates v1 logical trees, font order and content
// identity without opening any filesystem or interpreting provenance paths.
func ValidateSnapshotManifest(manifest SnapshotManifest) error {
	id, err := SnapshotContentID(manifest)
	if err != nil {
		return err
	}
	if !snapshotSHA256(manifest.ID) || manifest.ID != id {
		return snapshotFailure(SnapshotStageValidate, "manifest", errors.New("manifest content ID does not match v1 content"))
	}
	return nil
}

// SnapshotContentID validates v1 tree structure and computes canonical content
// identity. The current ID, Origin and font SourcePath are ignored. Slice order
// must already be canonical; this function never sorts or modifies the input.
func SnapshotContentID(manifest SnapshotManifest) (string, error) {
	if err := validateSnapshotManifestContent(manifest); err != nil {
		return "", snapshotFailure(SnapshotStageValidate, "manifest", err)
	}
	return snapshotContentID(manifest)
}

func validateSnapshotManifestContent(manifest SnapshotManifest) error {
	if manifest.FormatVersion != 1 {
		return errors.New("unsupported snapshot manifest format version")
	}
	if manifest.Fonts == nil {
		return errors.New("manifest fonts must be a canonical array, including when empty")
	}
	if err := validateManifestTree(manifest.Files, manifest.Directories); err != nil {
		return fmt.Errorf("project tree: %w", err)
	}
	project := manifestSnapshotScan(manifest.Files, manifest.Directories)
	for index, font := range manifest.Fonts {
		if err := validateManifestTree(font.Files, font.Directories); err != nil {
			return fmt.Errorf("font %d: %w", index, err)
		}
		if !validSnapshotPath(font.Path) {
			return fmt.Errorf("font %d has an invalid logical path", index)
		}
		if !font.InProject {
			if font.Path != "." {
				return fmt.Errorf("external font %d must be rooted at .", index)
			}
			continue
		}
		if !hasSnapshotDir(project, font.Path) {
			return fmt.Errorf("font %d is not a project directory", index)
		}
		subtree := snapshotSubtree(project, font.Path)
		if !slices.Equal(font.Files, manifestFiles(subtree)) || !slices.Equal(font.Directories, manifestDirs(subtree)) {
			return fmt.Errorf("font %d does not match its project subtree", index)
		}
	}
	return nil
}

func validateManifestTree(files []SnapshotFile, directories []string) error {
	if files == nil || directories == nil {
		return errors.New("tree files and directories must be canonical arrays, including when empty")
	}
	names := map[string]bool{".": true}
	previous := ""
	for _, directory := range directories {
		if !validSnapshotPath(directory) || directory == "." || directory <= previous {
			return fmt.Errorf("invalid or unsorted directory %q", directory)
		}
		if !names[path.Dir(directory)] {
			return fmt.Errorf("directory %q has no declared parent", directory)
		}
		names[directory] = true
		previous = directory
	}
	previous = ""
	for _, file := range files {
		if !validSnapshotPath(file.Path) || file.Path == "." || file.Path <= previous {
			return fmt.Errorf("invalid or unsorted file %q", file.Path)
		}
		if names[file.Path] || !names[path.Dir(file.Path)] {
			return fmt.Errorf("file %q collides with a directory or has no declared parent", file.Path)
		}
		if file.Size < 0 || !snapshotSHA256(file.SHA256) {
			return fmt.Errorf("file %q has an invalid size or SHA-256", file.Path)
		}
		previous = file.Path
	}
	return nil
}

func snapshotSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func manifestSnapshotScan(files []SnapshotFile, directories []string) snapshotScan {
	scan := snapshotScan{files: make([]snapshotScanFile, len(files)), dirs: make([]snapshotScanDir, len(directories)+1)}
	scan.dirs[0].path = "."
	for i, file := range files {
		scan.files[i].SnapshotFile = file
	}
	for i, directory := range directories {
		scan.dirs[i+1].path = directory
	}
	return scan
}

// RestoreSnapshot verifies exact project and external font trees, then copies
// them into owned storage. Manifest provenance is preserved verbatim and never
// used as an input path. The returned snapshot owns no caller filesystem.
func RestoreSnapshot(ctx context.Context, bundle SnapshotBundle, options RestoreSnapshotOptions) (snapshot *Snapshot, err error) {
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", err)
	}
	// Copy descriptions so successful restoration owns its complete manifest.
	bundle.Manifest = (&Snapshot{manifest: bundle.Manifest}).Manifest()
	if err := ValidateSnapshotManifest(bundle.Manifest); err != nil {
		return nil, err
	}
	limits, err := snapshotLimits(options.Limits)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", err)
	}
	if bundle.ProjectFS == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "project", errors.New("bundle project filesystem is required"))
	}
	for index, fontFS := range bundle.ExternalFontFS {
		if index < 0 || index >= len(bundle.Manifest.Fonts) || bundle.Manifest.Fonts[index].InProject || fontFS == nil {
			return nil, snapshotFailure(SnapshotStageValidate, "fonts", errors.New("external font filesystems must match external manifest indices"))
		}
	}
	trees := []snapshotCaptureTree{{fsys: bundle.ProjectFS, storage: "project", scan: manifestSnapshotScan(bundle.Manifest.Files, bundle.Manifest.Directories)}}
	for index, font := range bundle.Manifest.Fonts {
		if font.InProject {
			continue
		}
		fontFS := bundle.ExternalFontFS[index]
		if fontFS == nil {
			return nil, snapshotFailure(SnapshotStageValidate, "fonts/"+strconv.Itoa(index), errors.New("external font filesystem is required"))
		}
		trees = append(trees, snapshotCaptureTree{fsys: fontFS, storage: path.Join("fonts", strconv.Itoa(index)), scan: manifestSnapshotScan(font.Files, font.Directories)})
	}
	// Reject oversized declarations before allocating storage or reading bytes.
	declared := &snapshotBudget{limits: limits}
	for _, tree := range trees {
		for _, file := range tree.scan.files {
			if err := declared.addFile(file.Size); err != nil {
				return nil, snapshotFailure(SnapshotStageValidate, file.Path, err)
			}
			if file.Size > limits.MaxTotalBytes-declared.bytes {
				return nil, snapshotFailure(SnapshotStageValidate, file.Path, ErrSnapshotLimit)
			}
			declared.bytes += file.Size
		}
	}
	budget := &snapshotBudget{limits: limits}
	for index := range trees {
		actual, scanErr := scanSnapshotTree(ctx, trees[index].fsys, budget)
		if scanErr != nil {
			return nil, snapshotFailure(SnapshotStageVerify, trees[index].storage, scanErr)
		}
		if !sameManifestTree(trees[index].scan, actual) {
			return nil, snapshotFailure(SnapshotStageVerify, trees[index].storage, errors.New("bundle tree does not match manifest"))
		}
		trees[index].scan = actual
	}
	tempDir := options.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tempDir, err = canonicalDirectory(tempDir)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageStorage, "", err)
	}
	root, err := os.MkdirTemp(tempDir, "leafrun-snapshot-*")
	if err != nil {
		return nil, snapshotFailure(SnapshotStageStorage, "", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(root))
		}
	}()
	copyBudget := &snapshotBudget{limits: limits}
	for _, tree := range trees {
		if err := copySnapshotTree(ctx, tree.fsys, tree.scan, filepath.Join(root, filepath.FromSlash(tree.storage)), copyBudget); err != nil {
			return nil, err
		}
	}
	// Recheck membership after copying, including empty directories. This also
	// rejects changes during copying even when a file retained size and mtime.
	verifyBudget := &snapshotBudget{limits: limits}
	for _, tree := range trees {
		actual, scanErr := scanSnapshotTree(ctx, tree.fsys, verifyBudget)
		if scanErr != nil {
			return nil, snapshotFailure(SnapshotStageVerify, tree.storage, scanErr)
		}
		if !sameManifestTree(tree.scan, actual) {
			return nil, snapshotFailure(SnapshotStageVerify, tree.storage, errors.New("bundle tree changed during restoration"))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageVerify, "", err)
	}
	return &Snapshot{root: root, tempDir: tempDir, manifest: bundle.Manifest, cleanup: os.RemoveAll, files: make(map[*snapshotOpenFile]struct{}), limits: limits, fsInfo: snapshotReadInfo(trees[0].scan)}, nil
}

func sameManifestTree(expected, actual snapshotScan) bool {
	return slices.Equal(manifestFiles(expected), manifestFiles(actual)) && slices.Equal(manifestDirs(expected), manifestDirs(actual))
}
