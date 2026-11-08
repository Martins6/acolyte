// Package paths centralizes Acolyte's global and per-agent filesystem layout.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// HomeEnv overrides the global Acolyte root (default ~/.acolyte).
	HomeEnv = "ACOLYTE_HOME"
	// WorkspaceEnvVar is the fallback workspace when a command has neither
	// --agent-name nor --workspace-folder.
	WorkspaceEnvVar = "ACOLYTE_WORKSPACE_PATH"
)

// Home returns the absolute global Acolyte root: $ACOLYTE_HOME or ~/.acolyte.
func Home() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(HomeEnv)); raw != "" {
		return Abs(raw)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".acolyte"), nil
}

// RegistryDB returns the global registry database path.
func RegistryDB() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "registry.sqlite.db"), nil
}

// GlobalEnv returns the global secrets file path.
func GlobalEnv() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".env"), nil
}

// WorkspaceDB returns the per-agent database path.
func WorkspaceDB(workspace string) string {
	return filepath.Join(workspace, ".acolyte.sqlite.db")
}

// WorkspaceEnv returns the per-agent secrets file path.
func WorkspaceEnv(workspace string) string {
	return filepath.Join(workspace, ".env")
}

// ResolveWorkspace resolves a workspace in the documented order:
// explicit flag, $ACOLYTE_WORKSPACE_PATH, then the current directory.
func ResolveWorkspace(flag string) (string, error) {
	raw := strings.TrimSpace(flag)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(WorkspaceEnvVar))
	}
	if raw == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("paths: resolve current directory: %w", err)
		}
		raw = cwd
	}
	return Abs(raw)
}

// Canonical returns an absolute path with symlinks resolved, including when
// the final component does not exist yet. It is used for workspace identity so
// two symlinked paths to the same folder cannot be registered separately.
func Canonical(path string) (string, error) {
	abs, err := Abs(path)
	if err != nil {
		return "", err
	}
	return canonicalAbs(abs)
}

func canonicalAbs(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("paths: resolve symlinks for %q: %w", abs, err)
	}

	// If the final component is a symlink whose target does not exist yet,
	// resolve the link itself rather than treating it as a normal basename.
	if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(abs)
		if readErr != nil {
			return "", fmt.Errorf("paths: read symlink %q: %w", abs, readErr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		return canonicalAbs(filepath.Clean(target))
	}

	parent := filepath.Dir(abs)
	if parent == abs {
		return "", fmt.Errorf("paths: cannot resolve %q", abs)
	}
	resolvedParent, err := canonicalAbs(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

// Abs expands a leading ~ and returns a clean absolute path.
func Abs(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("paths: empty path")
	}

	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("paths: make %q absolute: %w", path, err)
	}
	return filepath.Clean(abs), nil
}
