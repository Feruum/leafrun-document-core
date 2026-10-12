package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

type snapshotTestSource struct {
	tree       SourceTree
	err        error
	opens      int
	releases   int
	releaseErr error
}

func (s *snapshotTestSource) Open(context.Context) (SourceTree, error) {
	s.opens++
	tree := s.tree
	tree.Close = func() error { s.releases++; return s.releaseErr }
	return tree, s.err
}

func TestSnapshotCapturesImmutableSortedContentAndDeepCopiesManifest(t *testing.T) {
	input := fstest.MapFS{
		"z.typ":             &fstest.MapFile{Data: []byte("world")},
		"main.typ":          &fstest.MapFile{Data: []byte("hello")},
		"fonts/example.ttf": &fstest.MapFile{Data: []byte("font")},
	}
	source := &snapshotTestSource{tree: SourceTree{FS: input, Origin: SourceOrigin{Kind: "test", Location: "source", Ref: "main", Revision: "revision"}}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil {
		t.Fatal("capture returned no immutable snapshot")
	}
	defer snapshot.Close()
	if source.opens != 1 || source.releases != 1 {
		t.Fatalf("source lifecycle = %d opens, %d releases", source.opens, source.releases)
	}
	manifest := snapshot.Manifest()
	if manifest.FormatVersion != 1 || len(manifest.ID) != 64 {
		t.Fatalf("invalid manifest version or ID: %+v", manifest)
	}
	var paths []string
	for _, file := range manifest.Files {
		paths = append(paths, file.Path)
	}
	if !reflect.DeepEqual(paths, []string{"fonts/example.ttf", "main.typ", "z.typ"}) {
		t.Fatalf("manifest file order = %v", paths)
	}
	wantHash := sha256.Sum256([]byte("hello"))
	if manifest.Files[1].Size != 5 || manifest.Files[1].SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("captured file metadata = %+v", manifest.Files[1])
	}
	if len(manifest.Fonts) != 1 || manifest.Fonts[0].Path != "fonts" || !manifest.Fonts[0].InProject {
		t.Fatalf("default fonts = %+v", manifest.Fonts)
	}
	manifest.Files[0].Path = "changed"
	manifest.Fonts[0].Files[0].Path = "changed"
	manifest.Origin.Location = "changed"
	input["main.typ"].Data[0] = 'X'
	delete(input, "z.typ")
	got, err := fs.ReadFile(snapshot.FS(), "main.typ")
	if err != nil || string(got) != "hello" {
		t.Fatalf("captured bytes = %q, error = %v", got, err)
	}
	if snapshot.Manifest().Files[0].Path != "fonts/example.ttf" || snapshot.Manifest().Fonts[0].Files[0].Path != "example.ttf" || snapshot.Manifest().Origin.Location != "source" {
		t.Fatal("manifest aliases caller-owned memory")
	}
	file, err := snapshot.FS().Open("main.typ")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, ok := file.(interface{ Write([]byte) (int, error) }); ok {
		t.Fatal("snapshot file exposes write access")
	}
	if _, ok := file.(interface{ Name() string }); ok {
		t.Fatal("snapshot file exposes its private storage path")
	}
}

func TestSnapshotContentIDIgnoresOriginTemporaryPathAndModificationTime(t *testing.T) {
	makeSnapshot := func(origin SourceOrigin) *Snapshot {
		snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("same")}}, Origin: origin}}, SnapshotOptions{TempDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot == nil {
			t.Fatal("capture returned no immutable snapshot")
		}
		t.Cleanup(func() {
			if err := snapshot.Close(); err != nil {
				t.Error(err)
			}
		})
		return snapshot
	}
	first := makeSnapshot(SourceOrigin{Kind: "folder", Location: "one"})
	second := makeSnapshot(SourceOrigin{Kind: "git", Location: "two", Ref: "branch", Revision: "commit"})
	if first.Manifest().ID != second.Manifest().ID {
		t.Fatalf("equal content has different IDs: %s / %s", first.Manifest().ID, second.Manifest().ID)
	}
	if first.Manifest().Origin.Revision != first.Manifest().ID {
		t.Fatal("folder revision does not identify captured content")
	}
}

