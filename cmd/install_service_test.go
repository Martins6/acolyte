package cmd

import (
	"bytes"
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
