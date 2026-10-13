package documentcore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func bundleFixture(t *testing.T) (*Snapshot, SnapshotBundle, fstest.MapFS, fstest.MapFS) {
	t.Helper()
	project := fstest.MapFS{
		"main.typ":           {Data: []byte("Portable document")},
		"fonts/internal.ttf": {Data: []byte("internal")},
		"empty":              {Mode: fs.ModeDir | 0755},
	}
	external := t.TempDir()
	writeTestFile(t, filepath.Join(external, "external.ttf"), "external")
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: project, Origin: SourceOrigin{Kind: "git", Location: "https://example.test/repo", Ref: "main", Revision: "abc", ArchiveSHA256: "provenance"}}}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{external, "fonts", external}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.RemoveAll(external); err != nil {
		t.Fatal(err)
	}
	externalFS := fstest.MapFS{"external.ttf": {Data: []byte("external")}}
	bundle := SnapshotBundle{Manifest: snapshot.Manifest(), ProjectFS: project, ExternalFontFS: map[int]fs.FS{1: externalFS, 3: externalFS}}
	return snapshot, bundle, project, externalFS
}

func TestSnapshotFontFSUsesCapturedOrderedTrees(t *testing.T) {
	snapshot, _, _, _ := bundleFixture(t)
	for index, name := range []string{"internal.ttf", "external.ttf", "internal.ttf", "external.ttf"} {
		fontFS, err := snapshot.FontFS(index)
		if err != nil {
			t.Fatal(err)
		}
		data, err := fs.ReadFile(fontFS, name)
		if err != nil || string(data) != strings.TrimSuffix(filepath.Base(name), ".ttf") {
			t.Fatalf("font %d: %q, %v", index, data, err)
		}
		if err := fstest.TestFS(fontFS, name); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{"../main.typ", "/main.typ", `nested\external.ttf`} {
			if _, err := fontFS.Open(invalid); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("invalid font path %q: %v", invalid, err)
			}
		}
		file, err := fontFS.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil || info.Sys() != nil || info.Mode().Perm() != 0444 {
			t.Fatalf("font metadata: %v, %v", info, err)
		}
		if _, ok := file.(interface{ Name() string }); ok {
			t.Fatal("font reader exposes native path")
		}
		if _, ok := file.(io.Writer); ok {
			t.Fatal("font reader exposes write access")
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, index := range []int{-1, 4} {
		if _, err := snapshot.FontFS(index); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("index %d: %v", index, err)
		}
	}
	fontFS, _ := snapshot.FontFS(1)
	file, _ := fontFS.Open("external.ttf")
	dir, _ := fontFS.Open(".")
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("open font reader survived close: %v", err)
	}
	if _, err := file.Stat(); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("open font stat survived close: %v", err)
	}
	if _, err := dir.(fs.ReadDirFile).ReadDir(-1); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("font directory survived close: %v", err)
	}
	if _, err := fontFS.Open("."); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("font FS survived close: %v", err)
	}
	if _, err := snapshot.FontFS(0); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("closed font lookup: %v", err)
	}
	var zero Snapshot
	if _, err := zero.FontFS(0); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("zero font lookup: %v", err)
	}
}