func TestSnapshotReleaseFailureRejectsSnapshotAndPreservesCause(t *testing.T) {
	cause := errors.New("source release failed")
	source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("hello")}}}, releaseErr: cause}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || !errors.Is(err, cause) {
		t.Fatalf("release failure returned snapshot=%v error=%v", snapshot, err)
	}
	var stage *SnapshotError
	if !errors.As(err, &stage) || stage.Stage != SnapshotStageRelease || !strings.Contains(err.Error(), "release") {
		t.Fatalf("release error lacks typed stage context: %v", err)
	}
	if source.opens != 1 || source.releases != 1 {
		t.Fatalf("source lifecycle = %d opens, %d releases", source.opens, source.releases)
	}
}

func TestSnapshotAlwaysVerifiesBytesAndRejectsSameSizeSameTimeChanges(t *testing.T) {
	for _, sequence := range [][]string{{"AAAA", "BBBB", "BBBB"}, {"AAAA", "BBBB", "AAAA"}, {"AAAA", "AAAA", "BBBB"}} {
		t.Run(strings.Join(sequence, "-"), func(t *testing.T) {
			input := newScriptFS("AAAA")
			input.readData = func(open int) []byte { return []byte(sequence[open-1]) }
			source := &snapshotTestSource{tree: SourceTree{FS: input}}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("changing source returned snapshot=%v error=%v", snapshot, err)
			}
			if input.opens != 3 {
				t.Fatalf("source file opened %d times; wanted scan, copy, verification without retries", input.opens)
			}
			if source.releases != 1 {
				t.Fatalf("source released %d times", source.releases)
			}
		})
	}
}

func TestSnapshotVerificationScanRunsAfterCopyReadFailure(t *testing.T) {
	cause := errors.New("copy read failed")
	input := newScriptFS("AAAA")
	input.readError = func(open int) error {
		if open == 2 {
			return cause
		}
		return nil
	}
	source := &snapshotTestSource{tree: SourceTree{FS: input}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || !errors.Is(err, cause) {
		t.Fatalf("copy failure returned snapshot=%v error=%v", snapshot, err)
	}
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("incomplete copy did not identify changed source: %v", err)
	}
	if input.opens != 3 {
		t.Fatalf("copy failure skipped verification: %d opens", input.opens)
	}
}

