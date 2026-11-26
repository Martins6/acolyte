package board

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBoardHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ACOLYTE_HOME", home)
	return home
}

func writeBoardFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	return resolved
}

func TestReferenceFile(t *testing.T) {
	testBoardHome(t)
	source := writeBoardFile(t, "chart.png", "image-bytes")

	file, err := ReferenceFile(source)
	if err != nil {
		t.Fatalf("ReferenceFile() error: %v", err)
	}
	if file.Mode != FileReference {
		t.Errorf("mode = %q, want reference", file.Mode)
	}
	if file.Path != source {
		t.Errorf("path = %q, want %q", file.Path, source)
	}
	if file.Name != "chart.png" || file.Size != int64(len("image-bytes")) {
		t.Errorf("file = %+v", file)
	}
	if file.SHA256 != "" {
		t.Errorf("reference sha256 = %q, want empty", file.SHA256)
	}
}

func TestCopyFile(t *testing.T) {
	home := testBoardHome(t)
	source := writeBoardFile(t, "chart.png", "image-bytes")

	file, err := CopyFile(source)
	if err != nil {
		t.Fatalf("CopyFile() error: %v", err)
	}
	if file.Mode != FileCopy {
		t.Errorf("mode = %q, want copy", file.Mode)
	}
	if file.SourcePath != source {
		t.Errorf("source path = %q, want %q", file.SourcePath, source)
	}
	if file.Name != "chart.png" || file.Size != int64(len("image-bytes")) {
		t.Errorf("file = %+v", file)
	}
	if !strings.HasPrefix(file.Path, filepath.Join(home, "board", "files")) {
		t.Errorf("path = %q, want under board/files", file.Path)
	}
	sum := sha256.Sum256([]byte("image-bytes"))
	wantHash := hex.EncodeToString(sum[:])
	if file.SHA256 != wantHash {
		t.Errorf("sha256 = %q, want %q", file.SHA256, wantHash)
	}
	if !strings.Contains(file.Path, wantHash) {
		t.Errorf("path = %q, want hash directory", file.Path)
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Errorf("copy content = %q", data)
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		t.Fatalf("stat copy: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(file.Path))
	if err != nil {
		t.Fatalf("stat copy dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Errorf("dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestCopyFileDeduplicates(t *testing.T) {
	testBoardHome(t)
	source := writeBoardFile(t, "same.txt", "same")
	first, err := CopyFile(source)
	if err != nil {
		t.Fatalf("CopyFile() error: %v", err)
	}
	second, err := CopyFile(source)
	if err != nil {
		t.Fatalf("CopyFile() error: %v", err)
	}
	if first.Path != second.Path {
		t.Errorf("paths differ: %q vs %q", first.Path, second.Path)
	}
}

func TestFileRejectsInvalid(t *testing.T) {
	testBoardHome(t)
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "missing", path: filepath.Join(t.TempDir(), "nope")},
		{name: "directory", path: dir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReferenceFile(tt.path); err == nil {
				t.Fatal("ReferenceFile() succeeded, want error")
			}
			if _, err := CopyFile(tt.path); err == nil {
				t.Fatal("CopyFile() succeeded, want error")
			}
		})
	}
}

func TestFileSizeLimit(t *testing.T) {
	testBoardHome(t)
	old := maxFileSize
	maxFileSize = 4
	t.Cleanup(func() { maxFileSize = old })

	source := writeBoardFile(t, "big.txt", "too large")
	if _, err := ReferenceFile(source); err == nil {
		t.Fatal("ReferenceFile() succeeded, want size error")
	}
	if _, err := CopyFile(source); err == nil {
		t.Fatal("CopyFile() succeeded, want size error")
	}
}

func TestReferenceFileSymlink(t *testing.T) {
	testBoardHome(t)
	source := writeBoardFile(t, "target.txt", "target")
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file, err := ReferenceFile(link)
	if err != nil {
		t.Fatalf("ReferenceFile() error: %v", err)
	}
	if file.Path != source {
		t.Errorf("path = %q, want resolved %q", file.Path, source)
	}
}