func TestRestoreSnapshotRoundtripOwnsBytesAndProvenance(t *testing.T) {
	original, bundle, project, external := bundleFixture(t)
	// Serialize from the captured readers; original native fonts are already gone.
	bundle.ProjectFS = original.FS()
	for index := range bundle.ExternalFontFS {
		bundle.ExternalFontFS[index], _ = original.FontFS(index)
	}
	restored, err := RestoreSnapshot(context.Background(), bundle, RestoreSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !reflect.DeepEqual(bundle.Manifest, restored.Manifest()) {
		t.Fatalf("roundtrip changed manifest: %+v", restored.Manifest())
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	project["main.typ"].Data[0] = 'X'
	external["external.ttf"].Data[0] = 'X'
	data, err := fs.ReadFile(restored.FS(), "main.typ")
	if err != nil || string(data) != "Portable document" {
		t.Fatalf("borrowed source affected restore: %q, %v", data, err)
	}
	for index := range restored.Manifest().Fonts {
		fontFS, err := restored.FontFS(index)
		if err != nil {
			t.Fatal(err)
		}
		name, want := "internal.ttf", "internal"
		if index%2 == 1 {
			name, want = "external.ttf", "external"
		}
		data, err := fs.ReadFile(fontFS, name)
		if err != nil || string(data) != want {
			t.Fatalf("restored font %d: %q, %v", index, data, err)
		}
	}
	manifest := restored.Manifest()
	manifest.Fonts[0].Files[0].Path = "changed"
	if restored.Manifest().Fonts[0].Files[0].Path == "changed" {
		t.Fatal("restore manifest aliases caller")
	}
}

func TestRestoreSnapshotRejectsTreeMismatchAndCleansStorage(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SnapshotBundle, fstest.MapFS, fstest.MapFS)
	}{
		{"tampered project", func(_ *SnapshotBundle, project, _ fstest.MapFS) { project["main.typ"].Data[0] = 'X' }},
		{"tampered font", func(_ *SnapshotBundle, _, external fstest.MapFS) { external["external.ttf"].Data[0] = 'X' }},
		{"missing file", func(_ *SnapshotBundle, project, _ fstest.MapFS) { delete(project, "main.typ") }},
		{"extra file", func(_ *SnapshotBundle, project, _ fstest.MapFS) {
			project["extra"] = &fstest.MapFile{Data: []byte("extra")}
		}},
		{"missing empty directory", func(_ *SnapshotBundle, project, _ fstest.MapFS) { delete(project, "empty") }},
		{"extra empty directory", func(_ *SnapshotBundle, project, _ fstest.MapFS) { project["extra"] = &fstest.MapFile{Mode: fs.ModeDir} }},
		{"missing external", func(bundle *SnapshotBundle, _, _ fstest.MapFS) { delete(bundle.ExternalFontFS, 1) }},
		{"extra external", func(bundle *SnapshotBundle, _, external fstest.MapFS) { bundle.ExternalFontFS[8] = external }},
		{"in-project external", func(bundle *SnapshotBundle, _, external fstest.MapFS) { bundle.ExternalFontFS[0] = external }},
		{"nil external", func(bundle *SnapshotBundle, _, _ fstest.MapFS) { bundle.ExternalFontFS[1] = nil }},
		{"nil project", func(bundle *SnapshotBundle, _, _ fstest.MapFS) { bundle.ProjectFS = nil }},
		{"symlink", func(_ *SnapshotBundle, project, _ fstest.MapFS) { project["main.typ"].Mode = fs.ModeSymlink }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, bundle, project, external := bundleFixture(t)
			test.mutate(&bundle, project, external)
			temp := t.TempDir()
			snapshot, err := RestoreSnapshot(context.Background(), bundle, RestoreSnapshotOptions{TempDir: temp})
			if snapshot != nil || err == nil {
				t.Fatalf("invalid bundle accepted: %v, %v", snapshot, err)
			}
			entries, readErr := os.ReadDir(temp)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("restore leaked storage: %v, %v", entries, readErr)
			}
		})
	}
}

func TestValidateSnapshotManifestAndContentIdentity(t *testing.T) {
	_, bundle, _, _ := bundleFixture(t)
	manifest := bundle.Manifest
	if err := ValidateSnapshotManifest(manifest); err != nil {
		t.Fatal(err)
	}
	id, err := SnapshotContentID(manifest)
	if err != nil || id != manifest.ID {
		t.Fatalf("public identity: %s, %v", id, err)
	}
	manifest.ID = ""
	manifest.Origin = SourceOrigin{Kind: "other"}
	manifest.Fonts[1].SourcePath = "/missing/native/provenance"
	id, err = SnapshotContentID(manifest)
	if err != nil || id != bundle.Manifest.ID {
		t.Fatalf("provenance affected identity: %s, %v", id, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*SnapshotManifest)
	}{
		{"version", func(m *SnapshotManifest) { m.FormatVersion = 2 }},
		{"nil files", func(m *SnapshotManifest) { m.Files = nil }},
		{"nil directories", func(m *SnapshotManifest) { m.Directories = nil }},
		{"nil fonts", func(m *SnapshotManifest) { m.Fonts = nil }},
		{"nil font files", func(m *SnapshotManifest) { m.Fonts[1].Files = nil }},
		{"nil font directories", func(m *SnapshotManifest) { m.Fonts[1].Directories = nil }},
		{"id", func(m *SnapshotManifest) { m.ID = strings.Repeat("0", 64) }},
		{"unsorted", func(m *SnapshotManifest) { m.Files[0], m.Files[1] = m.Files[1], m.Files[0] }},
		{"duplicate", func(m *SnapshotManifest) { m.Files = append(m.Files, m.Files[len(m.Files)-1]) }},
		{"negative size", func(m *SnapshotManifest) { m.Files[0].Size = -1 }},
		{"hash", func(m *SnapshotManifest) { m.Files[0].SHA256 = "bad" }},
		{"uppercase hash", func(m *SnapshotManifest) { m.Files[0].SHA256 = strings.ToUpper(m.Files[0].SHA256) }},
		{"traversal", func(m *SnapshotManifest) { m.Files[0].Path = "../main.typ" }},
		{"native path", func(m *SnapshotManifest) { m.Files[0].Path = `C:\main.typ` }},
		{"root file", func(m *SnapshotManifest) { m.Files[0].Path = "." }},
		{"root directory", func(m *SnapshotManifest) { m.Directories = []string{"."} }},
		{"missing parent", func(m *SnapshotManifest) { m.Directories = []string{"empty"} }},
		{"file directory collision", func(m *SnapshotManifest) { m.Directories = append(m.Directories, "main.typ") }},
		{"font tree mismatch", func(m *SnapshotManifest) { m.Fonts[0].Files[0].Size++ }},
		{"font path", func(m *SnapshotManifest) { m.Fonts[0].Path = "missing" }},
		{"external path", func(m *SnapshotManifest) { m.Fonts[1].Path = "native" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := bundle.Manifest
			// Roundtrip through the existing deep copy accessor before mutating.
			snapshot := &Snapshot{manifest: manifest}
			manifest = snapshot.Manifest()
			test.mutate(&manifest)
			if err := ValidateSnapshotManifest(manifest); err == nil {
				t.Fatal("invalid manifest accepted")
			}
			if test.name != "id" {
				if _, err := SnapshotContentID(manifest); err == nil {
					t.Fatal("invalid structure hashed")
				}
			}
		})
	}
}

