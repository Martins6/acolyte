//go:build !darwin && !linux

package fdverify

import (
	"database/sql/driver"
	"fmt"
	"os"
)

// VerifyConn fails closed on unsupported platforms.
func VerifyConn(_ driver.Conn, _ string, _ os.FileInfo) error {
	return fmt.Errorf("fdverify: unsupported platform")
}
