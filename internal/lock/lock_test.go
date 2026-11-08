package lock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
)

func TestAcquireRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	workspace := filepath.Join(t.TempDir(), "agent")

	path, err := Path(workspace)
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(home, "locks") {
		t.Errorf("lock directory = %q, want %q", filepath.Dir(path), filepath.Join(home, "locks"))
	}

	held, err := Acquire(workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("Release() error: %v", err)
	}
}
