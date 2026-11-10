package opencode

import (
	"fmt"
	"os"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
)

// TestMain gives the package a private $ACOLYTE_HOME so per-session turn
// locks never touch the real user store.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acolyte-opencode-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "opencode tests: create temp home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv(paths.HomeEnv, dir); err != nil {
		fmt.Fprintf(os.Stderr, "opencode tests: set %s: %v\n", paths.HomeEnv, err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
