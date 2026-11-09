package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
)

func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	t.Setenv("HOME", filepath.Dir(home))
	return home
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func writeEnv(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	testHome(t)

	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if _, ok := s.Lookup("ACOLYTE_TEST_MISSING_KEY"); ok {
		t.Fatal("Lookup() found a key in missing files")
	}
}

func TestWorkspaceOverridesGlobal(t *testing.T) {
	home := testHome(t)
	ws := t.TempDir()
	writeEnv(t, filepath.Join(home, ".env"), "ACOLYTE_TEST_LAYER=global\n")
	writeEnv(t, filepath.Join(ws, ".env"), "ACOLYTE_TEST_LAYER=workspace\n")
	unsetEnv(t, "ACOLYTE_TEST_LAYER")

	s, err := Load(ws)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got, ok := s.Lookup("ACOLYTE_TEST_LAYER")
	if !ok {
		t.Fatal("Lookup() did not find key")
	}
	if got != "workspace" {
		t.Errorf("Lookup() = %q, want workspace", got)
	}
}

func TestProcessEnvWins(t *testing.T) {
	home := testHome(t)
	ws := t.TempDir()
	writeEnv(t, filepath.Join(home, ".env"), "ACOLYTE_TEST_PROCESS=global\n")
	writeEnv(t, filepath.Join(ws, ".env"), "ACOLYTE_TEST_PROCESS=workspace\n")
	t.Setenv("ACOLYTE_TEST_PROCESS", "process")

	s, err := Load(ws)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got, _ := s.Lookup("ACOLYTE_TEST_PROCESS")
	if got != "process" {
		t.Errorf("Lookup() = %q, want process", got)
	}
}

func TestGlobalOnly(t *testing.T) {
	home := testHome(t)
	writeEnv(t, filepath.Join(home, ".env"), "ACOLYTE_TEST_GLOBAL=global\n")

	s, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got, ok := s.Lookup("ACOLYTE_TEST_GLOBAL")
	if !ok || got != "global" {
		t.Errorf("Lookup() = %q, %v; want global, true", got, ok)
	}
}

func TestWorkspaceValuesDoNotMutateProcessEnv(t *testing.T) {
	testHome(t)
	ws := t.TempDir()
	writeEnv(t, filepath.Join(ws, ".env"), "ACOLYTE_TEST_SCOPED=workspace\n")

	s, err := Load(ws)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := os.Getenv("ACOLYTE_TEST_SCOPED"); got != "" {
		t.Errorf("workspace value leaked into process env: %q", got)
	}
	if got, _ := s.Lookup("ACOLYTE_TEST_SCOPED"); got != "workspace" {
		t.Errorf("Lookup() = %q, want workspace", got)
	}
}

func TestEnviron(t *testing.T) {
	home := testHome(t)
	ws := t.TempDir()
	writeEnv(t, filepath.Join(home, ".env"), "ACOLYTE_TEST_ENV_GLOBAL=g\n")
	writeEnv(t, filepath.Join(ws, ".env"), "ACOLYTE_TEST_ENV_WS=w\n")
	t.Setenv("ACOLYTE_TEST_ENV_PROCESS", "p")

	s, err := Load(ws)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	env := s.Environ()
	present := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := cut(entry)
		if ok {
			present[key] = value
		}
	}
	for key, want := range map[string]string{
		"ACOLYTE_TEST_ENV_GLOBAL":  "g",
		"ACOLYTE_TEST_ENV_WS":      "w",
		"ACOLYTE_TEST_ENV_PROCESS": "p",
	} {
		if got := present[key]; got != want {
			t.Errorf("Environ()[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestMalformedFile(t *testing.T) {
	home := testHome(t)
	writeEnv(t, filepath.Join(home, ".env"), "ACOLYTE_TEST_BAD=\"unterminated\n")

	if _, err := Load(""); err == nil {
		t.Fatal("Load() error = nil, want error for malformed file")
	}
}

func TestScopedGetters(t *testing.T) {
	home := testHome(t)
	ws := t.TempDir()
	writeEnv(t, filepath.Join(home, ".env"), "GLOBAL_ONLY=g\nSHARED=global\n")
	writeEnv(t, filepath.Join(ws, ".env"), "WS_ONLY=w\nSHARED=workspace\n")

	s, err := Load(ws)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := s.Global()["GLOBAL_ONLY"]; got != "g" {
		t.Errorf("Global()[GLOBAL_ONLY] = %q, want g", got)
	}
	if got := s.Workspace()["WS_ONLY"]; got != "w" {
		t.Errorf("Workspace()[WS_ONLY] = %q, want w", got)
	}
	if got := s.Workspace()["SHARED"]; got != "workspace" {
		t.Errorf("Workspace()[SHARED] = %q, want workspace", got)
	}
}

func cut(entry string) (string, string, bool) {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], true
		}
	}
	return "", "", false
}
