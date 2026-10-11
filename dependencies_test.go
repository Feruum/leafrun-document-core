package documentcore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectDependenciesUsesCompilerWorkingDirectory(t *testing.T) {
	workingDirectory := testTempDir(t)
	t.Chdir(workingDirectory)
	root := filepath.Join(workingDirectory, "projects", "project")
	entry := filepath.Join(root, "main.typ")
	resource := filepath.Join(root, "resources", "data.json")
	writeTestFile(t, entry, "#json(\"resources/data.json\")\n")
	writeTestFile(t, resource, `{"title":"Resource protection"}`)
	deps := filepath.Join(workingDirectory, "compile.deps")
	entryRelative, err := filepath.Rel(workingDirectory, entry)
	if err != nil {
		t.Fatal(err)
	}
	resourceRelative, err := filepath.Rel(workingDirectory, resource)
	if err != nil {
		t.Fatal(err)
	}
	// Typst 0.15.1's zero format resolves project dependencies against the
	// compiler's current directory. Absolute external dependencies are valid too.
	data := []byte(entryRelative + "\x00" + resourceRelative + "\x00" + entry + "\x00")
	if err := os.WriteFile(deps, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		output    string
		collision bool
	}{
		{"replace_existing_pdf", filepath.Join(root, "output", "result.pdf"), false},
		{"protect_resource", resource, true},
		{"protect_hardlinked_resource", filepath.Join(root, "output", "resource-alias.pdf"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "protect_hardlinked_resource" {
				if err := os.MkdirAll(filepath.Dir(test.output), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(resource, test.output); err != nil {
					t.Fatal(err)
				}
			} else if !test.collision {
				writeTestFile(t, test.output, "%PDF-existing output\n")
			}
			old, err := os.ReadFile(test.output)
			if err != nil {
				t.Fatal(err)
			}
			err = protectDependencies(projectPaths{root: root, entry: entry, output: test.output}, deps, workingDirectory)
			if test.collision {
				requireErrorKind(t, err, ErrorValidation)
			} else if err != nil {
				t.Fatalf("valid cwd-relative dependencies blocked PDF replacement: %v", err)
			}
			data, err := os.ReadFile(test.output)
			if err != nil || !bytes.Equal(data, old) {
				t.Fatalf("dependency validation changed existing output: %v", err)
			}
		})
	}
}
