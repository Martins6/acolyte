package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
)

func TestNotifyUserValidation(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())
	if err := runNotifyUser(context.Background(), &bytes.Buffer{}, "  ", "", ""); err == nil {
		t.Fatal("notify-user(empty) error = nil, want error")
	}
	if err := runNotifyUser(context.Background(), &bytes.Buffer{}, "hello", "missing", ""); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("notify-user(missing agent) error = %v, want not registered", err)
	}
}

func TestInboxValidation(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())
	if err := runInbox(context.Background(), &bytes.Buffer{}, "  ", "valet", "", ""); err == nil {
		t.Fatal("inbox(empty) error = nil, want error")
	}
	if err := runInbox(context.Background(), &bytes.Buffer{}, "hello", "missing", "", ""); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("inbox(missing agent) error = %v, want not registered", err)
	}
}