func TestSnapshotDestinationFailuresDoNotIdentifySourceChanges(t *testing.T) {
	cause := errors.New("destination write failed")
	digest := sha256.Sum256([]byte("AAAA"))
	expected := snapshotScanFile{SnapshotFile: SnapshotFile{Path: "main.typ", Size: 4, SHA256: hex.EncodeToString(digest[:])}}
	limits, err := snapshotLimits(SnapshotLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = readSnapshotFile(context.Background(), newScriptFS("AAAA"), expected, snapshotFailWriter{cause}, &snapshotBudget{limits: limits})
	if !errors.Is(err, cause) || errors.Is(err, ErrSourceChanged) {
		t.Fatalf("destination failure classification = %v", err)
	}
}

type snapshotFailWriter struct{ cause error }

func (writer snapshotFailWriter) Write([]byte) (int, error) { return 0, writer.cause }

func TestSnapshotRejectsUnsafeEntriesAndInvalidPaths(t *testing.T) {
	for _, item := range []struct {
		name string
		mode fs.FileMode
	}{
		{"../escape", 0}, {"..", 0}, {"slash/name", 0}, {`back\name`, 0}, {"", 0},
		{"link", fs.ModeSymlink}, {"junction", fs.ModeIrregular}, {"pipe", fs.ModeNamedPipe},
		{"socket", fs.ModeSocket}, {"device", fs.ModeDevice},
	} {
		t.Run(fmtSafeName(item.name, item.mode), func(t *testing.T) {
			input := newScriptFS("AAAA")
			input.entries = []fs.DirEntry{testDirEntry{name: item.name, mode: item.mode}}
			source := &snapshotTestSource{tree: SourceTree{FS: input}}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
			if snapshot != nil || err == nil {
				t.Fatalf("unsafe entry %q (%v) accepted", item.name, item.mode)
			}
			if source.releases != 1 {
				t.Fatalf("unsafe source released %d times", source.releases)
			}
		})
	}
}

func TestSnapshotRejectsDuplicateDirectoryEntries(t *testing.T) {
	input := newScriptFS("AAAA")
	input.entries = []fs.DirEntry{testDirEntry{name: "main.typ"}, testDirEntry{name: "main.typ"}}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || err == nil {
		t.Fatal("duplicate directory entries accepted")
	}
}

func TestSnapshotEnforcesCountsAndActualByteLimits(t *testing.T) {
	for _, item := range []struct {
		name   string
		input  fs.FS
		limits SnapshotLimits
	}{
		{"files", fstest.MapFS{"a": &fstest.MapFile{Data: []byte("a")}, "b": &fstest.MapFile{Data: []byte("b")}}, SnapshotLimits{MaxFiles: 1}},
		{"total", fstest.MapFS{"a": &fstest.MapFile{Data: []byte("aaa")}, "b": &fstest.MapFile{Data: []byte("bbb")}}, SnapshotLimits{MaxTotalBytes: 5}},
		{"file", fstest.MapFS{"a": &fstest.MapFile{Data: []byte("aaaa")}}, SnapshotLimits{MaxFileBytes: 3}},
		{"negative", fstest.MapFS{"a": &fstest.MapFile{Data: []byte("a")}}, SnapshotLimits{MaxFiles: -1}},
	} {
		t.Run(item.name, func(t *testing.T) {
			snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: item.input}}, SnapshotOptions{TempDir: t.TempDir(), Limits: item.limits})
			if snapshot != nil || err == nil {
				t.Fatalf("%s limit was not enforced", item.name)
			}
			if item.name != "negative" && !errors.Is(err, ErrSnapshotLimit) {
				t.Fatalf("limit cause omitted: %v", err)
			}
		})
	}
	input := newScriptFS("too many real bytes")
	input.reportedSize = 0
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir(), Limits: SnapshotLimits{MaxFileBytes: 8}})
	if snapshot != nil || !errors.Is(err, ErrSnapshotLimit) {
		t.Fatalf("dishonest file size bypassed actual byte limit: %v", err)
	}
}

func TestSnapshotAcquisitionAndCancellationPreserveCausesAndRelease(t *testing.T) {
	cause := errors.New("acquisition failed")
	source := &snapshotTestSource{err: cause}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || !errors.Is(err, cause) || source.releases != 1 {
		t.Fatalf("acquisition failure snapshot=%v error=%v releases=%d", snapshot, err, source.releases)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source = &snapshotTestSource{}
	snapshot, err = CreateSnapshot(ctx, source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || !errors.Is(err, context.Canceled) || source.opens != 0 {
		t.Fatalf("pre-canceled capture snapshot=%v error=%v opens=%d", snapshot, err, source.opens)
	}
	input := newScriptFS("AAAA")
	ctx, cancel = context.WithCancel(context.Background())
	input.readData = func(open int) []byte {
		if open == 2 {
			cancel()
		}
		return []byte("AAAA")
	}
	source = &snapshotTestSource{tree: SourceTree{FS: input}}
	snapshot, err = CreateSnapshot(ctx, source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil || !errors.Is(err, context.Canceled) || source.releases != 1 {
		t.Fatalf("mid-copy cancellation snapshot=%v error=%v releases=%d", snapshot, err, source.releases)
	}
}

func TestSnapshotCloseInvalidatesOpenReadersWithoutWaitingAndReturnsCleanupErrors(t *testing.T) {
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("hello")}}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil {
		t.Fatal("capture returned no snapshot")
	}
	file, err := snapshot.FS().Open("main.typ")
	if err != nil {
		t.Fatal(err)
	}
	root := snapshot.root
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed storage remains: %v", err)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("open reader remained usable after Close: %v", err)
	}
	if _, err := snapshot.FS().Open("main.typ"); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("closed snapshot remained readable: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err = CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	root = snapshot.root
	t.Cleanup(func() { os.RemoveAll(root) })
	cause := errors.New("remove failed")
	calls := 0
	snapshot.cleanup = func(string) error { calls++; return cause }
	if err := snapshot.Close(); !errors.Is(err, cause) {
		t.Fatalf("cleanup failure omitted: %v", err)
	}
	if err := snapshot.Close(); !errors.Is(err, cause) || calls != 1 {
		t.Fatalf("repeated Close = %v, cleanup calls = %d", err, calls)
	}
}

func TestSnapshotReadOnlyFSSupportsDirectoriesAndRejectsInvalidPaths(t *testing.T) {
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"empty": &fstest.MapFile{Mode: fs.ModeDir}, "nested/main.typ": &fstest.MapFile{Data: []byte("hello")}}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	if err := fstest.TestFS(snapshot.FS(), "nested/main.typ"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "/absolute", `nested\main.typ`, "nested/../main.typ"} {
		if _, err := snapshot.FS().Open(name); err == nil {
			t.Fatalf("invalid fs path %q accepted", name)
		}
	}
}

