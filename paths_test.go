package documentcore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func makeTestDirectoryLink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// Junction creation does not require Windows symlink privileges.
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestRenderRejectsEscapingProjectAndFontLinks(t *testing.T) {
	for _, fonts := range []bool{false, true} {
		name := "project"
		if fonts {
			name = "fonts"
		}
		t.Run(name, func(t *testing.T) {
			root := testTempDir(t)
			outside := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "Project links\n")
			writeTestFile(t, filepath.Join(outside, "private.typ"), "Private resource\n")
			options := RenderOptions{ProjectDir: root}
			declaredRoot := root
			if fonts {
				declaredRoot = testTempDir(t)
				options.FontPaths = []string{declaredRoot}
			}
			makeTestDirectoryLink(t, outside, filepath.Join(declaredRoot, "escaping link"))
			result, err := Render(context.Background(), options)
			requireErrorKind(t, err, ErrorValidation)
			if result.PDFPath != "" {
				t.Fatalf("escaping link has PDFPath %q", result.PDFPath)
			}
		})
	}
}

func TestRenderRejectsEscapingFileSymlink(t *testing.T) {
	root := testTempDir(t)
	outside := filepath.Join(testTempDir(t), "private.typ")
	writeTestFile(t, outside, "Private document\n")
	writeTestFile(t, filepath.Join(root, "main.typ"), "Symlink protection\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked-private.typ")); err != nil {
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("Windows file symlink privilege unavailable; junction tests still run: %v", err)
		}
		t.Fatal(err)
	}
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	requireErrorKind(t, err, ErrorValidation)
	if result.PDFPath != "" {
		t.Fatalf("escaping file symlink published %q", result.PDFPath)
	}
}

func TestRenderAllowsContainedDirectoryLinksAndCanonicalProjectRoot(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "#include \"linked/chapter.typ\"\n")
	writeTestFile(t, filepath.Join(root, "chapters/chapter.typ"), "Contained link\n")
	makeTestDirectoryLink(t, filepath.Join(root, "chapters"), filepath.Join(root, "linked"))
	rootLink := filepath.Join(testTempDir(t), "project link")
	makeTestDirectoryLink(t, root, rootLink)
	result, err := Render(context.Background(), RenderOptions{ProjectDir: rootLink})
	if err != nil {
		t.Fatal(err)
	}
	if result.PDFPath != filepath.Join(root, "output/result.pdf") {
		t.Fatalf("project root not canonicalized: %q", result.PDFPath)
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderMatchesTypstCanonicalEntrypointImportSemantics(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "library", "deep", "chapter.typ"), "#include \"../sibling.typ\"\n")
	writeTestFile(t, filepath.Join(root, "library", "sibling.typ"), "Canonical sibling\n")
	writeTestFile(t, filepath.Join(root, "selected", "sibling.typ"), "#panic(\"incorrect logical sibling selected\")\n")
	makeTestDirectoryLink(t, filepath.Join(root, "library", "deep"), filepath.Join(root, "selected", "book"))
	entry := filepath.Join(root, "selected", "book", "chapter.typ")
	// Typst 0.15.1 canonicalizes the entrypoint before resolving relative
	// imports. Demonstrate that CLI behavior before asserting the core matches.
	direct := filepath.Join(testTempDir(t), "direct.pdf")
	if output, err := exec.Command("typst", "--color", "never", "compile", "--format", "pdf", "--root", root, entry, direct).CombinedOutput(); err != nil {
		t.Fatalf("Typst canonical-entry probe failed: %v: %s", err, output)
	}
	requirePDF(t, direct)
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Entrypoint: entry})
	if err != nil {
		t.Fatalf("library changed Typst entrypoint import semantics: %v", err)
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderProtectsHardlinkedEntrypointAndResource(t *testing.T) {
	requireRealTypst(t)
	for _, source := range []string{"main.typ", "resources/data.json"} {
		t.Run(source, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "#let values = json(\"resources/data.json\")\n#values.title\n")
			writeTestFile(t, filepath.Join(root, "resources/data.json"), `{"title":"Hardlink protection"}`)
			output := filepath.Join(root, "result.pdf")
			if err := os.Link(filepath.Join(root, source), output); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, source))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Render(context.Background(), RenderOptions{ProjectDir: root, Output: output})
			requireErrorKind(t, err, ErrorValidation)
			if result.PDFPath != "" {
				t.Fatalf("hardlinked resource has PDFPath %q", result.PDFPath)
			}
			after, readErr := os.ReadFile(output)
			if readErr != nil || string(after) != string(before) {
				t.Fatal("hardlinked resource changed")
			}
			requireNoRenderTemps(t, root)
		})
	}
}

