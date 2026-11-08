//go:build darwin || linux

package fdverify

import (
	"database/sql/driver"
	"fmt"
	"os"

	"github.com/martins6/acolyte/internal/sqlitefd"
	"golang.org/x/sys/unix"
)

// VerifyConn checks the exact main-database descriptor of a SQLite connection.
func VerifyConn(conn driver.Conn, path string, before os.FileInfo) error {
	fd, err := sqlitefd.MainFDFromConn(conn)
	if err != nil {
		return fmt.Errorf("fdverify: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fdverify: fstat %s: %w", path, err)
	}
	if !sameFile(&stat, before) {
		return fmt.Errorf("fdverify: %s connection opened a different file", path)
	}
	return nil
}
