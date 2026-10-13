package documentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestRenderSnapshotUsesAcceptedBytesAndBorrowsSnapshot(t *testing.T) {
	requireRealTypst(t)
	project := testTempDir(t)
	writeTestFile(t, filepath.Join(project, "nested/main.typ"), "#include \"body.typ\"")
	writeTestFile(t, filepath.Join(project, "nested/body.typ"), "#let data = json(\"/data.json\")\n= #data.title")
	writeTestFile(t, filepath.Join(project, "data.json"), `{"title":"Accepted invoice"}`)
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: project}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := os.RemoveAll(project); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(testTempDir(t), "счёт with spaces.pdf")
	for i := 0; i < 2; i++ {
		result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Entrypoint: "nested/main.typ", Output: output})
		if err != nil {
			t.Fatal(err)
		}
		if result.PDFPath != output {
			t.Fatalf("result: %+v", result)
		}
		requirePDF(t, output)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: output})
	if !errors.Is(err, ErrSnapshotClosed) || result.PDFPath != "" {
		t.Fatalf("closed: %+v, %v", result, err)
	}
}

func TestRenderSnapshotNormalizesStructuredDiagnosticsOnly(t *testing.T) {
	requireRealTypst(t)
	source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{
		"nested/main.typ": &fstest.MapFile{Data: []byte("#include \"body.typ\"")},
		"nested/body.typ": &fstest.MapFile{Data: []byte("#let fail() = panic(\"accepted failure\")\n#fail()")},
	}}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Entrypoint: "nested/main.typ", Output: output})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || result.PDFPath != "" {
		t.Fatalf("compile cause: %+v %v", result, err)
	}
	if len(result.Diagnostics) == 0 || result.Diagnostics[0].Location == nil || result.Diagnostics[0].Location.Path != "nested/body.typ" {
		t.Fatalf("diagnostics: %+v", result.Diagnostics)
	}
	if !strings.Contains(result.RawOutput, "accepted failure") || !strings.Contains(result.RawOutput, result.Diagnostics[0].Raw) {
		t.Fatal("raw diagnostics changed")
	}
	for _, frame := range result.Diagnostics[0].Trace {
		if frame.Location != nil && strings.Contains(frame.Location.Path, "leafrun-work-") {
			t.Fatalf("temporary trace path: %+v", frame)
		}
	}
	data, _ := os.ReadFile(output)
	if string(data) != "old PDF" {
		t.Fatal("failure replaced output")
	}
}

