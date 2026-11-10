package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/paths"
)

func TestAcquireRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	workspace := filepath.Join(t.TempDir(), "agent")

	path, err := Path(workspace)
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(home, "locks") {
		t.Errorf("lock directory = %q, want %q", filepath.Dir(path), filepath.Join(home, "locks"))
	}

	held, err := Acquire(workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("Release() error: %v", err)
	}
}

func TestAcquireSessionSerializesSameSession(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())

	held, err := AcquireSession(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("AcquireSession() error: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = held.Release()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = AcquireSession(ctx, "ses_test")
	if err == nil {
		t.Fatal("AcquireSession(same session) error = nil, want contention")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireSession(same session) error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("AcquireSession returned after %s, want it to wait", elapsed)
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release() error: %v", err)
	}
	released = true

	again, err := AcquireSession(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("AcquireSession(after release) error: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release(after) error: %v", err)
	}
}

func TestAcquireSessionDifferentSessionsDoNotBlock(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())

	first, err := AcquireSession(context.Background(), "ses_a")
	if err != nil {
		t.Fatalf("AcquireSession(ses_a) error: %v", err)
	}
	defer first.Release()

	second, err := AcquireSession(context.Background(), "ses_b")
	if err != nil {
		t.Fatalf("AcquireSession(ses_b) error: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release(ses_b) error: %v", err)
	}
}
