//go:build !windows

package documentcore

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFolderSourceRejectsNativeSpecialFiles(t *testing.T) {
	for _, kind := range []string{"fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			// Keep socket paths below macOS's native sockaddr_un pathname limit.
			root, err := os.MkdirTemp("", "leafrun-special-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(root); err != nil {
					t.Error(err)
				}
			})
			writeFolderFile(t, root, "main.typ", "document")
			name := filepath.Join(root, kind)
			if kind == "fifo" {
				if err := syscall.Mkfifo(name, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				listener, err := net.Listen("unix", name)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir()})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || err == nil {
				t.Fatalf("special %s accepted: %v, %v", kind, snapshot, err)
			}
		})
	}
}

func TestFolderSourceRejectsSelectedNativeSpecialFileDuringAcquisition(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	selected := filepath.Join(root, "result.pdf")
	if err := syscall.Mkfifo(selected, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{Output: "result.pdf", TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	var failure *SnapshotError
	if snapshot != nil || !errors.As(err, &failure) || failure.Stage != SnapshotStageAcquire {
		t.Fatalf("selected special file was not rejected during acquisition: %v, %v", snapshot, err)
	}
}
