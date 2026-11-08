//go:build !darwin && !linux

package fdverify

import (
	"fmt"
	"os"
)

// Unsupported platforms fail closed rather than silently trusting a path.
func verifyFileOpen(string, os.FileInfo) error {
	return fmt.Errorf("fdverify: unsupported platform")
}
