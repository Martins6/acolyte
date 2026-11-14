package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteUnitCreatesParentAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "agent", "daemon.plist")
	if err := WriteUnit(path, "content\n"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content\n" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("mode = %o, want 644", perm)
	}
}

func TestWriteUnitOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acolyte.service")
	if err := WriteUnit(path, "one\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteUnit(path, "two\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestWriteUnitRejectsRelativePath(t *testing.T) {
	if err := WriteUnit("acolyte.service", "content"); err == nil {
		t.Fatal("expected error for relative path")
	}
}
