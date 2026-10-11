package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		nil,
		{"watch"},
		{"render"},
		{"render", "--project", ".", "--unknown"},
		{"render", "--project", ".", "extra"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit = %d; stderr = %s", code, &stderr)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"render", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "--project") || stderr.Len() != 0 {
			t.Fatalf("help: exit = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
		}
	}
}

func TestRenderCommand(t *testing.T) {
	project := filepath.Join(t.TempDir(), "проект with spaces")
	if err := os.MkdirAll(filepath.Join(project, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "extra fonts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "templates", "invoice.typ"), []byte("= Invoice\nHello from CLI."), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"render", "--project", project, "--entrypoint", "templates/invoice.typ", "--output", "exports/invoice.pdf", "--font-path", "extra fonts", "--font-path", "templates"}, &stdout, &stderr)
	expected := filepath.Join(project, "exports", "invoice.pdf")
	if code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
	}
	requirePrintedPDFPath(t, stdout.String(), expected)
	content, err := os.ReadFile(expected)
	if err != nil || !bytes.HasPrefix(content, []byte("%PDF-")) {
		t.Fatalf("missing PDF: %v", err)
	}
}

func TestCompileFailurePreservesPDF(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "output"), 0755); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(project, "output", "result.pdf")
	if err := os.WriteFile(previous, []byte("previous PDF sentinel"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("#let broken ="), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"render", "--project", project}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "error:") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
	}
	content, err := os.ReadFile(previous)
	if err != nil || string(content) != "previous PDF sentinel" {
		t.Fatalf("previous result changed: %q, %v", content, err)
	}
}

func TestWarningIsSuccessful(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("#set text(font: \"LeafrunMissingFont9f08\")\nHello."), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"render", "--project", project}, &stdout, &stderr); code != 0 || !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
	}
	requirePrintedPDFPath(t, stdout.String(), filepath.Join(project, "output", "result.pdf"))
}

func requirePrintedPDFPath(t *testing.T, stdout, expected string) {
	t.Helper()
	path := strings.TrimSpace(stdout)
	if !filepath.IsAbs(path) || strings.Contains(path, "\n") {
		t.Fatalf("expected one absolute PDF path, got %q", stdout)
	}
	actualFile, actualErr := os.Stat(path)
	expectedFile, expectedErr := os.Stat(expected)
	if actualErr != nil || expectedErr != nil || !os.SameFile(actualFile, expectedFile) {
		t.Fatalf("stdout path %q does not identify %q: %v, %v", path, expected, actualErr, expectedErr)
	}
}
