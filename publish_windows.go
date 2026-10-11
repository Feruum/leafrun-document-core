//go:build windows

package documentcore

import (
	"os"
	"syscall"
	"unsafe"
)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func publishPDF(staging, output string) error {
	from, err := syscall.UTF16PtrFromString(staging)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(output)
	if err != nil {
		return err
	}
	// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH. The old path is never
	// removed first; a sharing violation leaves the old PDF intact.
	ok, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8)
	if ok == 0 {
		return &os.LinkError{Op: "MoveFileEx", Old: staging, New: output, Err: callErr}
	}
	return nil
}
