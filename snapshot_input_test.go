package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestCreateInputSnapshotRejectsStorageInsideCapturedSources(t *testing.T) {
	project := t.TempDir()
	external := t.TempDir()
	writeTestFile(t, filepath.Join(project, "main.typ"), "base")
	writeTestFile(t, filepath.Join(external, "font.ttf"), "font")
	base, err := CreateSnapshot(context.Background(), FolderSource{Root: project}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{external}})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, dir := range []string{project, external, base.root} {
		before, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		derived, err := CreateInputSnapshot(context.Background(), base, []byte(`{}`), CreateInputSnapshotOptions{TempDir: dir})
		if derived != nil {
			derived.Close()
		}
		if err == nil {
			t.Errorf("accepted snapshot storage inside %s", dir)
		}
		after, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(before) != len(after) {
			t.Error("modified source")
		}
	}
}

func TestCreateInputSnapshotAddsExactInputAndOwnsCapturedTrees(t *testing.T) {
	projectRoot := t.TempDir()
	writeTestFile(t, filepath.Join(projectRoot, "main.typ"), "base project")
	writeTestFile(t, filepath.Join(projectRoot, "fonts", "internal.ttf"), "internal font")
	externalFonts := t.TempDir()
	writeTestFile(t, filepath.Join(externalFonts, "external.ttf"), "captured external font")

	base, err := CreateSnapshot(context.Background(), FolderSource{Root: projectRoot}, SnapshotOptions{
		TempDir:   t.TempDir(),
		FontPaths: []string{".", externalFonts},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Error(err)
		}
	})
	baseManifest := base.Manifest()
	baseInputs := append([]snapshotInput(nil), base.inputs...)
	if len(baseManifest.Fonts) != 3 || baseManifest.Fonts[0].Path != "fonts" || baseManifest.Fonts[1].Path != "." || baseManifest.Fonts[2].SourcePath == "" {
		t.Fatalf("base font order/provenance = %+v", baseManifest.Fonts)
	}
	if err := os.RemoveAll(externalFonts); err != nil {
		t.Fatal(err)
	}

	input := []byte(`{"amount":1.2300e+04,"escaped":"\u0061","negativeZero":-0}`)
	wantInput := bytes.Clone(input)
	derivedTemp := t.TempDir()
	derived, err := CreateInputSnapshot(context.Background(), base, input, CreateInputSnapshotOptions{TempDir: derivedTemp})
	if err != nil {
		t.Fatal(err)
	}
	defer derived.Close()

	if got := derived.Manifest(); !reflect.DeepEqual(got.Origin, baseManifest.Origin) {
		t.Fatalf("origin changed: got %+v, want %+v", got.Origin, baseManifest.Origin)
	}
	if !reflect.DeepEqual(derived.inputs, baseInputs) {
		t.Fatalf("source identity guards changed: got %+v, want %+v", derived.inputs, baseInputs)
	}
	if got := derived.Manifest(); len(got.Fonts) != 3 || got.Fonts[0].Path != "fonts" || got.Fonts[1].Path != "." || got.Fonts[2].SourcePath != baseManifest.Fonts[2].SourcePath {
		t.Fatalf("font order/provenance changed: %+v", got.Fonts)
	}
	manifest := derived.Manifest()
	inputHash := sha256.Sum256(wantInput)
	var inputEntry *SnapshotFile
	for i := range manifest.Files {
		if manifest.Files[i].Path == "input.json" {
			inputEntry = &manifest.Files[i]
		}
	}
	if inputEntry == nil || inputEntry.Size != int64(len(wantInput)) || inputEntry.SHA256 != hex.EncodeToString(inputHash[:]) {
		t.Fatalf("input manifest entry = %+v", inputEntry)
	}
	if got, err := fs.ReadFile(derived.FS(), "input.json"); err != nil || !bytes.Equal(got, wantInput) {
		t.Fatalf("captured input = %q, %v", got, err)
	}
	input[0] = 'X'
	if got, err := fs.ReadFile(derived.FS(), "input.json"); err != nil || !bytes.Equal(got, wantInput) {
		t.Fatalf("input aliases caller bytes: %q, %v", got, err)
	}
	rootFont, err := derived.FontFS(1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(rootFont, "input.json"); err != nil || !bytes.Equal(got, wantInput) {
		t.Fatalf("root font tree input = %q, %v", got, err)
	}
	internalFont, err := derived.FontFS(0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(internalFont, "internal.ttf"); err != nil || string(got) != "internal font" {
		t.Fatalf("in-project font = %q, %v", got, err)
	}
	externalFont, err := derived.FontFS(2)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(externalFont, "external.ttf"); err != nil || string(got) != "captured external font" {
		t.Fatalf("captured external font = %q, %v", got, err)
	}
	for index, font := range manifest.Fonts {
		if !font.InProject {
			continue
		}
		wantTree := snapshotSubtree(manifestSnapshotScan(manifest.Files, manifest.Directories), font.Path)
		if !reflect.DeepEqual(font.Files, manifestFiles(wantTree)) || !reflect.DeepEqual(font.Directories, manifestDirs(wantTree)) {
			t.Fatalf("in-project font %d was not recomputed from project tree: %+v", index, font)
		}
	}
	if got, err := SnapshotContentID(manifest); err != nil || got != manifest.ID {
		t.Fatalf("content ID = %q, %v; manifest ID %q", got, err, manifest.ID)
	}

	second, err := CreateInputSnapshot(context.Background(), base, wantInput, CreateInputSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest().ID != manifest.ID {
		t.Fatalf("content ID depends on storage path: %s != %s", second.Manifest().ID, manifest.ID)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(base.FS(), "main.typ"); err != nil || string(got) != "base project" {
		t.Fatalf("closing derived snapshot closed its base: %q, %v", got, err)
	}
	if got := base.Manifest(); !reflect.DeepEqual(got, baseManifest) {
		t.Fatal("base manifest changed")
	}
	if got, err := fs.ReadFile(base.FS(), "main.typ"); err != nil || string(got) != "base project" {
		t.Fatalf("base project changed: %q, %v", got, err)
	}
	if _, err := fs.ReadFile(base.FS(), "input.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("base gained input.json: %v", err)
	}
	if relative, err := filepath.Rel(derivedTemp, derived.root); err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("derived storage escaped TempDir: %q, %v", relative, err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(derived.FS(), "input.json"); err != nil || !bytes.Equal(got, wantInput) {
		t.Fatalf("derived snapshot depends on base lifetime: %q, %v", got, err)
	}
	if err := derived.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(derived.FS(), "input.json"); !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("closed derived snapshot remained readable: %v", err)
	}
}

func TestCreateInputSnapshotReplacesRootFileAndRejectsDirectoryCollision(t *testing.T) {
	base, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstestMapFS("input.json", []byte("old"))}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	want := []byte(`{"replacement":true}`)
	derived, err := CreateInputSnapshot(context.Background(), base, want, CreateInputSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer derived.Close()
	if got, err := fs.ReadFile(derived.FS(), "input.json"); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("replaced input = %q, %v", got, err)
	}
	if len(derived.Manifest().Files) != 1 {
		t.Fatalf("replacement created duplicate project files: %+v", derived.Manifest().Files)
	}

	directoryBase, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstestMapFS("input.json/child", []byte("child"))}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer directoryBase.Close()
	if snapshot, err := CreateInputSnapshot(context.Background(), directoryBase, want, CreateInputSnapshotOptions{TempDir: t.TempDir()}); err == nil {
		_ = snapshot.Close()
		t.Fatal("accepted input.json directory collision")
	}
	if got, err := fs.ReadFile(directoryBase.FS(), "input.json/child"); err != nil || string(got) != "child" {
		t.Fatalf("directory collision changed base: %q, %v", got, err)
	}
}

func TestCreateInputSnapshotLimitsCancellationAndClosedBase(t *testing.T) {
	projectRoot := t.TempDir()
	writeTestFile(t, filepath.Join(projectRoot, "main.typ"), "base")
	writeTestFile(t, filepath.Join(projectRoot, "fonts", "internal.ttf"), "font")
	externalFonts := t.TempDir()
	writeTestFile(t, filepath.Join(externalFonts, "external.ttf"), "ext")
	base, err := CreateSnapshot(context.Background(), FolderSource{Root: projectRoot}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{externalFonts}})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := os.RemoveAll(externalFonts); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"x":123}`)
	limits := SnapshotLimits{MaxFiles: 4, MaxTotalBytes: 20, MaxFileBytes: 9}
	derived, err := CreateInputSnapshot(context.Background(), base, input, CreateInputSnapshotOptions{TempDir: t.TempDir(), Limits: limits})
	if err != nil {
		t.Fatalf("exact combined project/external limits rejected: %v", err)
	}
	if err := derived.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := CreateInputSnapshot(context.Background(), base, input, CreateInputSnapshotOptions{TempDir: t.TempDir(), Limits: SnapshotLimits{MaxTotalBytes: 19}}); snapshot != nil || !errors.Is(err, ErrSnapshotLimit) {
		t.Fatalf("external font bytes were not included in limits: snapshot=%v err=%v", snapshot, err)
	}
	if snapshot, err := CreateInputSnapshot(context.Background(), base, input, CreateInputSnapshotOptions{TempDir: t.TempDir(), Limits: SnapshotLimits{MaxFileBytes: 8}}); snapshot != nil || !errors.Is(err, ErrSnapshotLimit) {
		t.Fatalf("input file limit was not enforced: snapshot=%v err=%v", snapshot, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshot, err := CreateInputSnapshot(ctx, base, input, CreateInputSnapshotOptions{TempDir: t.TempDir()}); snapshot != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input capture: snapshot=%v err=%v", snapshot, err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := CreateInputSnapshot(context.Background(), base, input, CreateInputSnapshotOptions{TempDir: t.TempDir()}); snapshot != nil || !errors.Is(err, ErrSnapshotClosed) {
		t.Fatalf("closed base accepted: snapshot=%v err=%v", snapshot, err)
	}
}

func fstestMapFS(name string, data []byte) fs.FS {
	return fstest.MapFS{name: &fstest.MapFile{Data: data}}
}
