//go:build darwin || linux

// Package lock serializes per-workspace create/adopt operations across
// processes with an advisory flock.
package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/martins6/acolyte/internal/paths"
	"golang.org/x/sys/unix"
)

// Lock is a held workspace lock.
type Lock struct {
	file *os.File
}

// Path returns the lock file for a canonical workspace.
func Path(workspace string) (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(workspace))
	return filepath.Join(home, "locks", hex.EncodeToString(sum[:])+".lock"), nil
}

// Acquire blocks until the workspace lock is held.
func Acquire(workspace string) (*Lock, error) {
	path, err := Path(workspace)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("lock: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock: acquire %s: %w", path, err)
	}
	return &Lock{file: file}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
