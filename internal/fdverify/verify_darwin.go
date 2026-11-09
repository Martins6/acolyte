//go:build darwin

package fdverify

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const maxPath = 1024

func verifyFileOpen(canonical string, before os.FileInfo) error {
	buf := make([]byte, maxPath)
	for fd := 0; fd < fdLimit(); fd++ {
		for i := range buf {
			buf[i] = 0
		}
		_, _, errno := unix.Syscall(
			unix.SYS_FCNTL,
			uintptr(fd),
			uintptr(unix.F_GETPATH),
			uintptr(unsafe.Pointer(&buf[0])),
		)
		if errno != 0 {
			continue
		}
		if filepath.Clean(string(buf[:nulIndex(buf)])) != canonical {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			continue
		}
		if sameFile(&stat, before) {
			return nil
		}
	}
	return fmt.Errorf("fdverify: %s is not open with the expected identity", canonical)
}

func fdLimit() int {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err == nil {
		if limit.Cur != unix.RLIM_INFINITY && limit.Cur < 4096 {
			return int(limit.Cur)
		}
	}
	return 4096
}

func nulIndex(buf []byte) int {
	for i, value := range buf {
		if value == 0 {
			return i
		}
	}
	return len(buf)
}

func sameFile(stat *unix.Stat_t, before os.FileInfo) bool {
	expected, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Dev == expected.Dev && stat.Ino == expected.Ino
}
