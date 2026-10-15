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
	"sync"
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
	physicalTemp, err := canonicalDirectory(derivedTemp)
	if err != nil {
		t.Fatal(err)
	}
	if relative, err := filepath.Rel(physicalTemp, derived.root); err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
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

func TestCreatePreparedSnapshotVerifiesFilesAndKeepsCapturedFontsAfterSourceRemoval(t *testing.T) {
	requireRealTypst(t)
	fontBytes, err := os.ReadFile("examples/invoice/fonts/FiraSans-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	fontSource := t.TempDir()
	writeTestFile(t, filepath.Join(project, "main.typ"), "#set text(font: \"Fira Sans\")\n#let data = json(sys.inputs.at(\"leafrun-data\"))\n#assert(data.at(\"details\").at(\"count\") == 7)\n#assert(data.at(\"label\") == sys.inputs.at(\"watermark\"))\n#image(data.at(\"image\"), width: 20pt)\n#data.at(\"label\")")
	if err := os.WriteFile(filepath.Join(fontSource, "FiraSans-Regular.ttf"), fontBytes, 0644); err != nil {
		t.Fatal(err)
	}
	base, err := CreateSnapshot(context.Background(), FolderSource{Root: project}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{fontSource}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Error(err)
		}
	})
	baseManifest := base.Manifest()
	if err := os.RemoveAll(fontSource); err != nil {
		t.Fatal(err)
	}
	imageBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="red"/></svg>`)
	digest := sha256.Sum256(imageBytes)
	ref := FileRef{Pointer: "/image", ResourceID: "resource-image", Filename: "mark.svg", SizeBytes: int64(len(imageBytes)), SHA256: hex.EncodeToString(digest[:])}
	schemaFS := fstest.MapFS{"schemas/input.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["label","image","details"],"properties":{"label":{"type":"string"},"image":{"type":"string","x-leafrun-file":true},"details":{"type":"object","properties":{"count":{"type":"integer","default":7}}}}}`)}}
	type renderCase struct {
		label    string
		snapshot *Snapshot
		output   string
	}
	cases := make([]renderCase, 0, 2)
	for _, label := range []string{"invoice-a", "invoice-b"} {
		prepared, err := PrepareParameters(context.Background(), schemaFS, "schemas/input.json", []byte(`{"label":"`+label+`","details":{}}`), []FileRef{ref})
		if err != nil {
			t.Fatal(err)
		}
		prepared.Inputs["watermark"] = label
		if err := VerifyPreparedInput(context.Background(), schemaFS, "schemas/input.json", prepared); err != nil {
			t.Fatalf("VerifyPreparedInput() rejected prepared bytes: %v", err)
		}
		derived, err := CreatePreparedSnapshot(context.Background(), base, prepared, func(context.Context, PreparedFile) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(imageBytes)), nil
		}, CreateInputSnapshotOptions{TempDir: t.TempDir()})
		if err != nil {
			t.Fatalf("CreatePreparedSnapshot() error = %v", err)
		}
		t.Cleanup(func() {
			if err := derived.Close(); err != nil {
				t.Error(err)
			}
		})
		cases = append(cases, renderCase{label: label, snapshot: derived, output: filepath.Join(t.TempDir(), label+".pdf")})
	}
	if !reflect.DeepEqual(base.Manifest(), baseManifest) {
		t.Fatal("prepared snapshot changed its source manifest")
	}
	for index, testCase := range cases {
		if got, err := fs.ReadFile(testCase.snapshot.FS(), ".leafrun-input/"+ref.SHA256+".svg"); err != nil || !bytes.Equal(got, imageBytes) {
			t.Fatalf("derived file bytes = %q, %v", got, err)
		}
		if got, err := fs.ReadFile(testCase.snapshot.FS(), "input.json"); err != nil || !bytes.Contains(got, []byte(testCase.label)) || !bytes.Contains(got, []byte(`"count":7`)) {
			t.Fatalf("prepared input JSON %d = %q, %v", index, got, err)
		}
		if testCase.snapshot.Manifest().ID == baseManifest.ID {
			t.Fatal("derived snapshot content ID did not include prepared inputs")
		}
	}
	var wait sync.WaitGroup
	errs := make([]error, len(cases))
	results := make([]RenderResult, len(cases))
	for index, testCase := range cases {
		wait.Add(1)
		go func(index int, testCase renderCase) {
			defer wait.Done()
			results[index], errs[index] = RenderSnapshot(context.Background(), testCase.snapshot, RenderSnapshotOptions{
				Output: testCase.output, Inputs: map[string]string{"leafrun-data": "/input.json", "watermark": testCase.label}, PDFStandard: "1.7",
			})
		}(index, testCase)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Typst did not render parallel prepared input %d: %v", index, err)
		}
	}
	first, err := os.ReadFile(results[0].PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(results[1].PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || len(second) == 0 || bytes.Equal(first, second) {
		t.Fatal("parallel Typst renders did not retain independent prepared values")
	}
}

func TestCreatePreparedSnapshotOpensIdenticalGeneratedPathOnce(t *testing.T) {
	content := []byte("same verified bytes")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"first":{"type":"string","x-leafrun-file":true},"second":{"type":"string","x-leafrun-file":true}}}`
	prepared, err := PrepareParameters(context.Background(), parameterSchemaFS(schema), "schemas/input.json", []byte(`{}`), []FileRef{
		{Pointer: "/second", ResourceID: "resource-second", Filename: "same.svg", SizeBytes: int64(len(content)), SHA256: hash},
		{Pointer: "/first", ResourceID: "resource-first", Filename: "same.svg", SizeBytes: int64(len(content)), SHA256: hash},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstestMapFS("main.typ", []byte("base"))}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	openCount := 0
	derived, err := CreatePreparedSnapshot(context.Background(), base, prepared, func(_ context.Context, file PreparedFile) (io.ReadCloser, error) {
		openCount++
		if file.Pointer != "/first" {
			t.Errorf("representative file reference = %q, want sorted first pointer", file.Pointer)
		}
		return io.NopCloser(bytes.NewReader(content)), nil
	}, CreateInputSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("CreatePreparedSnapshot() error = %v", err)
	}
	defer derived.Close()
	if openCount != 1 {
		t.Fatalf("opener called %d times for one generated path", openCount)
	}
	if got, err := fs.ReadFile(derived.FS(), ".leafrun-input/"+hash+".svg"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("captured file bytes = %q, %v", got, err)
	}
}

func TestCreatePreparedSnapshotRejectsReservedRootAndChangedBytes(t *testing.T) {
	content := []byte("expected")
	digest := sha256.Sum256(content)
	ref := FileRef{Pointer: "/file", ResourceID: "resource-file", Filename: "file.bin", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	prepared, err := PrepareParameters(context.Background(), fstest.MapFS{"schema.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"file":{"type":"string","x-leafrun-file":true}}}`)}}, "schema.json", []byte(`{}`), []FileRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	base, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstestMapFS("main.typ", []byte("base"))}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if got, err := CreatePreparedSnapshot(context.Background(), base, prepared, func(context.Context, PreparedFile) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("tampered"))), nil
	}, CreateInputSnapshotOptions{TempDir: t.TempDir()}); got != nil || err == nil {
		if got != nil {
			_ = got.Close()
		}
		t.Fatal("accepted input bytes that differ from the frozen size and hash")
	}

	reserved, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstestMapFS(".leafrun-input/existing.svg", []byte("source"))}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer reserved.Close()
	if got, err := CreatePreparedSnapshot(context.Background(), reserved, prepared, func(context.Context, PreparedFile) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}, CreateInputSnapshotOptions{TempDir: t.TempDir()}); got != nil || err == nil {
		if got != nil {
			_ = got.Close()
		}
		t.Fatal("accepted a source snapshot that owns the reserved input root")
	}
}

func fstestMapFS(name string, data []byte) fs.FS {
	return fstest.MapFS{name: &fstest.MapFile{Data: data}}
}