func TestSnapshotFailureRemovesPrivateStorage(t *testing.T) {
	storage := t.TempDir()
	source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("hello")}}}, releaseErr: errors.New("release failed")}
	if snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage}); snapshot != nil || err == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	entries, err := os.ReadDir(storage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed capture left private storage: %v", entries)
	}
}

func TestSnapshotCapturesDeclaredFontDirectoriesInOrder(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "sample.ttf"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "sample.ttf"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("document")}, "fonts/default.ttf": &fstest.MapFile{Data: []byte("default")}, "alternate/relative.ttf": &fstest.MapFile{Data: []byte("relative")}}
	capture := func(fonts []string) *Snapshot {
		snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: project}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: fonts})
		if err != nil || snapshot == nil {
			t.Fatalf("capture = %v, %v", snapshot, err)
		}
		t.Cleanup(func() {
			if err := snapshot.Close(); err != nil {
				t.Error(err)
			}
		})
		return snapshot
	}
	snapshot := capture([]string{"alternate", first, second})
	manifest := snapshot.Manifest()
	if len(manifest.Fonts) != 4 {
		t.Fatalf("font directories = %+v", manifest.Fonts)
	}
	if manifest.Fonts[0].Path != "fonts" || manifest.Fonts[1].Path != "alternate" || manifest.Fonts[2].InProject || manifest.Fonts[2].SourcePath != first {
		t.Fatalf("font order/provenance = %+v", manifest.Fonts)
	}
	if snapshot.Manifest().ID == capture([]string{"alternate", second, first}).Manifest().ID {
		t.Fatal("content ID ignored font search order")
	}
	third := t.TempDir()
	if err := os.WriteFile(filepath.Join(third, "sample.ttf"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest().ID != capture([]string{"alternate", third, second}).Manifest().ID {
		t.Fatal("absolute font source path entered content ID")
	}
	if err := os.WriteFile(filepath.Join(first, "sample.ttf"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil || working == nil {
		t.Fatalf("working copy = %v, %v", working, err)
	}
	defer working.Close()
	data, err := os.ReadFile(filepath.Join(working.fonts[2], "sample.ttf"))
	if err != nil || string(data) != "one" {
		t.Fatalf("font was not frozen: %q, %v", data, err)
	}
}

func TestSnapshotAdditionalFontBytesShareProjectLimits(t *testing.T) {
	fontRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fontRoot, "sample.ttf"), []byte("font"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("text")}}}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{fontRoot}, Limits: SnapshotLimits{MaxTotalBytes: 7}})
	if snapshot != nil || !errors.Is(err, ErrSnapshotLimit) {
		t.Fatalf("external fonts bypassed combined limit: %v", err)
	}
}

