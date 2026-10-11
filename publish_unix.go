//go:build !windows

package documentcore

import "os"

func publishPDF(staging, output string) error { return os.Rename(staging, output) }