type trackedBundleFS struct {
	fs.FS
	opened, closed, ownerClosed int
	visits                      map[string]int
	onRead                      func(string, int)
	closeError                  error
}

func (source *trackedBundleFS) Open(name string) (fs.File, error) {
	file, err := source.FS.Open(name)
	if err != nil {
		return nil, err
	}
	source.opened++
	if source.visits == nil {
		source.visits = map[string]int{}
	}
	source.visits[name]++
	return &trackedBundleFile{File: file, source: source, name: name, visit: source.visits[name]}, nil
}

func (source *trackedBundleFS) Close() error { source.ownerClosed++; return nil }

type trackedBundleFile struct {
	fs.File
	source *trackedBundleFS
	name   string
	visit  int
	closed bool
}

func (file *trackedBundleFile) Read(buffer []byte) (int, error) {
	if file.source.onRead != nil {
		file.source.onRead(file.name, file.visit)
	}
	return file.File.Read(buffer)
}

func (file *trackedBundleFile) Close() error {
	if !file.closed {
		file.source.closed++
		file.closed = true
	}
	return errors.Join(file.File.Close(), file.source.closeError)
}

func (file *trackedBundleFile) ReadDir(n int) ([]fs.DirEntry, error) {
	return file.File.(fs.ReadDirFile).ReadDir(n)
}