func TestSnapshotRejectsMissingAndNonDirectoryFontPaths(t *testing.T) {
	for _, name := range []string{"", "missing", "main.typ", "../outside"} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("text")}}}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{name}})
			if snapshot != nil || err == nil {
				t.Fatalf("invalid font path %q accepted", name)
			}
		})
	}
}

func TestSnapshotNativeCopiesNeverShareHardlinksAndRetainOriginalIdentities(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "main.typ")
	if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(project, "alias.typ")); err != nil {
		t.Fatal(err)
	}
	originalFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := originalFile.Stat()
	if closeErr := originalFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	source := snapshotSourceFunc(func(context.Context) (SourceTree, error) {
		return SourceTree{FS: os.DirFS(project), ProtectedPaths: []string{project}, Close: func() error { return os.RemoveAll(project) }}, nil
	})
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	captured, err := os.Stat(filepath.Join(snapshot.root, "project", "main.typ"))
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Stat(filepath.Join(snapshot.root, "project", "alias.typ"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, captured) || os.SameFile(captured, alias) {
		t.Fatal("snapshot shared source or captured hardlinks")
	}
	if len(snapshot.inputs) != 2 {
		t.Fatalf("retained original identities = %+v", snapshot.inputs)
	}
	for _, input := range snapshot.inputs {
		if !os.SameFile(original, input.info) {
			t.Fatalf("original input identity was not retained: %+v", input)
		}
	}
}

func TestSnapshotNativeProtectedDirectoryOmitsExcludedProjectFiles(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "old.pdf"), []byte("old output"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("text")}}, ProtectedPaths: []string{project}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	if len(snapshot.inputs) != 1 || snapshot.inputs[0].projectPath != "main.typ" {
		t.Fatalf("excluded output retained as captured input: %+v", snapshot.inputs)
	}
}

func TestSnapshotDetectsNativeIdentityReplacementWithUnchangedMetadata(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive.zip")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(archive, filepath.Join(root, "keep-original")); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	input := newScriptFS("AAAA")
	input.readData = func(open int) []byte {
		if open == 2 {
			if err := os.Remove(archive); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(archive, original.ModTime(), original.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
		return []byte("AAAA")
	}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input, ProtectedPaths: []string{archive}}}, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("native input replacement was not detected: snapshot=%v error=%v", snapshot, err)
	}
}

func TestSnapshotRejectsStorageInsideNativeInputTrees(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: os.DirFS(project), ProtectedPaths: []string{project}}}, SnapshotOptions{TempDir: project})
	if snapshot != nil || err == nil {
		t.Fatal("snapshot storage inside native input tree accepted")
	}
}

func TestSnapshotWorkingCopySurvivesSnapshotCloseAndOwnsIndependentBytes(t *testing.T) {
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("text")}}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil || working == nil {
		t.Fatalf("prepare = %v, %v", working, err)
	}
	defer working.Close()
	captured, err := os.Stat(filepath.Join(snapshot.root, "project", "main.typ"))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.Stat(filepath.Join(working.root, "main.typ"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(captured, copied) {
		t.Fatal("compiler working copy shares snapshot hardlinks")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(working.root, "main.typ"))
	if err != nil || string(data) != "text" {
		t.Fatalf("Close deleted accepted compiler inputs: %q, %v", data, err)
	}
	if _, err := snapshot.prepareWorkingCopy(context.Background()); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("closed snapshot accepted compiler copy: %v", err)
	}
	storage := working.storage
	if err := working.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("working storage remains: %v", err)
	}
}

