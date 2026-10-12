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

func TestFolderSourceDetectsSameSizeSameTimestampContentChangesWithoutRetry(t *testing.T) {
	for _, sequence := range [][]string{{"AAAA", "BBBB", "BBBB"}, {"AAAA", "BBBB", "AAAA"}, {"AAAA", "AAAA", "BBBB"}} {
		t.Run(strings.Join(sequence, "-"), func(t *testing.T) {
			root, storage := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "AAAA")
			original, err := os.Stat(filepath.Join(root, "main.typ"))
			if err != nil {
				t.Fatal(err)
			}
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onOpen = func(name string, count int) {
				if name != "main.typ" {
					return
				}
				writeFolderFile(t, root, "main.typ", sequence[count-1])
				if err := os.Chtimes(filepath.Join(root, "main.typ"), original.ModTime(), original.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("same-size/time mutation was accepted: %v, %v", snapshot, err)
			}
			if source.opens != 1 || source.releases != 1 || source.counts["main.typ"] != 3 {
				t.Fatalf("source was retried or not verified: %+v", source)
			}
			requireEmptyFolderStorage(t, storage)
		})
	}
}

func TestFolderSourceDetectsNativeFileReplacementWithIdenticalBytesAndTimestamp(t *testing.T) {
	root, storage := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "AAAA")
	filename := filepath.Join(root, "main.typ")
	original, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the old identity allocated, so inode/file-ID reuse cannot hide replacement.
	if err := os.Link(filename, filepath.Join(t.TempDir(), "keep-original")); err != nil {
		t.Fatal(err)
	}
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onOpen = func(name string, count int) {
		if name != "main.typ" || count != 2 {
			return
		}
		if err := os.Remove(filename); err != nil {
			t.Fatal(err)
		}
		writeFolderFile(t, root, "main.typ", "AAAA")
		if err := os.Chtimes(filename, original.ModTime(), original.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replaced native identity was accepted: %v, %v", snapshot, err)
	}
	if source.opens != 1 || source.releases != 1 || source.counts["main.typ"] != 3 {
		t.Fatalf("replacement retried or leaked capture: %+v", source)
	}
	requireEmptyFolderStorage(t, storage)
}

func TestFolderSourceDetectsReplacementBetweenScanAndNativeMetadataRetention(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "AAAA")
	filename := filepath.Join(root, "main.typ")
	original, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filename, filepath.Join(t.TempDir(), "keep-original")); err != nil {
		t.Fatal(err)
	}
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onClose = func(name string, pass int) {
		if name != "main.typ" || pass != 1 {
			return
		}
		if err := os.Remove(filename); err != nil {
			t.Fatal(err)
		}
		writeFolderFile(t, root, "main.typ", "AAAA")
		if err := os.Chtimes(filename, original.ModTime(), original.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replacement before metadata retention accepted: %v, %v", snapshot, err)
	}
}

func TestFolderSourceClassifiesDisappearanceBetweenScanAndMetadataRetention(t *testing.T) {
	root, storage := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "AAAA")
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onClose = func(name string, pass int) {
		if name == "main.typ" && pass == 1 {
			if err := os.Remove(filepath.Join(root, "main.typ")); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disappearance before metadata retention lost classification: %v, %v", snapshot, err)
	}
	if source.releases != 1 {
		t.Fatalf("disappearing source released %d times", source.releases)
	}
	requireEmptyFolderStorage(t, storage)
}

func TestFolderSourceClassifiesDisappearingFilesDuringEveryReadPass(t *testing.T) {
	for _, pass := range []int{1, 2, 3} {
		t.Run(string(rune('0'+pass)), func(t *testing.T) {
			root, storage := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "AAAA")
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onOpen = func(name string, count int) {
				if name == "main.typ" && count == pass {
					if err := os.Remove(filepath.Join(root, "main.typ")); err != nil {
						t.Fatal(err)
					}
				}
			}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) || !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("disappearing source lost classification: %v, %v", snapshot, err)
			}
			if source.opens != 1 || source.releases != 1 {
				t.Fatalf("source lifecycle = %d/%d", source.opens, source.releases)
			}
			requireEmptyFolderStorage(t, storage)
		})
	}
}

