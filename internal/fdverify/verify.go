// Package fdverify pins the file that a library actually opened by pathname.
//
// SQLite (and other C libraries) open their files by name, so a pre/post
// Lstat pair cannot prove which inode the library used if the pathname is
// swapped in between. After the library opens the file, VerifyOpen scans the
// process file descriptors and checks that the expected canonical path is open
// with the same device/inode as the file that was inspected beforehand.
package fdverify

import (
	"fmt"
	"os"
	"path/filepath"
)

// VerifyOpen returns nil when the process has an open descriptor for path with
// the same device/inode as before.
func VerifyOpen(path string, before os.FileInfo) error {
	if before == nil {
		return fmt.Errorf("fdverify: missing expected file info for %s", path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("fdverify: resolve %s: %w", path, err)
	}
	return verifyFileOpen(filepath.Clean(canonical), before)
}
