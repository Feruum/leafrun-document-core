//go:build windows

package documentcore

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var finalPathByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

// Resolve through an OS handle. Unlike filepath.EvalSymlinks on Windows, this
// does not enumerate each ancestor merely to normalize its letter casing.
func resolvePath(path string) (string, error) {
	encoded, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := syscall.CreateFile(encoded, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "resolve", Path: path, Err: err}
	}
	defer syscall.CloseHandle(handle)
	buffer := make([]uint16, 256)
	for {
		length, _, callErr := finalPathByHandle.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		if length == 0 {
			return "", &os.PathError{Op: "resolve", Path: path, Err: callErr}
		}
		if length >= uintptr(len(buffer)) {
			buffer = make([]uint16, length+1)
			continue
		}
		resolved := syscall.UTF16ToString(buffer[:length])
		if strings.HasPrefix(resolved, `\\?\UNC\`) {
			resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
		} else {
			resolved = strings.TrimPrefix(resolved, `\\?\`)
		}
		return resolved, nil
	}
}
