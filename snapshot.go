package documentcore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"
)

// ProjectSource acquires one project tree. CreateSnapshot takes ownership of
// the returned release callback, including when Open also returns an error.
type ProjectSource interface {
	Open(context.Context) (SourceTree, error)
}

// SourceTree is a project rooted at "." in FS. Close must release the source
// and is called exactly once before CreateSnapshot returns. ProtectedPaths are
// original native inputs whose identities must survive source release for
// output guards. Native directories mirror the relative paths in FS.
type SourceTree struct {
	FS             fs.FS
	Origin         SourceOrigin
	Close          func() error
	ProtectedPaths []string
}

// SourceOrigin records provenance independently of the content identity.
type SourceOrigin struct {
	Kind          string `json:"kind"`
	Location      string `json:"location"`
	SourceRoot    string `json:"sourceRoot"`
	Ref           string `json:"ref"`
	Revision      string `json:"revision"`
	ArchiveSHA256 string `json:"archiveSHA256"`
}

// SnapshotLimits bounds all project and additional font bytes captured together.
// A zero field selects its default: 10000 files, 512 MiB total, 128 MiB per file.
type SnapshotLimits struct {
	MaxFiles      int   `json:"maxFiles"`
	MaxTotalBytes int64 `json:"maxTotalBytes"`
	MaxFileBytes  int64 `json:"maxFileBytes"`
}

// SnapshotOptions selects ordered font directories and private storage.
// Relative FontPaths are io/fs subdirectories; absolute paths are native
// directories. Output identifies a generated result for source adapters that
// support output exclusion; common capture never silently excludes an input.
type SnapshotOptions struct {
	FontPaths []string
	Output    string
	TempDir   string
	Limits    SnapshotLimits
}

// SnapshotFile records the path and SHA-256 of exactly the captured bytes.
type SnapshotFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SnapshotFont records a font search directory in declared search order. Path
// is logical and never contains a temporary path. SourcePath is provenance for
// an absolute font directory and does not participate in the content identity.
type SnapshotFont struct {
	Path        string         `json:"path"`
	SourcePath  string         `json:"sourcePath,omitempty"`
	InProject   bool           `json:"inProject"`
	Files       []SnapshotFile `json:"files"`
	Directories []string       `json:"directories"`
}

// SnapshotManifest is a deep-copied description of an immutable capture. ID is
// the SHA-256 of the version, sorted project tree, and ordered font trees. It
// ignores provenance, modification times, traversal order, and temporary paths.
type SnapshotManifest struct {
	FormatVersion int            `json:"formatVersion"`
	ID            string         `json:"id"`
	Origin        SourceOrigin   `json:"origin"`
	Files         []SnapshotFile `json:"files"`
	Directories   []string       `json:"directories"`
	Fonts         []SnapshotFont `json:"fonts"`
}

var (
	ErrSnapshotClosed = errors.New("snapshot is closed")
	ErrSourceChanged  = errors.New("source changed during snapshot capture")
	ErrSnapshotLimit  = errors.New("snapshot limit exceeded")
)

// SnapshotStage identifies a capture or lifecycle failure.
type SnapshotStage string

const (
	SnapshotStageAcquire  SnapshotStage = "acquire"
	SnapshotStageValidate SnapshotStage = "validate"
	SnapshotStageScan     SnapshotStage = "scan"
	SnapshotStageCapture  SnapshotStage = "capture"
	SnapshotStageVerify   SnapshotStage = "verify"
	SnapshotStageRelease  SnapshotStage = "release"
	SnapshotStageStorage  SnapshotStage = "storage"
	SnapshotStagePrepare  SnapshotStage = "prepare"
	SnapshotStageClose    SnapshotStage = "close"
)

// SnapshotError retains the stage, logical input path, and original cause.
type SnapshotError struct {
	Stage SnapshotStage
	Path  string
	Cause error
}

