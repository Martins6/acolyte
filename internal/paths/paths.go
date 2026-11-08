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