func TestSnapshotConcurrentReadsManifestCopiesAndClose(t *testing.T) {
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("text")}}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	done := make(chan error, 8)
	for n := 0; n < 8; n++ {
		go func() {
			for i := 0; i < 20; i++ {
				manifest := snapshot.Manifest()
				manifest.Files[0].Path = "caller mutation"
				data, err := fs.ReadFile(snapshot.FS(), "main.typ")
				if err != nil && !errors.Is(err, ErrSnapshotClosed) {
					done <- err
					return
				}
				if err == nil && string(data) != "text" {
					done <- errors.New("changed captured bytes")
					return
				}
			}
			done <- nil
		}()
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 8; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotManifestPreservesCanonicalEmptyLists(t *testing.T) {
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{}}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	manifest := snapshot.Manifest()
	if manifest.Files == nil || manifest.Directories == nil || manifest.Fonts == nil {
		t.Fatal("manifest copy changed canonical empty lists into nil")
	}
	computed, err := snapshotContentID(manifest)
	if err != nil || computed != manifest.ID {
		t.Fatalf("manifest copy changed content identity: %s / %s, %v", computed, manifest.ID, err)
	}
}

func TestSnapshotRejectsMalformedFilesystemResultsWithoutPanicking(t *testing.T) {
	for _, name := range []string{"nil-root-info", "nil-entry", "nil-entry-info", "nil-file", "nil-file-info", "inconsistent-entry-type"} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if failure := recover(); failure != nil {
					t.Fatalf("malformed filesystem caused panic: %v", failure)
				}
			}()
			input := newScriptFS("AAAA")
			switch name {
			case "nil-root-info":
				input.nilRootInfo = true
			case "nil-entry":
				input.entries = []fs.DirEntry{nil}
			case "nil-entry-info":
				input.entries = []fs.DirEntry{testDirEntry{name: "main.typ", nilInfo: true}}
			case "nil-file":
				input.nilFile = true
			case "nil-file-info":
				input.nilFileInfo = true
			case "inconsistent-entry-type":
				input.entries = []fs.DirEntry{testDirEntry{name: "main.typ", overrideType: true, entryType: fs.ModeDir}}
			}
			snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir()})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || err == nil {
				t.Fatalf("malformed filesystem %s accepted", name)
			}
		})
	}
}

func TestSnapshotRejectsCaseCollisionsThatCannotBeStoredOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows storage is case insensitive")
	}
	input := fstest.MapFS{"A/one.typ": &fstest.MapFile{Data: []byte("one")}, "a/two.typ": &fstest.MapFile{Data: []byte("two")}}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || err == nil {
		t.Fatal("case-colliding directories accepted into ambiguous native storage")
	}
}

func TestSnapshotSelectedNativeRootAliasesAreCanonicalized(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project-alias")
	makeTestDirectoryLink(t, project, alias)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: os.DirFS(alias), ProtectedPaths: []string{alias}}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{alias}})
	if err != nil || snapshot == nil {
		t.Fatalf("selected root alias rejected: %v", err)
	}
	defer snapshot.Close()
	if len(snapshot.Manifest().Fonts) != 1 || len(snapshot.inputs) != 2 {
		t.Fatalf("alias lost captured inputs or font manifest: %+v", snapshot.Manifest())
	}
}

func TestSnapshotRejectsTemporaryStorageAliasesIntoInputsBeforeCreation(t *testing.T) {
	project := t.TempDir()
	alias := filepath.Join(t.TempDir(), "storage-alias")
	makeTestDirectoryLink(t, project, alias)
	if err := rejectSnapshotStorage(alias, []string{project}, nil); err == nil {
		t.Fatal("temporary storage alias bypassed native input containment")
	}
}

func TestSnapshotRejectsNativeFontLinksWithinSelectedTree(t *testing.T) {
	fonts := t.TempDir()
	nested := filepath.Join(fonts, "ordinary")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "sample.ttf"), []byte("font"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeTestDirectoryLink(t, nested, filepath.Join(fonts, "linked"))
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{}}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{fonts}})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || err == nil {
		t.Fatal("link inside native font tree accepted")
	}
}

