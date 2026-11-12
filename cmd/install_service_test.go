package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/service"
)

func resetInstallServiceFlags(t *testing.T) {
	t.Helper()
	oldUser, oldSystem, oldPrint := installServiceUser, installServiceSystem, installServicePrint
	t.Cleanup(func() {
		installServiceUser, installServiceSystem, installServicePrint = oldUser, oldSystem, oldPrint
	})
}

func TestResolveInstallScopeConflict(t *testing.T) {
	resetInstallServiceFlags(t)
	installServiceUser = true
	installServiceSystem = true

	if _, err := resolveInstallScope(); err == nil {
		t.Fatal("expected error when both --user and --system are set")
	}
}

func TestResolveInstallScopeDefaultsToUser(t *testing.T) {
	resetInstallServiceFlags(t)

	scope, err := resolveInstallScope()
	if err != nil {
		t.Fatal(err)
	}
	if scope != service.ScopeUser {
		t.Fatalf("scope = %q, want %q", scope, service.ScopeUser)
	}
}

func TestInstallServicePrint(t *testing.T) {
	resetInstallServiceFlags(t)
	installServicePrint = true

	var buf bytes.Buffer
	installServiceCmd.SetOut(&buf)
	t.Cleanup(func() { installServiceCmd.SetOut(nil) })

	if err := installServiceCmd.RunE(installServiceCmd, nil); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "# 1. Write this file to") {
		t.Fatalf("print output missing unit instructions:\n%s", out)
	}
	if strings.Contains(out, "Installed acolyte service.") {
		t.Fatalf("print must not report an install:\n%s", out)
	}
}

func TestIsStrictSubdir(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
		want bool
	}{
		{"child", "/Users/alice", "/Users/alice/.acolyte", true},
		{"nested child", "/Users/alice", "/Users/alice/agents/valet", true},
		{"same directory", "/Users/alice", "/Users/alice", false},
		{"parent", "/Users/alice", "/Users", false},
		{"sibling prefix", "/Users/alice", "/Users/alice2", false},
		{"traversal", "/Users/alice", "/Users/alice/../etc", false},
		{"outside", "/Users/alice", "/etc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStrictSubdir(tt.base, tt.path); got != tt.want {
				t.Fatalf("isStrictSubdir(%q, %q) = %t, want %t", tt.base, tt.path, got, tt.want)
			}
		})
	}
}

func TestPrepareServiceDirsCreatesHome(t *testing.T) {
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".acolyte")
	opts := service.Options{
		GOOS:     "linux",
		Scope:    service.ScopeUser,
		Home:     home,
		UserHome: userHome,
		UID:      os.Getuid(),
	}

	if err := prepareServiceDirs(opts); err != nil {
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

func TestPrepareServiceDirsRejectsFile(t *testing.T) {
	userHome := t.TempDir()
	path := filepath.Join(userHome, "not-a-directory")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := service.Options{
		GOOS:     "linux",
		Scope:    service.ScopeUser,
		Home:     path,
		UserHome: userHome,
		UID:      os.Getuid(),
	}

	if err := prepareServiceDirs(opts); err == nil {
		t.Fatal("expected error when ACOLYTE_HOME is a file")
	}
}
