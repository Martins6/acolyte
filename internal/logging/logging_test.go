package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBelowThresholdDoesNotRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated file exists after a small write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("log content = %q, want %q", data, "hello")
	}
}

func TestWriteRotatesAtThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	first := strings.Repeat("a", 60)
	second := strings.Repeat("b", 60)
	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(second)); err != nil {
		t.Fatal(err)
	}

	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != second {
		t.Fatalf("active log = %q, want second write", active)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != first {
		t.Fatalf("rotated log = %q, want first write", rotated)
	}
}

func TestRotationKeepsConfiguredBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte("12345678")); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{path + ".1", path + ".2"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("backup %s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("backup %s exists beyond configured count", path+".3")
	}
}

func TestWriteLargerThanMaxIsNotDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	payload := strings.Repeat("x", 50)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != payload {
		t.Fatalf("log content length = %d, want %d", len(data), len(payload))
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "logs", "daemon.log")
	w, err := Open(path, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", filepath.Dir(path))
	}
}

func TestWriteAfterCloseReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late")); err != os.ErrClosed {
		t.Fatalf("write after close error = %v, want %v", err, os.ErrClosed)
	}
}

func TestOpenRejectsInvalidOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if _, err := Open(path, 0, 1); err == nil {
		t.Fatal("expected error for zero max size")
	}
	if _, err := Open(path, 100, -1); err == nil {
		t.Fatal("expected error for negative backups")
	}
}
