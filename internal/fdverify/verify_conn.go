//go:build darwin || linux

package fdverify

import (
	"database/sql/driver"
	"fmt"
	"os"
	"syscall"

	"github.com/martins6/acolyte/internal/sqlitefd"
)

// VerifyConn checks the exact main-database descriptor of a SQLite connection.
func VerifyConn(conn driver.Conn, path string, before os.FileInfo) error {
	fd, err := sqlitefd.MainFDFromConn(conn)
	if err != nil {
		return fmt.Errorf("fdverify: %w", err)
	}
	// sameFile is defined per platform with syscall.Stat_t. Use the same
	// type here so this file builds on both darwin and linux.
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fdverify: fstat %s: %w", path, err)
	}
	if !sameFile(&stat, before) {
		return fmt.Errorf("fdverify: %s connection opened a different file", path)
	}
	return nil
}
