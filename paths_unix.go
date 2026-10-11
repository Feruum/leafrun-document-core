//go:build !windows

package documentcore

import "path/filepath"

func resolvePath(path string) (string, error) { return filepath.EvalSymlinks(path) }
