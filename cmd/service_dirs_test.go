package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martins6/acolyte/internal/service"
	"golang.org/x/sys/unix"
)

func TestPrepareRootDirsCreatesNestedHome(t *testing.T) {
	userHome := t.TempDir()
	home := filepath.Join(userHome, "agents", "valet")
	opts := service.Options{
		GOOS:     "linux",
		Home:     home,
		UserHome: userHome,
		UID:      os.Getuid(),
	}

	if err := prepareRootDirs(userHome, home, opts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", home)
	}
}

func TestPrepareRootDirsRejectsOutsideHome(t *testing.T) {
	userHome := t.TempDir()
	opts := service.Options{
		GOOS:     "linux",
		Home:     "/etc",
		UserHome: userHome,
		UID:      os.Getuid(),
	}

	if err := prepareRootDirs(userHome, "/etc", opts); err == nil {
		t.Fatal("expected error for ACOLYTE_HOME outside the user home")
	}
}

func TestPrepareRootDirsRejectsSymlinkComponent(t *testing.T) {
	userHome := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(userHome, "agents")); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(userHome, "agents", "valet")
	opts := service.Options{
		GOOS:     "linux",
		Home:     home,
		UserHome: userHome,
		UID:      os.Getuid(),
	}

	if err := prepareRootDirs(userHome, home, opts); err == nil {
		t.Fatal("expected symlink component to be rejected")
	}
}

func TestEnsureDirAtCreates(t *testing.T) {
	parent := t.TempDir()
	parentFd, err := openDirNoFollow(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFd)

	child := filepath.Join(parent, "child")
	fd, err := ensureDirAt(parentFd, "child", child, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)

	info, err := os.Stat(child)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", child)
	}
}

func TestEnsureDirAtRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	parentFd, err := openDirNoFollow(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFd)

	link := filepath.Join(parent, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDirAt(parentFd, "link", link, os.Getuid()); err == nil {
		t.Fatal("expected symlink to be rejected")
	}
}