func TestRenderSnapshotRequiredCleanupPrecedesPublication(t *testing.T) {
	requireRealTypst(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Hello")}}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(working.storage)
	cause := errors.New("required cleanup denied")
	working.cleanup = func(string) error { return cause }
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	result, err := renderSnapshotWorking(context.Background(), working, snapshot.root, RenderSnapshotOptions{Output: output}, nil)
	if !errors.Is(err, cause) || result.PDFPath != "" {
		t.Fatalf("cleanup failure: %+v %v", result, err)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "old PDF" {
		t.Fatal("published before cleanup")
	}
	requireNoRenderTemps(t, filepath.Dir(output))
}

func TestRenderSourceReleaseBeforePublicationAndErrorJoin(t *testing.T) {
	requireRealTypst(t)
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	cause := errors.New("source release failed")
	source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Hello")}}}, releaseErr: cause}
	result, err := RenderSource(context.Background(), source, RenderSourceOptions{Output: output})
	if !errors.Is(err, cause) || result.PDFPath != "" {
		t.Fatalf("release: %+v %v", result, err)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "old PDF" {
		t.Fatal("release failure published")
	}
}
func TestRenderSnapshotConcurrentAcceptedCopiesSurviveClose(t *testing.T) {
	requireRealTypst(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Concurrent accepted inputs")}}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for i, working := range []*snapshotWorkingCopy{first, second} {
		output := filepath.Join(testTempDir(t), fmt.Sprintf("copy-%d.pdf", i))
		go func() {
			result, err := renderSnapshotWorking(context.Background(), working, snapshot.root, RenderSnapshotOptions{Output: output}, nil)
			if err == nil {
				_, err = os.Stat(result.PDFPath)
			}
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderSnapshotCleanupJoinsCompilerCause(t *testing.T) {
	requireRealTypst(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("#panic(\"error before cleanup\")")}}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(working.storage)
	cause := errors.New("working cleanup failed")
	working.cleanup = func(string) error { return cause }
	result, err := renderSnapshotWorking(context.Background(), working, snapshot.root, RenderSnapshotOptions{Output: filepath.Join(testTempDir(t), "result.pdf")}, nil)
	var exit *exec.ExitError
	if !errors.Is(err, cause) || !errors.As(err, &exit) || result.PDFPath != "" {
		t.Fatalf("joined causes lost: %+v %v", result, err)
	}
}

func TestRenderSnapshotReleasesOwnedDataWhileOldPDFStillExists(t *testing.T) {
	requireRealTypst(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Publication after release")}}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	checked := false
	result, err := renderSnapshotWorking(context.Background(), working, snapshot.root, RenderSnapshotOptions{Output: output}, func() error {
		data, err := os.ReadFile(output)
		if err != nil {
			return err
		}
		if string(data) != "old PDF" {
			return errors.New("published before release")
		}
		if _, err := os.Stat(working.storage); !errors.Is(err, os.ErrNotExist) {
			return errors.New("working copy still exists during snapshot release")
		}
		checked = true
		return snapshot.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("owned release not called")
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderSnapshotRequiresPublicOutputAndProtectsOriginalHardlinks(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#include \"body.typ\"")
	writeTestFile(t, filepath.Join(root, "body.typ"), "Accepted resource")
	alias := filepath.Join(testTempDir(t), "hardlink.pdf")
	if err := os.Link(filepath.Join(root, "body.typ"), alias); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := os.Remove(filepath.Join(root, "body.typ")); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"", filepath.Join(snapshot.root, "private.pdf"), alias, filepath.Join(root, "main.typ"), filepath.Join(root, "body.typ")} {
		result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: output})
		if err == nil || result.PDFPath != "" {
			t.Fatalf("protected output %q accepted: %+v %v", output, result, err)
		}
	}
	data, _ := os.ReadFile(alias)
	if string(data) != "Accepted resource" {
		t.Fatal("hardlink resource overwritten")
	}
}

func TestRenderSnapshotFrozenExternalFontAndSearchOrder(t *testing.T) {
	requireRealTypst(t)
	fontBytes, err := os.ReadFile("examples/invoice/fonts/FiraSans-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	project := testTempDir(t)
	external := testTempDir(t)
	writeTestFile(t, filepath.Join(project, "main.typ"), "#set text(font: \"Fira Sans\")\nFrozen Fira")
	if err := os.MkdirAll(filepath.Join(project, "fonts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "alternate"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "Fira.ttf"), fontBytes, 0644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: project}, SnapshotOptions{FontPaths: []string{"alternate", external}})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	manifest := snapshot.Manifest()
	if len(manifest.Fonts) != 3 || manifest.Fonts[0].Path != "fonts" || manifest.Fonts[1].Path != "alternate" || manifest.Fonts[2].SourcePath != external {
		t.Fatalf("font order: %+v", manifest.Fonts)
	}
	if err := os.RemoveAll(external); err != nil {
		t.Fatal(err)
	}
	result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: filepath.Join(testTempDir(t), "fonts.pdf")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.RawOutput, "unknown font family") {
		t.Fatalf("reopened live font: %s", result.RawOutput)
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderSnapshotCompilerCancellationCleansBeforeReturn(t *testing.T) {
	requireRealTypst(t)
	temp := testTempDir(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("#for i in range(100000000) [Accepted invoice #i #pagebreak()]")}}}}, SnapshotOptions{TempDir: temp})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := RenderSnapshot(ctx, snapshot, RenderSnapshotOptions{Output: output})
	if !errors.Is(err, context.DeadlineExceeded) || result.PDFPath != "" {
		t.Fatalf("cancellation lost: %+v %v", result, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("cancellation did not stop compiler promptly")
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("owned working inputs leaked: %+v %v", entries, err)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "old PDF" {
		t.Fatal("cancellation replaced PDF")
	}
	requireNoRenderTemps(t, filepath.Dir(output))
}
func TestSnapshotDiagnosticLocationsUseActualCompilerCWD(t *testing.T) {
	root := testTempDir(t)
	cwd := testTempDir(t)
	absolute := filepath.Join(root, "nested/счёт.typ")
	relative, err := filepath.Rel(cwd, absolute)
	if err != nil {
		t.Fatal(err)
	}
	raw := "warning: test\nunknown future diagnostic text\n"
	diagnostics := []Diagnostic{{Raw: raw, Location: &Location{Path: relative, Line: 7, Column: 3}, Hints: []string{"unchanged hint"}, Trace: []Trace{{Message: "first", Location: &Location{Path: absolute, Line: 9, Column: 8}}, {Message: "position absent"}}}, {Raw: "unlocated", Location: nil}}
	normalizeSnapshotDiagnostics(diagnostics, root, cwd)
	if diagnostics[0].Location.Path != "nested/счёт.typ" || diagnostics[0].Location.Line != 7 || diagnostics[0].Location.Column != 3 || diagnostics[0].Trace[0].Location.Path != "nested/счёт.typ" || diagnostics[0].Trace[1].Location != nil || diagnostics[1].Location != nil || diagnostics[0].Raw != raw || diagnostics[0].Hints[0] != "unchanged hint" {
		t.Fatalf("structured normalization changed diagnostics: %+v", diagnostics)
	}
}

func TestRenderSnapshotRealWarningsMissingResourceAndAssert(t *testing.T) {
	requireRealTypst(t)
	for _, test := range []struct {
		name, source, message string
		success               bool
	}{
		{"warning", "#set text(font: \"Leafrun Missing Font 94731\")\nAccepted warning", "unknown font family", true},
		{"missing resource", "#image(\"missing.svg\")", "file not found", false},
		{"assert", "#let fail() = assert(false, message: \"accepted assert\")\n#fail()", "accepted assert", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"nested/main.typ": &fstest.MapFile{Data: []byte(test.source)}}}}, SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			output := filepath.Join(testTempDir(t), "old.pdf")
			writeTestFile(t, output, "old PDF")
			result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Entrypoint: "nested/main.typ", Output: output})
			if (err == nil) != test.success || !strings.Contains(result.RawOutput, test.message) || len(result.Diagnostics) == 0 || result.Diagnostics[0].Location == nil || result.Diagnostics[0].Location.Path != "nested/main.typ" {
				t.Fatalf("real diagnostic: %+v %v", result, err)
			}
			if test.success {
				requirePDF(t, result.PDFPath)
			} else {
				data, _ := os.ReadFile(output)
				if result.PDFPath != "" || string(data) != "old PDF" {
					t.Fatal("error published PDF")
				}
			}
		})
	}
}

func TestRenderSourceCancelsAndReleasesCapturedStorage(t *testing.T) {
	requireRealTypst(t)
	temp := testTempDir(t)
	project := testTempDir(t)
	writeTestFile(t, filepath.Join(project, "main.typ"), "#for i in range(100000000) [Long invoice #i #pagebreak()]")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := RenderSource(ctx, FolderSource{Root: project}, RenderSourceOptions{Output: filepath.Join(testTempDir(t), "result.pdf"), TempDir: temp})
	if !errors.Is(err, context.DeadlineExceeded) || result.PDFPath != "" {
		t.Fatalf("source cancellation: %+v %v", result, err)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("owned capture/work storage leaked: %+v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(project, "main.typ")); err != nil {
		t.Fatal("caller project removed")
	}
}

func TestRenderSourceOrdinaryChosenResultCanBeReplaced(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Ordinary generated output")
	output := filepath.Join(root, "exports/generated.pdf")
	writeTestFile(t, output, "previous ordinary PDF")
	result, err := RenderSource(context.Background(), FolderSource{Root: root}, RenderSourceOptions{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	requirePDF(t, result.PDFPath)
}
func TestOwnedSnapshotCleanupFailureKeepsPriorPDF(t *testing.T) {
	requireRealTypst(t)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Required owned snapshot cleanup")}}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(snapshot.root)
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("owned snapshot cleanup denied")
	calls := 0
	snapshot.cleanup = func(string) error { calls++; return cause }
	output := filepath.Join(testTempDir(t), "old.pdf")
	writeTestFile(t, output, "old PDF")
	result, err := renderSnapshotWorking(context.Background(), working, snapshot.root, RenderSnapshotOptions{Output: output}, snapshot.Close)
	if !errors.Is(err, cause) || result.PDFPath != "" || calls != 1 {
		t.Fatalf("owned snapshot cleanup failed: %+v %v calls%d", result, err, calls)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "old PDF" {
		t.Fatal("published after mandatory cleanup failure")
	}
	requireNoRenderTemps(t, filepath.Dir(output))
	if _, err := os.Stat(working.storage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("working input cleanup skipped after owned failure")
	}
}
