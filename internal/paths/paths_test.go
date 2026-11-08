package paths

import (
	"os"
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

func TestCanonicalResolvesSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := Canonical(link)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("eval target: %v", err)
	}
	if got != want {
		t.Errorf("Canonical() = %q, want %q", got, want)
	}
}

func TestCanonicalMissingFinalComponent(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "not-created")

	got, err := Canonical(missing)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	parent, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("eval parent: %v", err)
	}
	want := filepath.Join(parent, "not-created")
	if got != want {
		t.Errorf("Canonical() = %q, want %q", got, want)
	}
}

func TestCanonicalSymlinkToMissingTarget(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "target-not-created")
	link := filepath.Join(home, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := Canonical(link)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	parent, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("eval parent: %v", err)
	}
	want := filepath.Join(parent, "target-not-created")
	if got != want {
		t.Errorf("Canonical() = %q, want %q", got, want)
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
