package board

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
)

// maxFileSize bounds one attachment. It is a var so tests can lower it.
var maxFileSize int64 = 100 << 20

// ReferenceFile records an attachment in place. The path is canonicalized
// without copying the file.
func ReferenceFile(raw string) (File, error) {
	source, info, err := resolveBoardFile(raw)
	if err != nil {
		return File{}, err
	}
	return File{
		Name: filepath.Base(source),
		Path: source,
		Mode: FileReference,
		Size: info.Size(),
	}, nil
}

// CopyFile copies an attachment into $ACOLYTE_HOME/board/files/<sha256>/ and
// returns the durable file record.
func CopyFile(raw string) (File, error) {
	source, _, err := resolveBoardFile(raw)
	if err != nil {
		return File{}, err
	}
	dir, err := paths.BoardFilesDir()
	if err != nil {
		return File{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return File{}, fmt.Errorf("board: create files directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(dir, ".incoming-")
	if err != nil {
		return File{}, fmt.Errorf("board: create temporary file: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpPath := filepath.Join(tmpDir, "file")

	src, err := os.Open(source)
	if err != nil {
		return File{}, fmt.Errorf("board: open %q: %w", source, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return File{}, fmt.Errorf("board: create temporary file: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(dst, hasher), io.LimitReader(src, maxFileSize+1))
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if copyErr != nil {
		return File{}, fmt.Errorf("board: copy %q: %w", source, copyErr)
	}
	if syncErr != nil {
		return File{}, fmt.Errorf("board: sync %q: %w", source, syncErr)
	}
	if closeErr != nil {
		return File{}, fmt.Errorf("board: close %q: %w", source, closeErr)
	}
	if written > maxFileSize {
		return File{}, fmt.Errorf("%w: %q is larger than %d MiB", ErrInvalid, source, maxFileSize>>20)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	destDir := filepath.Join(dir, sum)
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return File{}, fmt.Errorf("board: create attachment directory: %w", err)
	}
	dest := filepath.Join(destDir, filepath.Base(source))
	if err := os.Rename(tmpPath, dest); err != nil {
		return File{}, fmt.Errorf("board: store attachment: %w", err)
	}
	return File{
		Name:       filepath.Base(source),
		Path:       dest,
		Mode:       FileCopy,
		SourcePath: source,
		Size:       written,
		SHA256:     sum,
	}, nil
}

func resolveBoardFile(raw string) (string, os.FileInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, fmt.Errorf("%w: file path is required", ErrInvalid)
	}
	abs, err := paths.Abs(raw)
	if err != nil {
		return "", nil, fmt.Errorf("board: resolve file %q: %w", raw, err)
	}
	source, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, fmt.Errorf("board: resolve file %q: %w", raw, err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", nil, fmt.Errorf("board: stat file %q: %w", raw, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %q is not a regular file", ErrInvalid, raw)
	}
	if info.Size() > maxFileSize {
		return "", nil, fmt.Errorf("%w: %q is larger than %d MiB", ErrInvalid, raw, maxFileSize>>20)
	}
	return source, info, nil
}
