package documentcore

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestSnapshotNativeOpenRejectsFIFOWhilePathsAreExchanged(t *testing.T) {
	root := t.TempDir()
	input, swap := filepath.Join(root, "main.typ"), filepath.Join(root, "swap")
	if err := os.WriteFile(input, []byte("ordinary input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(swap, 0o600); err != nil {
		t.Fatal(err)
	}
	native, err := openSnapshotNativeFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	inputPtr, err := syscall.BytePtrFromString(input)
	if err != nil {
		t.Fatal(err)
	}
	swapPtr, err := syscall.BytePtrFromString(swap)
	if err != nil {
		t.Fatal(err)
	}
	stop, mutationDone := make(chan struct{}), make(chan struct{})
	mutationErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		defer close(mutationDone)
		for iteration := 0; ; iteration++ {
			select {
			case <-stop:
				return
			default:
			}
			// Linux amd64 renameat2(AT_FDCWD, ..., RENAME_EXCHANGE)
			// atomically keeps both entries present and the FIFO inode alive.
			_, _, errno := syscall.Syscall6(316, ^uintptr(99), uintptr(unsafe.Pointer(inputPtr)), ^uintptr(99), uintptr(unsafe.Pointer(swapPtr)), 2, 0)
			if errno != 0 {
				mutationErr <- errno
				if iteration == 0 {
					close(started)
				}
				return
			}
			if iteration == 0 {
				close(started)
			}
			runtime.Gosched()
		}
	}()
	<-started
	var readers sync.WaitGroup
	readerErrors := make(chan error, 128)
	for range 128 {
		readers.Go(func() {
			for range 64 {
				file, err := native.Open("main.typ")
				if err != nil {
					continue
				}
				info, statErr := file.Stat()
				closeErr := file.Close()
				if statErr != nil || closeErr != nil {
					readerErrors <- fmt.Errorf("opened handle failed: stat=%v, close=%v", statErr, closeErr)
					return
				}
				if !info.Mode().IsRegular() {
					readerErrors <- fmt.Errorf("exposed a special handle: %v", info.Mode())
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { readers.Wait(); close(done) }()
	blocked := false
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		blocked = true
	}
	close(stop)
	<-mutationDone
	// Release the one FIFO inode wherever the last atomic exchange left it.
	// This makes the RED run bounded without abandoning blocked goroutines.
	for _, name := range []string{input, swap} {
		unblock, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unblock.Close()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("native readers did not drain after releasing the FIFO")
	}
	select {
	case err := <-mutationErr:
		t.Fatal(err)
	default:
	}
	close(readerErrors)
	for err := range readerErrors {
		t.Error(err)
	}
	if blocked {
		t.Error("native acquisition blocked during ordinary-file/FIFO exchange")
	}
}
