//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package documentcore

import (
	"os"
	"syscall"
)

func openSnapshotNativeHandle(root *os.Root, name string) (*os.File, error) {
	// A checked regular file can become a FIFO before acquisition. Do not wait
	// for a writer or acquire a controlling terminal before validating Stat.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
