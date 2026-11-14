// Package logging provides the daemon's rotating file log.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultMaxSize is the size at which the active log file is rotated.
	DefaultMaxSize int64 = 10 << 20 // 10 MiB
	// DefaultBackups is the number of rotated files kept beside the active log.
	DefaultBackups = 3
)

// RotatingWriter is an io.WriteCloser that appends to path and rotates the
// file once the next write would exceed maxSize. Rotated files are named
// path.1 (newest) through path.<backups> (oldest).
type RotatingWriter struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
}

// Open opens (creating if needed) path for append and returns a writer that
// rotates it at maxSize. backups controls how many rotated files are kept.
func Open(path string, maxSize int64, backups int) (*RotatingWriter, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("logging: max size must be positive")
	}
	if backups < 0 {
		return nil, fmt.Errorf("logging: backups must not be negative")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logging: create log directory %q: %w", dir, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("logging: open log file %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("logging: stat log file %q: %w", path, err)
	}

	return &RotatingWriter{
		path:    path,
		maxSize: maxSize,
		backups: backups,
		file:    file,
		size:    info.Size(),
	}, nil
}

// Write appends p, rotating first when the active file is non-empty and the
// write would push it past maxSize. A single write larger than maxSize is
// always accepted so no log line is ever dropped.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the active log file. Writes after Close return os.ErrClosed.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("logging: close log file %q: %w", w.path, err)
	}
	w.file = nil

	if w.backups == 0 {
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: remove log file %q: %w", w.path, err)
		}
	} else {
		oldest := fmt.Sprintf("%s.%d", w.path, w.backups)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: remove old log %q: %w", oldest, err)
		}
		for i := w.backups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", w.path, i)
			dst := fmt.Sprintf("%s.%d", w.path, i+1)
			if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("logging: rotate %q to %q: %w", src, dst, err)
			}
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: rotate %q: %w", w.path, err)
		}
	}

	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logging: reopen log file %q: %w", w.path, err)
	}
	w.file = file
	w.size = 0
	return nil
}
