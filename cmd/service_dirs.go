package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/service"
	"golang.org/x/sys/unix"
)

// prepareServiceDirs creates ACOLYTE_HOME (and the macOS log directory) and,
// for a root system install, gives ownership to the target user.
//
// The root path walks from the user's home with directory file descriptors
// opened using O_NOFOLLOW, so the target user cannot race a symlink into place
// between the ownership check and the chown.
func prepareServiceDirs(opts service.Options) error {
	userHome, err := paths.Canonical(opts.UserHome)
	if err != nil {
		return err
	}
	home, err := paths.Canonical(opts.Home)
	if err != nil {
		return err
	}

	if os.Geteuid() != 0 || opts.UID == 0 {
		return prepareOwnedDirs(home, opts.GOOS)
	}
	return prepareRootDirs(userHome, home, opts)
}

// prepareOwnedDirs is the non-root path: no ownership changes are needed, so
// plain path-based creation is sufficient.
func prepareOwnedDirs(home, goos string) error {
	dirs := []string{home}
	if goos == "darwin" {
		dirs = append(dirs, filepath.Join(home, service.LogDir))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func prepareRootDirs(userHome, home string, opts service.Options) error {
	if !isStrictSubdir(userHome, home) {
		return fmt.Errorf("refusing to prepare ACOLYTE_HOME %q as root: it must be a dedicated directory inside %q; move it under your home or run without --system", opts.Home, opts.UserHome)
	}
	rel, err := filepath.Rel(userHome, home)
	if err != nil {
		return fmt.Errorf("resolve ACOLYTE_HOME relative to %q: %w", userHome, err)
	}

	rootFd, err := openDirNoFollow(userHome)
	if err != nil {
		return fmt.Errorf("open user home %s: %w", userHome, err)
	}
	defer unix.Close(rootFd)

	current := rootFd
	currentPath := userHome
	for _, name := range strings.Split(rel, string(os.PathSeparator)) {
		if name == "" {
			continue
		}
		if name == "." || name == ".." {
			if current != rootFd {
				unix.Close(current)
			}
			return fmt.Errorf("refusing unsafe path component %q", name)
		}

		currentPath = filepath.Join(currentPath, name)
		next, err := ensureDirAt(current, name, currentPath, opts.UID)
		if err != nil {
			if current != rootFd {
				unix.Close(current)
			}
			return err
		}
		if current != rootFd {
			unix.Close(current)
		}
		current = next
	}

	if opts.GOOS == "darwin" {
		logPath := filepath.Join(currentPath, service.LogDir)
		logFd, err := ensureDirAt(current, service.LogDir, logPath, opts.UID)
		if err != nil {
			if current != rootFd {
				unix.Close(current)
			}
			return err
		}
		unix.Close(logFd)
	}

	if current != rootFd {
		unix.Close(current)
	}
	return nil
}

// ensureDirAt opens name below parent without following symlinks, creating it
// when missing, and gives a newly created directory to uid. Pre-existing
// directories owned by another user are never chowned.
func ensureDirAt(parent int, name, displayPath string, uid int) (int, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

	fd, err := unix.Openat(parent, name, flags, 0)
	created := false
	if errors.Is(err, unix.ENOENT) {
		mkErr := unix.Mkdirat(parent, name, 0o755)
		if mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
			return -1, fmt.Errorf("create %s: %w", displayPath, mkErr)
		}
		created = mkErr == nil
		fd, err = unix.Openat(parent, name, flags, 0)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return -1, fmt.Errorf("refusing symlink at %s", displayPath)
		}
		return -1, fmt.Errorf("open %s: %w", displayPath, err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("stat %s: %w", displayPath, err)
	}

	if int(stat.Uid) != uid {
		if !created {
			unix.Close(fd)
			return -1, fmt.Errorf("refusing to change ownership of existing directory %q; run: sudo chown %d %q", displayPath, uid, displayPath)
		}
		if int(stat.Uid) != 0 {
			unix.Close(fd)
			return -1, fmt.Errorf("refusing to change ownership of %q: unexpected owner %d", displayPath, stat.Uid)
		}
		if err := unix.Fchown(fd, uid, -1); err != nil {
			unix.Close(fd)
			return -1, fmt.Errorf("chown %s: %w", displayPath, err)
		}
	}
	return fd, nil
}

func openDirNoFollow(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// isStrictSubdir reports whether path is a dedicated directory below base.
func isStrictSubdir(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
