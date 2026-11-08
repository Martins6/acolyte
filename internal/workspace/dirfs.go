package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	"golang.org/x/sys/unix"
)

// dirFS performs workspace file operations relative to an open directory
// descriptor. Every component is opened with O_NOFOLLOW, so a concurrent
// swap of an ancestor or leaf to a symlink fails instead of escaping.
type dirFS struct {
	root *os.File
}

func openDirFS(workspace string, create bool) (*dirFS, error) {
	abs, err := paths.Abs(workspace)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace: refusing symlinked workspace %s", abs)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace: stat %s: %w", abs, err)
	}

	// Resolve existing ancestors (system paths such as /var are symlinks),
	// then traverse/create the canonical path with O_NOFOLLOW at every step.
	canonical, err := paths.Canonical(abs)
	if err != nil {
		return nil, err
	}
	dir, err := openDirPathCreate(canonical, create)
	if err != nil {
		return nil, err
	}
	return &dirFS{root: dir}, nil
}

func openDirPathCreate(path string, create bool) (*os.File, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("workspace: %s is not absolute", path)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("workspace: open root: %w", err)
	}
	current := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := openAtDir(current, part)
		if errors.Is(err, unix.ENOENT) && create {
			if mkErr := unix.Mkdirat(int(current.Fd()), part, 0755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				current.Close()
				return nil, fmt.Errorf("workspace: create %s: %w", part, mkErr)
			}
			next, err = openAtDir(current, part)
		}
		if err != nil {
			current.Close()
			return nil, fmt.Errorf("workspace: open %s: %w", part, err)
		}
		current.Close()
		current = next
	}
	return current, nil
}

func (d *dirFS) Close() error {
	if d == nil || d.root == nil {
		return nil
	}
	return d.root.Close()
}

// walkDir opens rel without creating anything. The caller closes the result.
func (d *dirFS) walkDir(rel string) (*os.File, error) {
	rel, err := safeRel(rel, true)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		fd, err := unix.Dup(int(d.root.Fd()))
		if err != nil {
			return nil, fmt.Errorf("workspace: duplicate root: %w", err)
		}
		return os.NewFile(uintptr(fd), "."), nil
	}

	current := d.root
	owned := false
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := openAtDir(current, part)
		if err != nil {
			if owned {
				current.Close()
			}
			return nil, err
		}
		if owned {
			current.Close()
		}
		current = next
		owned = true
	}
	return current, nil
}

// ensureDir opens rel, creating missing components. The caller closes the
// result.
func (d *dirFS) ensureDir(rel string) (*os.File, error) {
	rel, err := safeRel(rel, true)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return d.walkDir(".")
	}

	current := d.root
	owned := false
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, err := openAtDir(current, part)
		if errors.Is(err, unix.ENOENT) {
			if mkErr := unix.Mkdirat(int(current.Fd()), part, 0755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				if owned {
					current.Close()
				}
				return nil, fmt.Errorf("workspace: create %s: %w", part, mkErr)
			}
			next, err = openAtDir(current, part)
		}
		if err != nil {
			if owned {
				current.Close()
			}
			return nil, fmt.Errorf("workspace: open %s: %w", part, err)
		}
		if owned {
			current.Close()
		}
		current = next
		owned = true
	}
	return current, nil
}

func openAtDir(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(
		int(dir.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		var stat unix.Stat_t
		if statErr := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && isSymlink(stat) {
			return nil, fmt.Errorf("workspace: refusing symlinked path component %s", name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// statNoFollow checks a path component-by-component without following symlinks.
func (d *dirFS) statNoFollow(rel string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	rel, err := safeRel(rel, false)
	if err != nil {
		return stat, err
	}
	dirRel := filepath.Dir(rel)
	base := filepath.Base(rel)
	dir, err := d.walkDir(dirRel)
	if err != nil {
		return stat, err
	}
	defer dir.Close()
	if err := unix.Fstatat(int(dir.Fd()), base, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return stat, err
	}
	return stat, nil
}

func (d *dirFS) readFile(rel string) ([]byte, error) {
	rel, err := safeRel(rel, false)
	if err != nil {
		return nil, err
	}
	dir, err := d.walkDir(filepath.Dir(rel))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), filepath.Base(rel), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if stat, statErr := d.statNoFollow(rel); statErr == nil && isSymlink(stat) {
			return nil, fmt.Errorf("workspace: refusing symlink %s", rel)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), rel)
	defer file.Close()
	return io.ReadAll(file)
}

func (d *dirFS) createFileIfMissing(rel, content string) error {
	rel, err := safeRel(rel, false)
	if err != nil {
		return err
	}
	dir, err := d.ensureDir(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()

	fd, err := unix.Openat(
		int(dir.Fd()),
		filepath.Base(rel),
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0644,
	)
	if errors.Is(err, unix.EEXIST) {
		stat, statErr := d.statNoFollow(rel)
		if statErr != nil {
			return statErr
		}
		if isSymlink(stat) {
			return fmt.Errorf("workspace: refusing to overwrite symlink %s", rel)
		}
		if !isRegular(stat) {
			return fmt.Errorf("workspace: %s is not a regular file", rel)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: create %s: %w", rel, err)
	}
	file := os.NewFile(uintptr(fd), rel)
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return fmt.Errorf("workspace: write %s: %w", rel, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", rel, err)
	}
	return nil
}

func (d *dirFS) appendFile(rel, content string) error {
	rel, err := safeRel(rel, false)
	if err != nil {
		return err
	}
	dir, err := d.walkDir(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()

	fd, err := unix.Openat(
		int(dir.Fd()),
		filepath.Base(rel),
		unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		if stat, statErr := d.statNoFollow(rel); statErr == nil && isSymlink(stat) {
			return fmt.Errorf("workspace: refusing symlink %s", rel)
		}
		return fmt.Errorf("workspace: open %s: %w", rel, err)
	}
	file := os.NewFile(uintptr(fd), rel)
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return fmt.Errorf("workspace: append %s: %w", rel, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", rel, err)
	}
	return nil
}

func isDir(stat unix.Stat_t) bool     { return stat.Mode&unix.S_IFMT == unix.S_IFDIR }
func isRegular(stat unix.Stat_t) bool { return stat.Mode&unix.S_IFMT == unix.S_IFREG }
func isSymlink(stat unix.Stat_t) bool { return stat.Mode&unix.S_IFMT == unix.S_IFLNK }
