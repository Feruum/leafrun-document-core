package documentcore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFolderSourceRejectsSelectedFileSymlinksWithoutHidingTargets(t *testing.T) {
	for _, kind := range []string{"inside", "outside", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			root, outside, storage := t.TempDir(), t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "document")
			writeFolderFile(t, root, "resource.json", "project resource")
			writeFolderFile(t, outside, "resource.json", "external resource")
			target := filepath.Join(root, "resource.json")
			if kind == "outside" {
				target = filepath.Join(outside, "resource.json")
			}
			if kind == "dangling" {
				target = filepath.Join(root, "missing.json")
			}
			selected := filepath.Join(root, "result.pdf")
			if err := os.Symlink(target, selected); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("Windows file symlink privileges unavailable: %v", err)
				}
				t.Fatal(err)
			}
			source := &folderHookSource{source: FolderSource{Root: root}}
			options := SnapshotOptions{Output: "result.pdf", TempDir: storage}
			snapshot, err := CreateSnapshot(context.Background(), source, options)
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || err == nil {
				t.Fatalf("selected %s file link was accepted: %v", kind, err)
			}
			var failure *SnapshotError
			if !errors.As(err, &failure) || failure.Stage != SnapshotStageAcquire {
				t.Fatalf("selected link was not rejected during acquisition: %v", err)
			}
			if kind == "dangling" && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("dangling selected link lost its missing-target cause: %v", err)
			}
			if source.opens != 1 || source.releases != 1 {
				t.Fatalf("selected-link lifecycle = %d/%d", source.opens, source.releases)
			}
			requireEmptyFolderStorage(t, storage)
			for filename, want := range map[string]string{filepath.Join(root, "resource.json"): "project resource", filepath.Join(outside, "resource.json"): "external resource"} {
				info, err := os.Lstat(filename)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("rejection changed ordinary target %q: %v", filename, err)
				}
				data, err := os.ReadFile(filename)
				if err != nil || string(data) != want {
					t.Fatalf("rejection changed target bytes: %q, %v", data, err)
				}
			}
			if err := os.Remove(selected); err != nil {
				t.Fatal(err)
			}
			accepted, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, options)
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			data, err := fs.ReadFile(accepted.FS(), "resource.json")
			if err != nil || string(data) != "project resource" {
				t.Fatalf("removed output link still hid its ordinary target: %q, %v", data, err)
			}
			if _, err := fs.ReadFile(accepted.FS(), "result.pdf"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("removed selected result remains captured: %v", err)
			}
		})
	}
}

func TestFolderSourceRejectsSelectedDirectoryJunctionDuringAcquisition(t *testing.T) {
	root, target, storage := t.TempDir(), t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "resource.json", "project resource")
	writeFolderFile(t, target, "resource.json", "target resource")
	selected := filepath.Join(root, "result.pdf")
	makeTestDirectoryLink(t, target, selected)
	source := &folderHookSource{source: FolderSource{Root: root}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: "result.pdf", TempDir: storage})
	if snapshot != nil {
		snapshot.Close()
	}
	var failure *SnapshotError
	if snapshot != nil || !errors.As(err, &failure) || failure.Stage != SnapshotStageAcquire {
		t.Fatalf("selected directory link was canonicalized: %v, %v", snapshot, err)
	}
	if source.releases != 1 {
		t.Fatalf("selected directory link released source %d times", source.releases)
	}
	requireEmptyFolderStorage(t, storage)
	data, err := os.ReadFile(filepath.Join(target, "resource.json"))
	if err != nil || string(data) != "target resource" {
		t.Fatalf("rejection changed linked directory: %q, %v", data, err)
	}
}

func TestFolderSourceRejectsSelectedFileLinkAddedAfterAcquisition(t *testing.T) {
	root, storage := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "resource.json", "resource")
	selected := filepath.Join(root, "result.pdf")
	if err := os.Symlink(filepath.Join(root, "resource.json"), selected); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows file symlink privileges unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onOpen = func(name string, pass int) {
		if name == "main.typ" && pass == 2 {
			if err := os.Symlink(filepath.Join(root, "resource.json"), selected); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: "result.pdf", TempDir: storage})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("selected file link bypassed final source validation: %v, %v", snapshot, err)
	}
	if source.releases != 1 {
		t.Fatalf("changing selected result released source %d times", source.releases)
	}
	requireEmptyFolderStorage(t, storage)
	data, err := os.ReadFile(filepath.Join(root, "resource.json"))
	if err != nil || string(data) != "resource" {
		t.Fatalf("selected file link changed its ordinary target: %q, %v", data, err)
	}
}
