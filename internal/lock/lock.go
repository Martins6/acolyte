//go:build darwin || linux

// Package lock serializes per-workspace create/adopt operations across
// processes with an advisory flock.
package lock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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

// AcquireSession blocks until the per-session turn lock is held or ctx is
// done. The advisory flock is shared by every process using the same
// $ACOLYTE_HOME, so CLI turns and daemon turns cannot interleave on one
// OpenCode session.
func AcquireSession(ctx context.Context, sessionID string) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := SessionPath(sessionID)
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
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, fmt.Errorf("lock: acquire %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, fmt.Errorf("lock: acquire %s: %w", path, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
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
