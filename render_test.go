package documentcore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testTempDir(t *testing.T) string {
	t.Helper()
	// macOS commonly exposes its temporary directory through /var ->
	// /private/var. Fixtures use the same canonical roots the API returns.
	directory, err := resolvePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func requireRealTypst(t *testing.T) {
	t.Helper()
	engine, err := exec.LookPath("typst")
	if err != nil {
		t.Fatalf("real Typst 0.15.1 integration tests require typst on PATH: %v", err)
	}
	output, err := exec.Command(engine, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(output), "typst 0.15.1 ") {
		t.Fatalf("expected real Typst 0.15.1, got %q (%v)", output, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requirePDF(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= len("%PDF-") || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("output at %s is not a nonempty PDF", path)
	}
	return data
}

func requireNoRenderTemps(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".leafrun-") {
			t.Errorf("render left temporary file %s", filepath.Join(directory, entry.Name()))
		}
	}
}

func requireErrorKind(t *testing.T, err error, kind ErrorKind) *RenderError {
	t.Helper()
	if err == nil {
		t.Fatal("expected render to fail")
	}
	var renderErr *RenderError
	if !errors.As(err, &renderErr) || renderErr.Kind != kind {
		t.Fatalf("expected %s RenderError, got %T: %v", kind, err, err)
	}
	return renderErr
}

func TestRenderDefaultProjectWithNestedResources(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#include \"chapters/body.typ\"\n")
	writeTestFile(t, filepath.Join(root, "chapters/body.typ"), "#let values = json(\"../resources/data.json\")\n= #values.title\n#image(\"/resources/picture.svg\", width: 40pt)\n")
	writeTestFile(t, filepath.Join(root, "resources/data.json"), `{"title":"Nested resources"}`)
	writeTestFile(t, filepath.Join(root, "resources/picture.svg"), `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="blue"/></svg>`)
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "output/result.pdf")
	if result.PDFPath != want || result.EngineVersion != "0.15.1" {
		t.Fatalf("result = %#v, want PDF %s from 0.15.1", result, want)
	}
	requirePDF(t, want)
	requireNoRenderTemps(t, filepath.Dir(want))
}

func TestRenderSpaceUnicodePathsAndCustomOutputReplacesOldPDF(t *testing.T) {
	requireRealTypst(t)
	root := filepath.Join(testTempDir(t), "project résumé 文档")
	entry := filepath.Join("子 folder", "start file.typ")
	output := filepath.Join("PDF output", "final résumé.pdf")
	writeTestFile(t, filepath.Join(root, entry), "= Unicode path\nThe custom entrypoint rendered.\n")
	old := "%PDF-old output must be replaced on success\n"
	writeTestFile(t, filepath.Join(root, output), old)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRoot, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(context.Background(), RenderOptions{ProjectDir: relativeRoot, Entrypoint: entry, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if result.PDFPath != filepath.Join(root, output) {
		t.Fatalf("PDFPath = %q", result.PDFPath)
	}
	if data := requirePDF(t, result.PDFPath); bytes.Equal(data, []byte(old)) {
		t.Fatal("successful render retained old output")
	}
	requireNoRenderTemps(t, filepath.Dir(result.PDFPath))
}

func TestRenderAbsoluteOutput(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	output := filepath.Join(testTempDir(t), "outside project.pdf")
	writeTestFile(t, filepath.Join(root, "main.typ"), "Absolute output\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Output: output})
	if err != nil || result.PDFPath != output {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	requirePDF(t, output)
}

func TestRenderCompileErrorsPreserveOldPDFAndExitCause(t *testing.T) {
	requireRealTypst(t)
	for name, source := range map[string]string{
		"syntax":        "#let =\n",
		"unknown":       "#leafrun_missing\n",
		"missing_image": "#image(\"resources/missing.svg\")\n",
		"assertion":     "#assert(false, message: \"leafrun assertion\")\n",
		"panic":         "#panic(\"leafrun panic\")\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := testTempDir(t)
			output := filepath.Join(root, "output/result.pdf")
			old := "%PDF-existing successful result\n"
			writeTestFile(t, filepath.Join(root, "main.typ"), source)
			writeTestFile(t, output, old)
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
			renderErr := requireErrorKind(t, err, ErrorCompile)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("missing original process exit cause: %v", err)
			}
			if result.PDFPath != "" || result.EngineVersion != "0.15.1" || result.RawOutput == "" || len(result.Diagnostics) == 0 || renderErr.RawOutput != result.RawOutput || !strings.Contains(err.Error(), "error:") {
				t.Fatalf("failure lost diagnostic details: %#v, %v", result, err)
			}
			data, readErr := os.ReadFile(output)
			if readErr != nil || string(data) != old {
				t.Fatalf("old output changed: %v", readErr)
			}
			requireNoRenderTemps(t, filepath.Dir(output))
		})
	}
}

func TestRenderMissingFontWarningStillPublishesPDF(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#set text(font: \"Leafrun Missing Font 6139\")\nWarning still renders.\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.RawOutput, "warning: unknown font family") || len(result.Diagnostics) == 0 || result.Diagnostics[0].Severity != "warning" || result.Diagnostics[0].Location == nil {
		t.Fatalf("missing font warning not preserved: %#v", result)
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderRealNestedTrace(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#let g() = leafrun_missing\n#let f() = g()\n#f()\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	requireErrorKind(t, err, ErrorCompile)
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Location == nil || len(result.Diagnostics[0].Trace) != 2 {
		t.Fatalf("nested diagnostic trace missing: %#v", result.Diagnostics)
	}
	trace := result.Diagnostics[0].Trace
	if trace[0].Message != "while calling `g`" || trace[0].Location.Line != 2 || trace[1].Message != "while calling `f`" || trace[1].Location.Line != 3 {
		t.Fatalf("unexpected nested trace: %#v", trace)
	}
}

func TestRenderCannotOverwriteEntrypointOrResource(t *testing.T) {
	requireRealTypst(t)
	for _, target := range []string{"main.typ", "resources/data.json", "resources/picture.svg"} {
		t.Run(target, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "#let values = json(\"resources/data.json\")\n#values.title\n#image(\"resources/picture.svg\")\n")
			writeTestFile(t, filepath.Join(root, "resources/data.json"), `{"title":"Resource protection"}`)
			writeTestFile(t, filepath.Join(root, "resources/picture.svg"), `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect width="1" height="1"/></svg>`)
			path := filepath.Join(root, target)
			old, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Output: target})
			requireErrorKind(t, err, ErrorValidation)
			if result.PDFPath != "" {
				t.Fatalf("failed render has published PDF path %q", result.PDFPath)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(data, old) {
				t.Fatal("render overwrote a source/resource")
			}
			requireNoRenderTemps(t, filepath.Dir(path))
		})
	}
}

