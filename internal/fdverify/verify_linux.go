//go:build linux

package fdverify

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func verifyFileOpen(canonical string, before os.FileInfo) error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("fdverify: read /proc/self/fd: %w", err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		if filepath.Clean(target) != canonical {
			continue
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			continue
		}
		if sameFile(&stat, before) {
			return nil
		}
	}
	return fmt.Errorf("fdverify: %s is not open with the expected identity", canonical)
}

func sameFile(stat *syscall.Stat_t, before os.FileInfo) bool {
	expected, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Dev == expected.Dev && stat.Ino == expected.Ino
}
