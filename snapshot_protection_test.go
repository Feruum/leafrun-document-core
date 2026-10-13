package documentcore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func openedProtection(t *testing.T, path string) NativeProtection {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return NativeProtection{Path: path, Info: info}
}

func TestSnapshotEagerFileGuardSurvivesReplacementAndRemoval(t *testing.T) {
	requireRealTypst(t)
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "replaced", true: "removed"}[remove], func(t *testing.T) {
			root := testTempDir(t)
			original := filepath.Join(root, "accepted.zip")
			alias := filepath.Join(root, "alias.pdf")
			writeTestFile(t, original, "original archive")
			guard := openedProtection(t, original)
			if err := os.Link(original, alias); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
			if !remove {
				writeTestFile(t, original, "replacement archive")
			}
			source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Frozen")}}, ProtectedPaths: []string{original}, NativeProtection: []NativeProtection{guard}}}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: alias})
			if err == nil || result.PDFPath != "" {
				t.Fatalf("accepted identity overwritten: %+v %v", result, err)
			}
			data, _ := os.ReadFile(alias)
			if string(data) != "original archive" {
				t.Fatalf("alias changed: %q", data)
			}
			result, err = RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: original})
			if err == nil || result.PDFPath != "" {
				t.Fatalf("original association overwritten: %+v %v", result, err)
			}
			// A failed protected publication does not close the borrowed snapshot.
			if _, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: filepath.Join(testTempDir(t), "ok.pdf")}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotDirectoryGuardsAreNotContentMirrors(t *testing.T) {
	requireRealTypst(t)
	metadata := testTempDir(t)
	writeTestFile(t, filepath.Join(metadata, "config"), "mutable Git configuration")
	guard := openedProtection(t, metadata)
	source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Immutable Git export")}}, NativeProtection: []NativeProtection{guard}}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	output := filepath.Join(metadata, "nested/untracked.pdf")
	result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: output})
	if err == nil || result.PDFPath != "" {
		t.Fatalf("metadata prefix overwritten: %+v %v", result, err)
	}
	if _, err := os.Stat(output); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unexpected output: %v", err)
	}
}

func TestSnapshotRejectsMalformedNativeGuard(t *testing.T) {
	root := testTempDir(t)
	writeTestFile(t, filepath.Join(root, "input"), "input")
	guard := openedProtection(t, filepath.Join(root, "input"))
	cases := []NativeProtection{{Path: "relative", Info: guard.Info}, {Path: guard.Path, Info: nil}, {Path: guard.Path, Info: snapshotFileInfo{name: "fake", mode: 0644}}}
	for _, invalid := range cases {
		snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{}, NativeProtection: []NativeProtection{invalid}}}, SnapshotOptions{})
		if snapshot != nil {
			snapshot.Close()
		}
		if err == nil {
			t.Fatalf("invalid guard accepted: %+v", invalid)
		}
	}
}
func TestSnapshotLegacyProtectedFileIsAlwaysOriginalInput(t *testing.T) {
	requireRealTypst(t)
	original := filepath.Join(testTempDir(t), "legacy-archive.zip")
	writeTestFile(t, original, "accepted legacy source")
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Legacy source")}}, ProtectedPaths: []string{original}}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: original})
	if err == nil || result.PDFPath != "" {
		t.Fatalf("legacy source input overwritten: %+v %v", result, err)
	}
}
func TestSnapshotGuardsPreserveOriginalOutputAssociationAfterDirectoryRetarget(t *testing.T) {
	requireRealTypst(t)
	for _, kind := range []string{"directory", "file", "project resource", "external font"} {
		t.Run(kind, func(t *testing.T) {
			base := testTempDir(t)
			original := filepath.Join(base, "accepted")
			target := filepath.Join(base, "replacement")
			if err := os.Mkdir(original, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
			logical := "resource.pdf"
			if kind == "external font" {
				logical = "Fira.ttf"
			}
			entry := "main.typ"
			if kind == "project resource" {
				writeTestFile(t, filepath.Join(original, entry), "#read(\"resource.pdf\")")
			}
			input := filepath.Join(original, logical)
			writeTestFile(t, input, "accepted input")
			output := filepath.Join(original, logical)
			if kind == "directory" {
				output = filepath.Join(original, "nested/result.pdf")
			}
			source := &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("Accepted frozen bytes")}}}}
			options := SnapshotOptions{}
			var provider ProjectSource = source
			switch kind {
			case "directory":
				source.tree.NativeProtection = []NativeProtection{openedProtection(t, original)}
			case "file":
				source.tree.NativeProtection = []NativeProtection{openedProtection(t, input)}
			case "project resource":
				provider = FolderSource{Root: original}
			case "external font":
				options.FontPaths = []string{original}
			}
			snapshot, err := CreateSnapshot(context.Background(), provider, options)
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			if err := os.RemoveAll(original); err != nil {
				t.Fatal(err)
			}
			makeTestDirectoryLink(t, target, original)
			canonicalOutput := filepath.Join(target, logical)
			if kind == "directory" {
				canonicalOutput = filepath.Join(target, "nested/result.pdf")
			}
			writeTestFile(t, canonicalOutput, "previous target PDF")
			result, err := RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: output})
			if err == nil || result.PDFPath != "" {
				t.Fatalf("retargeted original %s association published: %+v %v", kind, result, err)
			}
			data, err := os.ReadFile(canonicalOutput)
			if err != nil || string(data) != "previous target PDF" {
				t.Fatalf("retargeted output changed: %q %v", data, err)
			}
			requireNoRenderTemps(t, filepath.Dir(canonicalOutput))
			result, err = RenderSnapshot(context.Background(), snapshot, RenderSnapshotOptions{Output: filepath.Join(testTempDir(t), "allowed.pdf")})
			if err != nil {
				t.Fatal(err)
			}
			requirePDF(t, result.PDFPath)
		})
	}
}
