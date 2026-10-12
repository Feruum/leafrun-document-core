//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package documentcore

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotNativeIdentityPreservesOpenErrorAfterSocketReplacement(t *testing.T) {
	// Unix socket names must fit macOS's native pathname limit.
	root, err := os.MkdirTemp("", "leafrun-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	name := filepath.Join(root, "input.typ")
	if err := os.WriteFile(name, []byte("ordinary input"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	file, openErr := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if file != nil {
		file.Close()
		t.Fatal("native socket unexpectedly opened as a file")
	}
	var original *os.PathError
	if !errors.As(openErr, &original) {
		t.Fatalf("native socket open error = %v", openErr)
	}
	info, err := snapshotNativeIdentity(name, before)
	if info != nil || !errors.Is(err, ErrSourceChanged) || !errors.Is(err, original.Err) {
		t.Fatalf("socket replacement = %v, %v, want source changed and native cause %v", info, err, original.Err)
	}
}

func TestSnapshotNativeIdentityRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	name := filepath.Join(t.TempDir(), "font.ttf")
	if err := os.WriteFile(name, []byte("ordinary input"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the file after the caller's last regular-file validation.
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := snapshotNativeIdentity(name, before)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSourceChanged) {
			t.Fatalf("FIFO replacement error = %v, want source changed", err)
		}
	case <-time.After(2 * time.Second):
		// Release and drain a regressed blocking open before reporting failure.
		unblock, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unblock.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("identity acquisition did not drain after releasing the FIFO")
		}
		t.Fatal("identity acquisition blocked on a replacement FIFO")
	}
}
