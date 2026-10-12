package documentcore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestFolderSourceChecksSelectedResultKindAfterAllHashes(t *testing.T) {
	for _, selected := range []string{"result.pdf", "output/result.pdf"} {
		for _, pass := range []int{2, 3} {
			for _, kind := range []string{"symlink", "junction", "replace", "remove"} {
				t.Run(selected+string(rune('0'+pass))+"-"+kind, func(t *testing.T) {
					root, outside, storage := t.TempDir(), t.TempDir(), t.TempDir()
					writeFolderFile(t, root, "main.typ", "document")
					writeFolderFile(t, root, "resource.json", "resource")
					writeFolderFile(t, outside, "resource.json", "outside resource")
					filename := filepath.Join(root, filepath.FromSlash(selected))
					if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						if err := os.Symlink(filepath.Join(root, "resource.json"), filename); err != nil {
							if runtime.GOOS == "windows" {
								t.Skipf("Windows file symlink privileges unavailable: %v", err)
							}
							t.Fatal(err)
						}
						if err := os.Remove(filename); err != nil {
							t.Fatal(err)
						}
					}
					writeFolderFile(t, root, selected, "old result")
					if err := os.Link(filename, filepath.Join(outside, "keep-original.pdf")); err != nil {
						t.Fatal(err)
					}
					original, err := snapshotNativeFileInfo(filename)
					if err != nil {
						t.Fatal(err)
					}
					source := &folderHookSource{source: FolderSource{Root: root}}
					source.onClose = func(name string, count int) {
						if name != "main.typ" || count != pass {
							return
						}
						if err := os.Remove(filename); err != nil {
							t.Fatal(err)
						}
						switch kind {
						case "symlink":
							if err := os.Symlink(filepath.Join(root, "resource.json"), filename); err != nil {
								t.Fatal(err)
							}
						case "junction":
							makeTestDirectoryLink(t, outside, filename)
						case "replace":
							writeFolderFile(t, root, selected, "replacement result with different bytes and metadata")
						}
					}
					snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: selected, TempDir: storage, FontPaths: []string{"."}})
					if source.opens != 1 || source.releases != 1 {
						t.Fatalf("final result validation lifecycle = %d/%d", source.opens, source.releases)
					}
					if kind == "symlink" || kind == "junction" {
						if snapshot != nil {
							snapshot.Close()
						}
						if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
							t.Fatalf("selected result changed to %s after pass %d: %v, %v", kind, pass, snapshot, err)
						}
						if !strings.Contains(err.Error(), "ordinary") {
							t.Fatalf("changed result lost its kind validation cause: %v", err)
						}
						requireEmptyFolderStorage(t, storage)
						return
					}
					if err != nil || snapshot == nil {
						t.Fatalf("ordinary %s result churn was rejected after pass %d: %v", kind, pass, err)
					}
					defer snapshot.Close()
					data, err := fs.ReadFile(snapshot.FS(), "resource.json")
					if err != nil || string(data) != "resource" {
						t.Fatalf("result churn changed included bytes: %q, %v", data, err)
					}
					if _, err := fs.ReadFile(snapshot.FS(), selected); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("generated result entered accepted bytes: %v", err)
					}
					found := false
					for _, input := range snapshot.inputs {
						if input.projectPath == selected {
							found = true
							if !input.excluded || input.font || !os.SameFile(input.info, original) {
								t.Fatalf("ordinary result churn lost original guard metadata: %+v", input)
							}
						}
						if !input.excluded && !input.info.Mode().IsRegular() {
							t.Fatalf("directory identity entered publication guard inputs: %+v", input)
						}
					}
					if !found {
						t.Fatal("ordinary result churn lost excluded result metadata")
					}
				})
			}
		}
	}
}

func TestFolderSourceChecksLexicalAndCanonicalResultKindsAfterAliasRetargeting(t *testing.T) {
	for _, changed := range []string{"lexical", "canonical"} {
		t.Run(changed, func(t *testing.T) {
			root, other, storage := t.TempDir(), t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "document")
			writeFolderFile(t, root, "resource.json", "resource")
			writeFolderFile(t, root, "output/result.pdf", "old result")
			if changed == "lexical" {
				if err := os.Mkdir(filepath.Join(other, "output"), 0o700); err != nil {
					t.Fatal(err)
				}
				makeTestDirectoryLink(t, t.TempDir(), filepath.Join(other, "output", "result.pdf"))
			} else {
				writeFolderFile(t, other, "output/result.pdf", "other ordinary result")
			}
			alias := filepath.Join(t.TempDir(), "selected-parent")
			makeTestDirectoryLink(t, root, alias)
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onClose = func(name string, pass int) {
				if name != "main.typ" || pass != 3 {
					return
				}
				if changed == "canonical" {
					selected := filepath.Join(root, "output", "result.pdf")
					if err := os.Remove(selected); err != nil {
						t.Fatal(err)
					}
					makeTestDirectoryLink(t, t.TempDir(), selected)
				}
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				makeTestDirectoryLink(t, other, alias)
			}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: filepath.Join(alias, "output", "result.pdf"), TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) || !strings.Contains(err.Error(), "ordinary") {
				t.Fatalf("%s result kind bypassed final alias validation: %v, %v", changed, snapshot, err)
			}
			if source.releases != 1 {
				t.Fatalf("final alias validation released source %d times", source.releases)
			}
			requireEmptyFolderStorage(t, storage)
		})
	}
}