func TestRenderRejectsInvalidProjectEntrypointAndFonts(t *testing.T) {
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Validation\n")
	writeTestFile(t, filepath.Join(root, "not-a-directory"), "file")
	outOfRoot := filepath.Join(testTempDir(t), "outside.typ")
	writeTestFile(t, outOfRoot, "Outside\n")
	for name, options := range map[string]RenderOptions{
		"empty_project":       {},
		"missing_project":     {ProjectDir: filepath.Join(root, "missing")},
		"file_project":        {ProjectDir: filepath.Join(root, "main.typ")},
		"missing_entry":       {ProjectDir: root, Entrypoint: "missing.typ"},
		"directory_entry":     {ProjectDir: root, Entrypoint: "."},
		"relative_traversal":  {ProjectDir: root, Entrypoint: "../outside.typ"},
		"absolute_traversal":  {ProjectDir: root, Entrypoint: outOfRoot},
		"missing_fonts":       {ProjectDir: root, FontPaths: []string{"missing-fonts"}},
		"file_fonts":          {ProjectDir: root, FontPaths: []string{"not-a-directory"}},
		"empty_explicit_font": {ProjectDir: root, FontPaths: []string{""}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Render(context.Background(), options)
			requireErrorKind(t, err, ErrorValidation)
			if result.PDFPath != "" {
				t.Fatalf("validation failure has PDFPath %q", result.PDFPath)
			}
		})
	}
}

func TestRenderOutputDirectoryFailurePreservesFiles(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Storage test\n")
	writeTestFile(t, filepath.Join(root, "blocked"), "must remain unchanged")
	for _, output := range []string{"blocked/result.pdf", "."} {
		result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Output: output})
		requireErrorKind(t, err, ErrorStorage)
		if result.PDFPath != "" {
			t.Fatalf("storage failure has PDFPath %q", result.PDFPath)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "blocked"))
	if err != nil || string(data) != "must remain unchanged" {
		t.Fatal("failed storage changed existing file")
	}
}

func TestRenderParallelDifferentOutputs(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Parallel rendering\n")
	var wait sync.WaitGroup
	for _, output := range []string{"output/first.pdf", "output/second.pdf", "output/third.pdf"} {
		wait.Add(1)
		go func(output string) {
			defer wait.Done()
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Output: output})
			if err != nil {
				t.Errorf("render %s: %v", output, err)
				return
			}
			requirePDF(t, result.PDFPath)
		}(output)
	}
	wait.Wait()
	requireNoRenderTemps(t, filepath.Join(root, "output"))
}

func TestRenderOversizedLayoutIsAllowed(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#set page(width: 2000pt, height: 3000pt)\nLarge page.\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	if err != nil {
		t.Fatal(err)
	}
	requirePDF(t, result.PDFPath)
}
