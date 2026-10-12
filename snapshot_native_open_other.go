//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package documentcore

import "os"

func openSnapshotNativeHandle(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
