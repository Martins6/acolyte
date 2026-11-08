package paths

import (
	"path/filepath"
	"testing"
)

func TestHomeDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(HomeEnv, "")

	got, err := Home()
	if err != nil {
		t.Fatalf("Home() error: %v", err)
	}
	want := filepath.Join(home, ".acolyte")
	if got != want {
		t.Errorf("Home() = %q, want %q", got, want)
	}
}

func TestHomeOverrideAndTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(HomeEnv, "~/custom")

	got, err := Home()
	if err != nil {
		t.Fatalf("Home() error: %v", err)
	}
	want := filepath.Join(home, "custom")
	if got != want {
		t.Errorf("Home() = %q, want %q", got, want)
	}
}

func TestGlobalPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv(HomeEnv, home)

	registry, err := RegistryDB()
	if err != nil {
		t.Fatalf("RegistryDB() error: %v", err)
	}
	if want := filepath.Join(home, "registry.sqlite.db"); registry != want {
		t.Errorf("RegistryDB() = %q, want %q", registry, want)
	}

	env, err := GlobalEnv()
	if err != nil {
		t.Fatalf("GlobalEnv() error: %v", err)
	}
	if want := filepath.Join(home, ".env"); env != want {
		t.Errorf("GlobalEnv() = %q, want %q", env, want)
	}
}

func TestWorkspacePaths(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if want := filepath.Join(ws, ".acolyte.sqlite.db"); WorkspaceDB(ws) != want {
		t.Errorf("WorkspaceDB() = %q, want %q", WorkspaceDB(ws), want)
	}
	if want := filepath.Join(ws, ".env"); WorkspaceEnv(ws) != want {
		t.Errorf("WorkspaceEnv() = %q, want %q", WorkspaceEnv(ws), want)
	}
}

func TestResolveWorkspacePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	flagDir := filepath.Join(home, "flag")
	envDir := filepath.Join(home, "env")

	t.Setenv(WorkspaceEnvVar, envDir)

	got, err := ResolveWorkspace(flagDir)
	if err != nil {
		t.Fatalf("ResolveWorkspace(flag) error: %v", err)
	}
	if got != flagDir {
		t.Errorf("ResolveWorkspace(flag) = %q, want %q", got, flagDir)
	}

	got, err = ResolveWorkspace("")
	if err != nil {
		t.Fatalf("ResolveWorkspace(env) error: %v", err)
	}
	if got != envDir {
		t.Errorf("ResolveWorkspace(env) = %q, want %q", got, envDir)
	}
}

func TestAbsRejectsEmpty(t *testing.T) {
	if _, err := Abs("  "); err == nil {
		t.Fatal("Abs(empty) error = nil, want error")
	}
}

func TestAbsCleans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := Abs("~/a/../b")
	if err != nil {
		t.Fatalf("Abs() error: %v", err)
	}
	if want := filepath.Join(home, "b"); got != want {
		t.Errorf("Abs() = %q, want %q", got, want)
	}
}