func TestRenderProjectTraversalRemainsCompileDiagnostic(t *testing.T) {
	requireRealTypst(t)
	parent := testTempDir(t)
	root := filepath.Join(parent, "project")
	writeTestFile(t, filepath.Join(parent, "private.typ"), "Private document\n")
	writeTestFile(t, filepath.Join(root, "main.typ"), "#include \"../private.typ\"\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	requireErrorKind(t, err, ErrorCompile)
	if len(result.Diagnostics) == 0 || !strings.Contains(result.RawOutput, "error:") {
		t.Fatalf("resource traversal lost compiler diagnostic: %#v", result)
	}
}

func TestRenderBundledAndExplicitFonts(t *testing.T) {
	requireRealTypst(t)
	fontSource := filepath.Join("examples", "invoice", "fonts", "FiraSans-Regular.ttf")
	font, err := os.ReadFile(fontSource)
	if err != nil {
		t.Fatalf("read bundled integration font: %v", err)
	}
	for _, mode := range []string{"project_fonts", "relative_explicit", "absolute_explicit"} {
		t.Run(mode, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "#set text(font: \"Fira Sans\")\nCustom font\n")
			fontDir := filepath.Join(root, "fonts")
			options := RenderOptions{ProjectDir: root}
			if mode == "relative_explicit" {
				fontDir = filepath.Join(root, "custom fonts")
				options.FontPaths = []string{"custom fonts"}
			} else if mode == "absolute_explicit" {
				fontDir = testTempDir(t)
				options.FontPaths = []string{fontDir}
			}
			if err := os.MkdirAll(fontDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fontDir, "FiraSans-Regular.ttf"), font, 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := Render(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.RawOutput, "unknown font family") {
				t.Fatalf("custom font not discovered: %s", result.RawOutput)
			}
			requirePDF(t, result.PDFPath)
		})
	}
}

func TestRenderCannotOverwriteAutomaticOrExplicitFontInputs(t *testing.T) {
	requireRealTypst(t)
	font, err := os.ReadFile(filepath.Join("examples", "invoice", "fonts", "FiraSans-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"automatic", "explicit_relative", "explicit_absolute", "automatic_hardlink", "explicit_hardlink", "automatic_directory_alias", "explicit_directory_alias"} {
		t.Run(mode, func(t *testing.T) {
			root := testTempDir(t)
			writeTestFile(t, filepath.Join(root, "main.typ"), "#set text(font: \"Fira Sans\")\nProtected font\n")
			fontDir := filepath.Join(root, "fonts")
			options := RenderOptions{ProjectDir: root}
			if strings.HasPrefix(mode, "explicit") {
				fontDir = filepath.Join(root, "custom fonts")
				options.FontPaths = []string{"custom fonts"}
				if mode == "explicit_absolute" || mode == "explicit_hardlink" {
					fontDir = testTempDir(t)
					options.FontPaths = []string{fontDir}
				}
			}
			if err := os.MkdirAll(fontDir, 0o755); err != nil {
				t.Fatal(err)
			}
			fontPath := filepath.Join(fontDir, "FiraSans-Regular.ttf")
			if err := os.WriteFile(fontPath, font, 0o644); err != nil {
				t.Fatal(err)
			}
			output := fontPath
			if strings.HasSuffix(mode, "hardlink") {
				output = filepath.Join(root, "font alias.pdf")
				if err := os.Link(fontPath, output); err != nil {
					t.Fatal(err)
				}
			} else if strings.HasSuffix(mode, "directory_alias") {
				alias := filepath.Join(root, "font alias")
				makeTestDirectoryLink(t, fontDir, alias)
				output = filepath.Join(alias, "FiraSans-Regular.ttf")
			}
			options.Output = output
			result, err := Render(context.Background(), options)
			requireErrorKind(t, err, ErrorValidation)
			if result.PDFPath != "" {
				t.Fatalf("font collision published %q", result.PDFPath)
			}
			for _, path := range []string{fontPath, output} {
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(after, font) {
					t.Fatalf("font bytes changed at %q: %v", path, readErr)
				}
			}
			requireNoRenderTemps(t, filepath.Dir(output))
		})
	}
}

func TestRenderAllowsPDFOutputWhenProjectRootIsExplicitFontDirectory(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "main.typ"), "Existing output is not a font input\n")
	writeTestFile(t, filepath.Join(root, "output/result.pdf"), "%PDF-old result\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root, FontPaths: []string{"."}})
	if err != nil {
		t.Fatal(err)
	}
	requirePDF(t, result.PDFPath)
}

func TestRenderIgnoresAmbientCustomFontPathsAndAcceptsExplicitFonts(t *testing.T) {
	requireRealTypst(t)
	root := testTempDir(t)
	fontDir := testTempDir(t)
	font, err := os.ReadFile(filepath.Join("examples", "invoice", "fonts", "FiraSans-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fontDir, "FiraSans-Regular.ttf"), font, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "main.typ"), "#set text(font: \"Fira Sans\")\nExplicit fonts only\n")
	t.Setenv("TYPST_FONT_PATHS", fontDir)
	// Prevent a host installation of Fira Sans from masking whether the custom
	// directory is used. Embedded Typst fonts still provide a valid fallback.
	t.Setenv("TYPST_IGNORE_SYSTEM_FONTS", "true")
	ambient, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ambient.RawOutput, "warning: unknown font family: fira sans") {
		t.Fatalf("ambient custom font path was not ignored: %q", ambient.RawOutput)
	}
	requirePDF(t, ambient.PDFPath)
	explicit, err := Render(context.Background(), RenderOptions{ProjectDir: root, FontPaths: []string{fontDir}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(explicit.RawOutput, "unknown font family") {
		t.Fatalf("explicit validated font path was ignored: %s", explicit.RawOutput)
	}
	requirePDF(t, explicit.PDFPath)
}
