package fdverify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	if err := VerifyOpen(path, info); err != nil {
		t.Fatalf("VerifyOpen() error: %v", err)
	}
}

func TestVerifyOpenWrongIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(other, []byte("y"), 0600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	otherInfo, err := os.Stat(other)
	if err != nil {
		t.Fatalf("stat other: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	if err := VerifyOpen(path, otherInfo); err == nil {
		t.Fatal("VerifyOpen(wrong identity) error = nil, want error")
	}
}

func TestVerifyOpenSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	file, err := os.Open(target)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer file.Close()

	if err := VerifyOpen(link, info); err != nil {
		t.Fatalf("VerifyOpen(symlink) error: %v", err)
	}
}