func TestFolderSourceClassifiesIncompleteNativeReadsDuringEveryPass(t *testing.T) {
	for _, pass := range []int{1, 2, 3} {
		t.Run(string(rune('0'+pass)), func(t *testing.T) {
			root, storage := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "AAAA")
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onRead = func(name string, count int) {
				if name == "main.typ" && count == pass {
					if err := os.Truncate(filepath.Join(root, "main.typ"), 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("incomplete native read was accepted: %v, %v", snapshot, err)
			}
			if source.opens != 1 || source.releases != 1 {
				t.Fatalf("incomplete source lifecycle = %d/%d", source.opens, source.releases)
			}
			requireEmptyFolderStorage(t, storage)
		})
	}
}

func TestFolderSourceClassifiesNativeReadFailuresDuringInitialHashing(t *testing.T) {
	root, storage := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "AAAA")
	source := snapshotSourceFunc(func(ctx context.Context) (SourceTree, error) {
		tree, err := (FolderSource{Root: root}).Open(ctx)
		if err == nil {
			tree.FS = folderNativeReadFailureFS{tree.FS}
		}
		return tree, err
	})
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("incomplete initial native read lost cause: %v, %v", snapshot, err)
	}
	requireEmptyFolderStorage(t, storage)
}

// Fail below the folder read adapter after reading actual native bytes. No
// production hook or global filesystem replacement is needed for this fault.
type folderNativeReadFailureFS struct{ fs.FS }

func (filesystem folderNativeReadFailureFS) Open(name string) (fs.File, error) {
	file, err := filesystem.FS.Open(name)
	if err == nil && name == "main.typ" {
		native := file.(*folderFile)
		native.File = folderNativeReadFailureFile{native.File}
	}
	return file, err
}

type folderNativeReadFailureFile struct{ fs.File }

func (file folderNativeReadFailureFile) Read(buffer []byte) (int, error) {
	n, err := file.File.Read(buffer)
	if n > 0 {
		return n - 1, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestFolderSourceVerifiesAdditionalFontHashesAndReleasesOnFailure(t *testing.T) {
	root, fonts, storage := t.TempDir(), t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "text")
	writeFolderFile(t, fonts, "sample.ttf", "AAAA")
	filename := filepath.Join(fonts, "sample.ttf")
	original, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	source := &folderHookSource{source: FolderSource{Root: root}}
	fontHook := &folderHookSource{}
	fontHook.counts = make(map[string]int)
	fontHook.onOpen = func(name string, count int) {
		if name != "sample.ttf" || count != 3 {
			return
		}
		writeFolderFile(t, fonts, "sample.ttf", "BBBB")
		if err := os.Chtimes(filename, original.ModTime(), original.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	var ownedFontFS fs.FS
	source.wrapFontFS = func(_ string, filesystem fs.FS) fs.FS {
		ownedFontFS = filesystem
		return &folderHookFS{FS: filesystem, source: fontHook}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage, FontPaths: []string{fonts}})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) || fontHook.counts["sample.ttf"] != 3 {
		t.Fatalf("same-size/time native font mutation accepted: %v, %v; %d reads", snapshot, err, fontHook.counts["sample.ttf"])
	}
	if source.releases != 1 {
		t.Fatalf("font failure released source %d times", source.releases)
	}
	if _, err := ownedFontFS.Open("sample.ttf"); err == nil {
		t.Fatal("font failure retained source-owned root")
	}
	requireEmptyFolderStorage(t, storage)
}

func TestFolderSourceChecksIncludedInventoryAfterCopy(t *testing.T) {
	for _, name := range []string{"new.typ", "new-empty-directory", "removed-file"} {
		t.Run(name, func(t *testing.T) {
			root, storage := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "AAAA")
			writeFolderFile(t, root, "z.typ", "BBBB")
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onOpen = func(path string, count int) {
				if path != "main.typ" || count != 2 {
					return
				}
				switch name {
				case "new.typ":
					writeFolderFile(t, root, name, "new")
				case "new-empty-directory":
					if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
						t.Fatal(err)
					}
				case "removed-file":
					if err := os.Remove(filepath.Join(root, "z.typ")); err != nil {
						t.Fatal(err)
					}
				}
			}
			snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("included inventory mutation accepted: %v, %v", snapshot, err)
			}
			requireEmptyFolderStorage(t, storage)
		})
	}
}