func TestRestoreSnapshotClosesReadersAndBorrowsFilesystem(t *testing.T) {
	_, bundle, project, external := bundleFixture(t)
	projectFS := &trackedBundleFS{FS: project}
	fontFS := &trackedBundleFS{FS: external}
	bundle.ProjectFS = projectFS
	bundle.ExternalFontFS = map[int]fs.FS{1: fontFS, 3: fontFS}
	restored, err := RestoreSnapshot(context.Background(), bundle, RestoreSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
	for _, source := range []*trackedBundleFS{projectFS, fontFS} {
		if source.opened == 0 || source.opened != source.closed || source.ownerClosed != 0 {
			t.Fatalf("input lifecycle: %+v", source)
		}
	}
}

func TestRestoreSnapshotFailureDuringCopyClosesReadersAndRemovesStorage(t *testing.T) {
	for _, failure := range []string{"cancel", "mutate", "close error", "extra entry"} {
		t.Run(failure, func(t *testing.T) {
			_, bundle, project, _ := bundleFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("borrowed reader close failure")
			source := &trackedBundleFS{FS: project}
			source.onRead = func(name string, visit int) {
				if name != "main.typ" || visit != 2 {
					return
				}
				switch failure {
				case "cancel":
					cancel()
				case "mutate":
					project["main.typ"].Data[0] = 'X'
				case "close error":
					source.closeError = cause
				case "extra entry":
					project["extra"] = &fstest.MapFile{Mode: fs.ModeDir}
				}
			}
			bundle.ProjectFS = source
			temp := t.TempDir()
			restored, err := RestoreSnapshot(ctx, bundle, RestoreSnapshotOptions{TempDir: temp})
			if restored != nil || err == nil {
				t.Fatalf("copy failure accepted: %v, %v", restored, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			if failure == "close error" && !errors.Is(err, cause) {
				t.Fatalf("reader close cause lost: %v", err)
			}
			if source.opened != source.closed || source.ownerClosed != 0 {
				t.Fatalf("failed restore lifecycle: %+v", source)
			}
			entries, _ := os.ReadDir(temp)
			if len(entries) != 0 {
				t.Fatalf("failed copy leaked storage: %v", entries)
			}
		})
	}
}

func TestRestoreSnapshotCopiesNestedPortableTreesWithoutHardlinks(t *testing.T) {
	project := fstest.MapFS{"main.typ": {Data: []byte("nested")}}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: project}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	manifest := snapshot.Manifest()
	fontBytes := []byte("font")
	fontInput := fstest.MapFS{"nested/font.ttf": {Data: fontBytes}, "empty": {Mode: fs.ModeDir}}
	fontScan, err := scanSnapshotTree(context.Background(), fontInput, &snapshotBudget{limits: snapshot.limits})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Fonts = []SnapshotFont{{Path: ".", SourcePath: "/this/provenance/must/not/be/opened", Files: manifestFiles(fontScan), Directories: manifestDirs(fontScan)}}
	manifest.ID, err = SnapshotContentID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Native input bytes can be removed independently after restore.
	inputRoot := t.TempDir()
	writeTestFile(t, filepath.Join(inputRoot, "main.typ"), "nested")
	restored, err := RestoreSnapshot(context.Background(), SnapshotBundle{Manifest: manifest, ProjectFS: os.DirFS(inputRoot), ExternalFontFS: map[int]fs.FS{0: fontInput}}, RestoreSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	originalInfo, _ := os.Stat(filepath.Join(inputRoot, "main.typ"))
	restoredInfo, _ := os.Stat(filepath.Join(restored.root, "project", "main.typ"))
	if os.SameFile(originalInfo, restoredInfo) {
		t.Fatal("restore hardlinked borrowed input")
	}
	writeTestFile(t, filepath.Join(inputRoot, "main.typ"), "mutate")
	data, err := fs.ReadFile(restored.FS(), "main.typ")
	if err != nil || string(data) != "nested" {
		t.Fatalf("source write changed restored bytes: %q, %v", data, err)
	}
	fontFS, err := restored.FontFS(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(fontFS, "nested/font.ttf", "empty"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSnapshotLimitsCountInProjectFontsOnce(t *testing.T) {
	_, bundle, _, _ := bundleFixture(t)
	// Two project files (17+8 bytes), two ordered external trees (8 bytes each).
	limits := SnapshotLimits{MaxFiles: 4, MaxTotalBytes: 41, MaxFileBytes: 17}
	restored, err := RestoreSnapshot(context.Background(), bundle, RestoreSnapshotOptions{TempDir: t.TempDir(), Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
	for _, limits := range []SnapshotLimits{
		{MaxFiles: 3}, {MaxTotalBytes: 40}, {MaxFileBytes: 16}, {MaxFiles: -1},
	} {
		temp := t.TempDir()
		restored, err := RestoreSnapshot(context.Background(), bundle, RestoreSnapshotOptions{TempDir: temp, Limits: limits})
		if restored != nil || err == nil {
			t.Fatalf("limit accepted: %+v, %v", limits, err)
		}
		if limits.MaxFiles >= 0 && !errors.Is(err, ErrSnapshotLimit) {
			t.Fatalf("limit cause lost: %v", err)
		}
		entries, _ := os.ReadDir(temp)
		if len(entries) != 0 {
			t.Fatalf("limit leaked storage: %v", entries)
		}
	}
}

func TestRestoreSnapshotCanceledContextCleansStorage(t *testing.T) {
	_, bundle, _, _ := bundleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	temp := t.TempDir()
	if restored, err := RestoreSnapshot(ctx, bundle, RestoreSnapshotOptions{TempDir: temp}); restored != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore: %v, %v", restored, err)
	}
	entries, _ := os.ReadDir(temp)
	if len(entries) != 0 {
		t.Fatalf("cancellation leaked storage: %v", entries)
	}
}

func TestRestoreSnapshotRendersFrozenExternalFont(t *testing.T) {
	requireRealTypst(t)
	fontBytes, err := os.ReadFile("examples/invoice/fonts/FiraSans-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "Fira.ttf"), fontBytes, 0644); err != nil {
		t.Fatal(err)
	}
	project := fstest.MapFS{"main.typ": {Data: []byte("#set text(font: \"Fira Sans\")\nRestored font")}, "fonts": {Mode: fs.ModeDir | 0755}}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: project}}, SnapshotOptions{FontPaths: []string{external}})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := os.RemoveAll(external); err != nil {
		t.Fatal(err)
	}
	fontFS, err := snapshot.FontFS(1)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSnapshot(context.Background(), SnapshotBundle{Manifest: snapshot.Manifest(), ProjectFS: snapshot.FS(), ExternalFontFS: map[int]fs.FS{1: fontFS}}, RestoreSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	snapshot.Close()
	result, err := RenderSnapshot(context.Background(), restored, RenderSnapshotOptions{Output: filepath.Join(t.TempDir(), "restored.pdf")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.RawOutput, "unknown font family") {
		t.Fatalf("lost font bytes: %s", result.RawOutput)
	}
	requirePDF(t, result.PDFPath)
}
