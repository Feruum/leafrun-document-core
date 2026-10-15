package documentcore

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestProjectTechnicalValidationRejectsReservedInputRoot(t *testing.T) {
	ctx := context.Background()
	manifest, err := ReadProject(ctx, projectFixtureFS(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		path     string
		mode     fs.FileMode
		reserved bool
	}{
		{"root file", ".leafrun-input", 0, true},
		{"nonempty directory", ".leafrun-input/readme.txt", 0, true},
		{"empty directory", ".leafrun-input", fs.ModeDir, true},
		{"similar root file", ".leafrun-input-backup", 0, false},
		{"similar directory", ".leafrun-input-backup/readme.txt", 0, false},
		{"nested name", "assets/.leafrun-input/readme.txt", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := mutableProjectFixtureFS(t)
			(*files)[test.path] = &fstest.MapFile{Mode: test.mode}
			_, readErr := ReadProject(ctx, files)
			_, describeErr := DescribeTarget(ctx, files, manifest, projectSnapshotManifest(t, files), "invoice", "")
			for _, result := range []struct {
				operation string
				err       error
			}{{"ReadProject", readErr}, {"DescribeTarget", describeErr}} {
				if !test.reserved {
					if result.err != nil {
						t.Errorf("%s rejected allowed path %q: %v", result.operation, test.path, result.err)
					}
					continue
				}
				var diagnostic *ProjectValidationError
				if !errors.As(result.err, &diagnostic) || diagnostic.File != ".leafrun-input" || diagnostic.Pointer != "" || diagnostic.Code != "reserved_input_root" {
					t.Errorf("%s error = %v; want .leafrun-input reserved_input_root diagnostic", result.operation, result.err)
				}
			}
		})
	}
}
