//go:build !windows

package documentcore

import (
	"context"
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