func TestSnapshotNativeFSRejectsReplacementOfItsPinnedRoot(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	native, err := openSnapshotNativeFS(project)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	if err := os.Rename(project, filepath.Join(parent, "original")); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) {
			t.Fatal(err)
		}
		data, readErr := fs.ReadFile(native, "main.typ")
		if readErr != nil || string(data) != "inside" {
			t.Fatalf("pinned root did not prevent replacement: %q, %v", data, readErr)
		}
		return
	}
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(native, "main.typ")
	if string(data) == "replacement" || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replacement root read escaped pinned tree: %q, %v", data, err)
	}
}

type snapshotSourceFunc func(context.Context) (SourceTree, error)

func (f snapshotSourceFunc) Open(ctx context.Context) (SourceTree, error) { return f(ctx) }

type scriptFS struct {
	base         fs.FS
	opens        int
	readData     func(int) []byte
	readError    func(int) error
	reportedSize int64
	entries      []fs.DirEntry
	nilRootInfo  bool
	nilFile      bool
	nilFileInfo  bool
}

func newScriptFS(data string) *scriptFS {
	return &scriptFS{base: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte(data)}}, reportedSize: int64(len(data))}
}
func (f *scriptFS) Open(name string) (fs.File, error) {
	file, err := f.base.Open(name)
	if err != nil || name != "main.typ" {
		return file, err
	}
	f.opens++
	if f.nilFile {
		file.Close()
		return nil, nil
	}
	var data []byte
	if f.readData != nil {
		data = f.readData(f.opens)
	} else {
		data, _ = fs.ReadFile(f.base, name)
	}
	var readErr error
	if f.readError != nil {
		readErr = f.readError(f.opens)
	}
	var info fs.FileInfo = testFileInfo{name: name, size: f.reportedSize}
	if f.nilFileInfo {
		info = nil
	}
	return &scriptFile{File: file, reader: bytes.NewReader(data), info: info, readErr: readErr}, nil
}
func (f *scriptFS) Stat(name string) (fs.FileInfo, error) {
	if name == "." && f.nilRootInfo {
		return nil, nil
	}
	if name == "main.typ" {
		return testFileInfo{name: name, size: f.reportedSize}, nil
	}
	return fs.Stat(f.base, name)
}
func (f *scriptFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "." && f.entries != nil {
		return append([]fs.DirEntry(nil), f.entries...), nil
	}
	if name == "." {
		return []fs.DirEntry{scriptEntry{testDirEntry{name: "main.typ"}, f.reportedSize}}, nil
	}
	return fs.ReadDir(f.base, name)
}

type scriptEntry struct {
	testDirEntry
	size int64
}

func (d scriptEntry) Info() (fs.FileInfo, error) {
	return testFileInfo{name: d.name, mode: d.mode, size: d.size}, nil
}

type scriptFile struct {
	fs.File
	reader  *bytes.Reader
	info    fs.FileInfo
	readErr error
}

func (f *scriptFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.reader.Read(p)
}
func (f *scriptFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type testDirEntry struct {
	name         string
	mode         fs.FileMode
	nilInfo      bool
	overrideType bool
	entryType    fs.FileMode
}

func (d testDirEntry) Name() string { return d.name }
func (d testDirEntry) Type() fs.FileMode {
	if d.overrideType {
		return d.entryType
	}
	return d.mode.Type()
}
func (d testDirEntry) IsDir() bool { return d.mode.IsDir() }
func (d testDirEntry) Info() (fs.FileInfo, error) {
	if d.nilInfo {
		return nil, nil
	}
	return testFileInfo{name: d.name, mode: d.mode, size: 4}, nil
}

type testFileInfo struct {
	name string
	mode fs.FileMode
	size int64
}

func (i testFileInfo) Name() string       { return i.name }
func (i testFileInfo) Size() int64        { return i.size }
func (i testFileInfo) Mode() fs.FileMode  { return i.mode }
func (i testFileInfo) ModTime() time.Time { return time.Time{} }
func (i testFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i testFileInfo) Sys() any           { return nil }
func fmtSafeName(name string, mode fs.FileMode) string {
	return strings.ReplaceAll(name, "/", "_") + mode.String()
}

var _ io.Reader = (*scriptFile)(nil)
