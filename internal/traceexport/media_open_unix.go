//go:build unix

package traceexport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	"golang.org/x/sys/unix"
)

// openMediaSource opens rel under workspace for streaming without following a
// symlink in any path component. mediaRelPath has already enforced textual
// containment; this walk is the race-free filesystem enforcement. The opened
// descriptor is fstat'ed so the caller streams exactly the regular file that
// was validated, not a path that could change afterward.
func openMediaSource(workspace, rel string) (*os.File, error) {
	if rel == "" || rel == "." {
		return nil, fmt.Errorf("source is not a regular file")
	}
	rootPath, err := paths.Canonical(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	parent, err := openDirNoFollow(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()

	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return nil, fmt.Errorf("source path escapes the workspace")
		}
		next, err := unix.Openat(
			int(parent.Fd()),
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if err != nil {
			return nil, mediaOpenError(int(parent.Fd()), part, err)
		}
		nextFile := os.NewFile(uintptr(next), part)
		_ = parent.Close()
		parent = nextFile
	}

	base := parts[len(parts)-1]
	if base == "" || base == "." || base == ".." {
		return nil, fmt.Errorf("source is not a regular file")
	}
	fd, err := unix.Openat(
		int(parent.Fd()),
		base,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOCTTY,
		0,
	)
	if err != nil {
		return nil, mediaOpenError(int(parent.Fd()), base, err)
	}
	file := os.NewFile(uintptr(fd), rel)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat media %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("source is non-regular")
	}
	return file, nil
}

// openDirNoFollow opens an absolute directory path from the filesystem root,
// refusing every symlinked component. The caller closes the result.
func openDirNoFollow(dirPath string) (*os.File, error) {
	dirPath = filepath.Clean(dirPath)
	if !filepath.IsAbs(dirPath) {
		return nil, fmt.Errorf("workspace path is not absolute")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	current := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(dirPath, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(
			int(current.Fd()),
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("open workspace component %q: %w", part, err)
		}
		_ = current.Close()
		current = os.NewFile(uintptr(next), part)
	}
	return current, nil
}

// mediaOpenError classifies an Openat failure without weakening the
// no-symlink guarantee: Fstatat is used only to produce a clear warning.
func mediaOpenError(dirFD int, part string, err error) error {
	var stat unix.Stat_t
	if statErr := unix.Fstatat(dirFD, part, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil &&
		stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("source has a symlinked path component %q", part)
	}
	if errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("source file is missing")
	}
	if errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("source path component %q is not a directory", part)
	}
	return fmt.Errorf("open source component %q: %w", part, err)
}
