//go:build !darwin && !linux

package fdverify

import "os"

// Unsupported platforms fall back to the caller's pre/post path checks.
func verifyFileOpen(string, os.FileInfo) error { return nil }
