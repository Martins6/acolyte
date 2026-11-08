//go:build !darwin && !linux

package lock

import (
	"os"
	"path/filepath"

	"github.com/martins6/acolyte/internal/paths"
)

type Lock struct{ file *os.File }

func Path(workspace string) (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "locks", filepath.Base(workspace)+".lock"), nil
}

func Acquire(workspace string) (*Lock, error) {
	path, err := Path(workspace)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
