//go:build windows

package documentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRenderWindowsLockedOutputLeavesOldPDF(t *testing.T) {
	requireRealTypst(t)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.typ"), "Locked output\n")
	output := filepath.Join(root, "output/result.pdf")
	old := "%PDF-locked existing output\n"
	writeTestFile(t, output, old)
	path, err := syscall.UTF16PtrFromString(output)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	requireErrorKind(t, err, ErrorStorage)
	if result.PDFPath != "" {
		t.Fatalf("locked output published %q", result.PDFPath)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil || string(data) != old {
		t.Fatal("locked output changed")
	}
	requireNoRenderTemps(t, filepath.Dir(output))
}

func TestCanonicalDirectoryDoesNotRequireAncestorEnumeration(t *testing.T) {
	root := t.TempDir()
	got, err := canonicalDirectory(root)
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("canonicalDirectory(%q) = %q, %v", root, got, err)
	}
	original, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := os.Stat(got)
	if err != nil || !os.SameFile(original, resolved) {
		t.Fatalf("canonical path does not identify original directory: %v", err)
	}
}

func TestRenderWindowsLongPaths(t *testing.T) {
	requireRealTypst(t)
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		root = filepath.Join(root, strings.Repeat("long-path-", 6))
	}
	writeTestFile(t, filepath.Join(root, "main.typ"), "Long Windows path\n")
	result, err := Render(context.Background(), RenderOptions{ProjectDir: root})
	if err != nil {
		t.Fatal(err)
	}
	requirePDF(t, result.PDFPath)
}