func (e *SnapshotError) Error() string {
	message := fmt.Sprintf("snapshot %s failed", e.Stage)
	if e.Path != "" {
		message += fmt.Sprintf(" for %q", e.Path)
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *SnapshotError) Unwrap() error { return e.Cause }

// Snapshot owns private copied bytes. Use FS for read-only access, Manifest for
// a value description, and Close to release storage. Its zero value is closed.
type Snapshot struct {
	mu       sync.Mutex
	root     string
	tempDir  string
	manifest SnapshotManifest
	closed   bool
	closeErr error
	cleanup  func(string) error
	files    map[*snapshotOpenFile]struct{}
	inputs   []snapshotInput
	limits   SnapshotLimits
	fsInfo   map[string]snapshotFileInfo
}

// CreateSnapshot opens source once and releases it before returning. It does
// not retry changing sources. All source trees must remain stable throughout
// the scan, capture, and unconditional verification scan.
func CreateSnapshot(ctx context.Context, source ProjectSource, options SnapshotOptions) (snapshot *Snapshot, err error) {
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", err)
	}
	if source == nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", errors.New("project source is required"))
	}
	options.FontPaths = append([]string(nil), options.FontPaths...)
	if configurable, ok := source.(snapshotOptionsSource); ok {
		source = configurable.withSnapshotOptions(options)
		if source == nil {
			return nil, snapshotFailure(SnapshotStageAcquire, "", errors.New("configured project source is required"))
		}
	}
	tree, openErr := source.Open(ctx)
	if tree.Close != nil {
		defer func() {
			if releaseErr := tree.Close(); releaseErr != nil {
				err = errors.Join(err, snapshotFailure(SnapshotStageRelease, "", releaseErr))
			}
			if ctx.Err() != nil {
				err = errors.Join(err, snapshotFailure(SnapshotStageAcquire, "", ctx.Err()))
			}
			if err != nil && snapshot != nil {
				err = errors.Join(err, snapshot.Close())
				snapshot = nil
			}
		}()
	}
	if openErr != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", errors.Join(openErr, ctx.Err()))
	}
	if tree.Close == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("source release callback is required"))
	}
	if tree.FS == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("source filesystem is required"))
	}
	return captureSnapshot(ctx, tree, options)
}

// Manifest returns a value copy, including independent slices for font files.
func (s *Snapshot) Manifest() SnapshotManifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest := s.manifest
	manifest.Files = slices.Clone(manifest.Files)
	manifest.Directories = slices.Clone(manifest.Directories)
	manifest.Fonts = slices.Clone(manifest.Fonts)
	for i := range manifest.Fonts {
		manifest.Fonts[i].Files = slices.Clone(manifest.Fonts[i].Files)
		manifest.Fonts[i].Directories = slices.Clone(manifest.Fonts[i].Directories)
	}
	return manifest
}

// FS returns a read-only project filesystem. All operations reject a closed
// snapshot; files opened before Close are also closed by Close.
func (s *Snapshot) FS() fs.FS { return snapshotView{s} }

// Close releases private storage once and returns the same cleanup result on
// subsequent calls. It is safe concurrently with filesystem operations.
func (s *Snapshot) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	for file := range s.files {
		s.closeErr = errors.Join(s.closeErr, file.file.Close())
		file.closed = true
		delete(s.files, file)
	}
	if s.root != "" {
		cleanup := s.cleanup
		if cleanup == nil {
			cleanup = os.RemoveAll
		}
		s.closeErr = errors.Join(s.closeErr, cleanup(s.root))
	}
	if s.closeErr != nil {
		s.closeErr = snapshotFailure(SnapshotStageClose, "", s.closeErr)
	}
	return s.closeErr
}

type snapshotView struct{ snapshot *Snapshot }

type snapshotInput struct {
	path, projectPath string
	font              bool
	info              fs.FileInfo
}

type snapshotOptionsSource interface {
	withSnapshotOptions(SnapshotOptions) ProjectSource
}
type snapshotNativeFontSource interface {
	snapshotFontFS(root string) (fs.FS, error)
}

func snapshotFailure(stage SnapshotStage, path string, cause error) *SnapshotError {
	return &SnapshotError{Stage: stage, Path: path, Cause: cause}
}
