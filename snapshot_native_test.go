package documentcore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotNativeIdentityRequiresRegularFile(t *testing.T) {
	name := t.TempDir()
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := snapshotNativeIdentity(name, before); info != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("directory identity = %v, %v, want source changed", info, err)
	}
}

func TestSnapshotNativeOpenSupportsOrdinaryFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "main.typ")
	if err := os.WriteFile(name, []byte("ordinary input"), 0o600); err != nil {
		t.Fatal(err)
	}
	native, err := openSnapshotNativeFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	for _, name := range []string{".", "main.typ"} {
		file, err := native.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		info, statErr := file.Stat()
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if statErr != nil || info.IsDir() != (name == ".") {
			t.Fatalf("Open(%q) = %v, %v", name, info, statErr)
		}
	}
	if entries, err := native.ReadDir("."); err != nil || len(entries) != 1 || entries[0].Name() != "main.typ" {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
}
