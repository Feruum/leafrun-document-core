package documentcore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSnapshotTemporaryAliasRetargetPreservesReadOnlyBytesAndCleanup(t *testing.T) {
	selected := t.TempDir()
	unrelated := t.TempDir()
	alias := filepath.Join(t.TempDir(), "selected-temp")
	makeTestDirectoryLink(t, selected, alias)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("original")}}}}, SnapshotOptions{TempDir: alias})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	stores, err := os.ReadDir(selected)
	if err != nil || len(stores) != 1 {
		t.Fatalf("allocated stores = %v, %v", stores, err)
	}
	originalStore := filepath.Join(selected, stores[0].Name())
	replacementStore := filepath.Join(unrelated, stores[0].Name())
	if err := os.MkdirAll(filepath.Join(replacementStore, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	replacementFile := filepath.Join(replacementStore, "project", "main.typ")
	if err := os.WriteFile(replacementFile, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	makeTestDirectoryLink(t, unrelated, alias)
	data, err := fs.ReadFile(snapshot.FS(), "main.typ")
	if err != nil || string(data) != "original" {
		t.Errorf("accepted snapshot followed retargeted storage alias: %q, %v", data, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(originalStore); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Close left original storage: %v", err)
	}
	data, err = os.ReadFile(replacementFile)
	if err != nil || string(data) != "replacement" {
		t.Errorf("Close touched unrelated replacement storage: %q, %v", data, err)
	}
}

func TestSnapshotTemporaryAliasRetargetKeepsCompilerCopyOutsideInputs(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.typ"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	selected := t.TempDir()
	alias := filepath.Join(t.TempDir(), "selected-temp")
	makeTestDirectoryLink(t, selected, alias)
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: os.DirFS(project), ProtectedPaths: []string{project}}}, SnapshotOptions{TempDir: alias})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	stores, err := os.ReadDir(selected)
	if err != nil || len(stores) != 1 {
		t.Fatalf("allocated stores = %v, %v", stores, err)
	}
	bait := filepath.Join(project, stores[0].Name(), "project")
	if err := os.MkdirAll(bait, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bait, "main.typ"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	makeTestDirectoryLink(t, project, alias)
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	storage, err := canonicalDirectory(working.storage)
	if err != nil {
		t.Fatal(err)
	}
	input, err := canonicalDirectory(project)
	if err != nil {
		t.Fatal(err)
	}
	if isContained(input, storage) {
		t.Errorf("compiler copy allocated inside original inputs: %q", storage)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(working.root, "main.typ"))
	if err != nil || string(data) != "original" {
		t.Errorf("compiler copy lost independent inputs: %q, %v", data, err)
	}
	if err := working.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storage); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("compiler cleanup left allocated storage: %v", err)
	}
}

func TestSnapshotWindowsRejectsUnrepresentableComponentsBeforeStorageWrites(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native filename normalization")
	}
	for _, name := range []string{"file.", "file ", "dir./main.typ", "dir /main.typ", ".. /.. /escaped.typ", "bad<name", "bad>name", `bad"name`, "bad|name", "bad?name", "bad*name", "bad\x01name"} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			storage := t.TempDir()
			snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: fstest.MapFS{name: &fstest.MapFile{Data: []byte("original")}}}}, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			var stage *SnapshotError
			if snapshot != nil || !errors.As(err, &stage) || stage.Stage != SnapshotStageValidate {
				t.Errorf("unrepresentable path %q was not rejected before storage: %v", name, err)
			}
			entries, readErr := os.ReadDir(storage)
			if readErr != nil || len(entries) != 0 {
				t.Errorf("invalid native path left allocated storage: %v, %v", entries, readErr)
			}
		})
	}
}

func TestSnapshotWindowsAcceptedNamesRoundTripThroughNativeStorage(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows native filename normalization")
	}
	paths := []string{"plain.typ", "nested/file.typ", "space inside/main.typ", " leading space/name.typ", "unicode-λ/font.ttf"}
	input := fstest.MapFS{}
	for _, name := range paths {
		input[name] = &fstest.MapFile{Data: []byte(name)}
	}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := fstest.TestFS(snapshot.FS(), paths...); err != nil {
		t.Fatal(err)
	}
	for _, name := range paths {
		data, err := fs.ReadFile(snapshot.FS(), name)
		if err != nil || string(data) != name {
			t.Errorf("accepted logical path %q changed in native storage: %q, %v", name, data, err)
		}
	}
}

func TestSnapshotLogicalFilesystemRejectsCaseAliasesConsistently(t *testing.T) {
	input := fstest.MapFS{"main.typ": &fstest.MapFile{Data: []byte("captured")}, "nested/main.typ": &fstest.MapFile{Data: []byte("captured")}}
	snapshot, err := CreateSnapshot(context.Background(), &snapshotTestSource{tree: SourceTree{FS: input}}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	filesystem := snapshot.FS()
	for _, name := range []string{"MAIN.TYP", "NESTED", "Nested/main.typ", "nested/MAIN.TYP"} {
		t.Run(name, func(t *testing.T) {
			file, openErr := filesystem.Open(name)
			if file != nil {
				info, statErr := file.Stat()
				data, readErr := io.ReadAll(file)
				if err := file.Close(); err != nil {
					t.Error(err)
				}
				t.Errorf("unknown logical path opened: Stat=%v, %v; Read=%q, %v", info, statErr, data, readErr)
			}
			var logicalError *fs.PathError
			if !errors.Is(openErr, fs.ErrNotExist) || !errors.As(openErr, &logicalError) || logicalError.Op != "open" || logicalError.Path != name {
				t.Errorf("Open(%q) did not return a logical not-exist error: %v", name, openErr)
			}
			if _, err := fs.Stat(filesystem, name); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Stat(%q) = %v", name, err)
			}
			if data, err := fs.ReadFile(filesystem, name); !errors.Is(err, fs.ErrNotExist) || len(data) != 0 {
				t.Errorf("ReadFile(%q) = %q, %v", name, data, err)
			}
		})
	}
	for _, name := range []string{"main.typ", "nested/main.typ"} {
		file, err := filesystem.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		info, statErr := file.Stat()
		data, readErr := io.ReadAll(file)
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if statErr != nil || info.Name() != "main.typ" || readErr != nil || string(data) != "captured" {
			t.Errorf("exact path %q was inconsistent: Stat=%v, %v; Read=%q, %v", name, info, statErr, data, readErr)
		}
		data, err = fs.ReadFile(filesystem, name)
		if err != nil || string(data) != "captured" {
			t.Errorf("ReadFile(%q) = %q, %v", name, data, err)
		}
	}
	if err := fstest.TestFS(filesystem, "main.typ", "nested/main.typ"); err != nil {
		t.Fatal(err)
	}
}
