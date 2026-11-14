package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteUnit writes content to path through a temp file in the same directory,
// so a crash cannot leave a half-written service definition. The resulting
// file is world-readable (0644), as launchd and systemd expect.
func WriteUnit(path, content string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("service: unit path %q must be absolute", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("service: create unit directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("service: create temp unit in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	if _, err := tmp.WriteString(content); err != nil {
		cleanup()
		return fmt.Errorf("service: write unit: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("service: set unit mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("service: close unit: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("service: install unit at %q: %w", path, err)
	}
	return nil
}