func TestFolderSourceCancellationReleasesHandlesAndOwnedStorage(t *testing.T) {
	for _, pass := range []int{1, 2, 3} {
		t.Run(string(rune('0'+pass)), func(t *testing.T) {
			root, storage := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "AAAA")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &folderHookSource{source: FolderSource{Root: root}}
			source.onOpen = func(name string, count int) {
				if name == "main.typ" && count == pass {
					cancel()
				}
			}
			snapshot, err := CreateSnapshot(ctx, source, SnapshotOptions{TempDir: storage})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled capture = %v, %v", snapshot, err)
			}
			if source.opens != 1 || source.releases != 1 {
				t.Fatalf("canceled source lifecycle = %d/%d", source.opens, source.releases)
			}
			requireEmptyFolderStorage(t, storage)
			// Removing the root succeeds on Windows only after its os.Root handle closes.
			if err := os.RemoveAll(root); err != nil {
				t.Fatalf("canceled source retained its root: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &folderHookSource{source: FolderSource{Root: t.TempDir()}}
	if snapshot, err := CreateSnapshot(ctx, source, SnapshotOptions{}); snapshot != nil || !errors.Is(err, context.Canceled) || source.opens != 0 {
		t.Fatalf("pre-canceled source was opened: %v, %v, %d", snapshot, err, source.opens)
	}
}

func TestFolderSourceAcquisitionFailureStillReleasesPinnedRoot(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "caller-owned")
	source := &folderHookSource{source: FolderSource{Root: root}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: "invalid\x00result.pdf", TempDir: t.TempDir()})
	if snapshot != nil || err == nil || source.opens != 1 || source.releases != 1 {
		t.Fatalf("failed acquisition lifecycle: %v, %v, %d/%d", snapshot, err, source.opens, source.releases)
	}
	data, err := os.ReadFile(filepath.Join(root, "main.typ"))
	if err != nil || string(data) != "caller-owned" {
		t.Fatalf("failed acquisition changed caller project: %q, %v", data, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("failed acquisition retained root: %v", err)
	}
}

func TestFolderSourceRejectsInternalProjectAndFontDirectoryLinks(t *testing.T) {
	for _, target := range []string{"inside-project", "outside-project", "inside-fonts"} {
		t.Run(target, func(t *testing.T) {
			root, external := t.TempDir(), t.TempDir()
			writeFolderFile(t, root, "main.typ", "document")
			writeFolderFile(t, root, "ordinary/data.json", "resource")
			writeFolderFile(t, external, "ordinary/sample.ttf", "font")
			options := SnapshotOptions{TempDir: t.TempDir()}
			linkTarget, linkParent := filepath.Join(root, "ordinary"), root
			if target == "outside-project" {
				linkTarget = external
			}
			if target == "inside-fonts" {
				linkTarget, linkParent = filepath.Join(external, "ordinary"), external
				options.FontPaths = []string{external}
			}
			makeTestDirectoryLink(t, linkTarget, filepath.Join(linkParent, "linked"))
			source := &folderHookSource{source: FolderSource{Root: root}}
			snapshot, err := CreateSnapshot(context.Background(), source, options)
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || err == nil || source.releases != 1 {
				t.Fatalf("internal directory link accepted or leaked: %v, %v", snapshot, err)
			}
			requireEmptyFolderStorage(t, options.TempDir)
		})
	}
}

func TestFolderSourceRejectsInternalFileSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	if err := os.Symlink(filepath.Join(root, "main.typ"), filepath.Join(root, "alias.typ")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows file symlink privileges unavailable: %v", err)
		}
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || err == nil {
		t.Fatalf("internal file symlink accepted: %v, %v", snapshot, err)
	}
}

func TestFolderSourceFilesystemBoundsReadsAndRetainsDirectorySemantics(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "nested/data.json", "resource")
	writeFolderFile(t, root, ".leafrun-staging", "excluded")
	writeFolderFile(t, root, ".git/config", "excluded")
	tree, err := (FolderSource{Root: root}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if err := fstest.TestFS(tree.FS, "main.typ", "nested/data.json"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "nested/../../outside", filepath.ToSlash(filepath.Join(t.TempDir(), "external")), "nested\\data.json", "./main.typ", ""} {
		file, err := tree.FS.Open(name)
		if file != nil {
			file.Close()
		}
		if file != nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("unbounded path %q accepted: %v", name, err)
		}
	}
}

func requireEmptyFolderStorage(t *testing.T, storage string) {
	t.Helper()
	entries, err := os.ReadDir(storage)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed capture left owned storage: %v, %v", entries, err)
	}
}
