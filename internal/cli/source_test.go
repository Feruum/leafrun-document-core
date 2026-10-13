package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFolderSnapshotNativeAbsoluteEntryAndOutputSemantics(t *testing.T) {
	project := filepath.Join(t.TempDir(), "проект with spaces")
	entry := filepath.Join(project, "nested", "счёт.typ")
	if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "fonts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("= Accepted invoice"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"render", "--project", project, "--snapshot", "--entrypoint", entry, "--output", "exports/invoice.pdf", "--font-path", filepath.Join(project, "fonts")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("snapshot exit %d: %s", code, &stderr)
	}
	requirePrintedPDFPath(t, stdout.String(), filepath.Join(project, "exports/invoice.pdf"))
}

func TestRejectContradictorySourceFlags(t *testing.T) {
	cases := [][]string{
		{"--project", ".", "--archive", "input.zip"},
		{"--project", ".", "--git", "."},
		{"--archive", "input.zip", "--git", "."},
		{"--archive", "input.zip"},
		{"--git", ".", "--output", "out.pdf"},
		{"--project", ".", "--ref", "main"},
		{"--project", ".", "--source-root", "nested"},
		{"--archive", "input.zip", "--output", "out.pdf", "--ref", "main"},
		{"--git", ".", "--ref", "main", "--output", "out.pdf", "--snapshot=false"},
		{"--project", ".", "--archive", ""},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), append([]string{"render"}, args...), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("%v: exit %d stdout %q stderr %q", args, code, &stdout, &stderr)
		}
	}
}
func TestFolderSnapshotNativeRelativeExternalFonts(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	fonts := filepath.Join(base, "external fonts")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fonts, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("= Native font paths"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"render", "--project", project, "--snapshot", "--font-path", "../external fonts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("relative external font exit %d: %s", code, &stderr)
	}
	requirePrintedPDFPath(t, stdout.String(), filepath.Join(project, "output/result.pdf"))
}
