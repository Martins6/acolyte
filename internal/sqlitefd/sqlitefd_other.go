//go:build !darwin && !linux

package sqlitefd

import (
	"database/sql/driver"
	"fmt"
)

// MainFDFromConn is unavailable without the unix SQLite VFS.
func MainFDFromConn(driver.Conn) (int, error) {
	return -1, fmt.Errorf("sqlitefd: unsupported platform")
}
