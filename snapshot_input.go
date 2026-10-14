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
	inputSize := int64(len(input))
	if inputSize > limits.MaxFileBytes || inputSize > limits.MaxTotalBytes {
		return nil, snapshotFailure(SnapshotStageValidate, "input.json", ErrSnapshotLimit)
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

	if hasSnapshotDir(manifestSnapshotScan(manifest.Files, manifest.Directories), "input.json") {
		return nil, snapshotFailure(SnapshotStageValidate, "input.json", errors.New("input.json collides with a project directory"))
	}

	data := bytes.Clone(input)
	inputHash := sha256.Sum256(data)
	inputFile := SnapshotFile{Path: "input.json", Size: inputSize, SHA256: hex.EncodeToString(inputHash[:])}
	files := make([]SnapshotFile, 0, len(manifest.Files)+1)
	found := false
	for _, file := range manifest.Files {
		if file.Path == inputFile.Path {
			if !found {
				files = append(files, inputFile)
				found = true
			}
			continue
		}
		files = append(files, file)
	}
	if !found {
		files = append(files, inputFile)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest.Files = files

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
		ProjectFS:      snapshotInputOverlayFS{base: base.FS(), data: data},
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
	base fs.FS
	data []byte
}

func (overlay snapshotInputOverlayFS) Open(name string) (fs.File, error) {
	if name == "input.json" {
		info := snapshotFileInfo{name: name, size: int64(len(overlay.data)), mode: 0o444}
		return &snapshotInputOverlayFile{reader: bytes.NewReader(overlay.data), info: info}, nil
	}
	return overlay.base.Open(name)
}

func (overlay snapshotInputOverlayFS) Stat(name string) (fs.FileInfo, error) {
	if name == "input.json" {
		return snapshotFileInfo{name: name, size: int64(len(overlay.data)), mode: 0o444}, nil
	}
	return fs.Stat(overlay.base, name)
}

func (overlay snapshotInputOverlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(overlay.base, name)
	if err != nil || name != "." {
		return entries, err
	}
	rootEntries := make([]fs.DirEntry, 0, len(entries)+1)
	for _, entry := range entries {
		if entry.Name() != "input.json" {
			rootEntries = append(rootEntries, entry)
		}
	}
	info := snapshotFileInfo{name: "input.json", size: int64(len(overlay.data)), mode: 0o444}
	rootEntries = append(rootEntries, snapshotDirEntry{info: info})
	sort.Slice(rootEntries, func(i, j int) bool { return rootEntries[i].Name() < rootEntries[j].Name() })
	return rootEntries, nil
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
