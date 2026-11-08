//go:build !darwin && !linux

package fdverify

import (
	"database/sql/driver"
	"os"
)

// VerifyConn falls back to the path scan on unsupported platforms.
func VerifyConn(_ driver.Conn, path string, before os.FileInfo) error {
	return VerifyOpen(path, before)
}
