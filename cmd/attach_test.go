package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/store"
)

func TestAttachUsesLatestSessionAndAuthEnv(t *testing.T) {
	ws := setupCronCLI(t)
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_attach", time.Now().UTC()); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	st.Close()

	var capturedArgs []string
	var capturedEnv []string
	oldURL, oldPassword, oldRun := openCodeServiceURL, openCodeServicePassword, runAttachCommand
	openCodeServiceURL = func(context.Context) (string, error) { return "http://127.0.0.1:49374", nil }
	openCodeServicePassword = func() (string, error) { return "secret", nil }
	runAttachCommand = func(_ context.Context, args, env []string) error {
		capturedArgs = append([]string(nil), args...)
		capturedEnv = append([]string(nil), env...)
		return nil
	}
	t.Cleanup(func() {
		openCodeServiceURL, openCodeServicePassword, runAttachCommand = oldURL, oldPassword, oldRun
	})

	if err := runAttach(context.Background(), "valet", "", ""); err != nil {
		t.Fatalf("runAttach() error: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	want := []string{"attach", "http://127.0.0.1:49374", "--dir", canonical, "--session", "ses_attach"}
	if strings.Join(capturedArgs, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", capturedArgs, want)
	}
	env := strings.Join(capturedEnv, "\n")
	for _, want := range []string{"OPENCODE_SERVER_USERNAME=opencode", "OPENCODE_SERVER_PASSWORD=secret"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	if strings.Count(env, "OPENCODE_SERVER_PASSWORD=") != 1 {
		t.Errorf("env has duplicate password entries:\n%s", env)
	}
}

func TestAttachExplicitSession(t *testing.T) {
	setupCronCLI(t)
	var capturedArgs []string
	oldURL, oldPassword, oldRun := openCodeServiceURL, openCodeServicePassword, runAttachCommand
	openCodeServiceURL = func(context.Context) (string, error) { return "http://127.0.0.1:49374", nil }
	openCodeServicePassword = func() (string, error) { return "secret", nil }
	runAttachCommand = func(_ context.Context, args, _ []string) error {
		capturedArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() {
		openCodeServiceURL, openCodeServicePassword, runAttachCommand = oldURL, oldPassword, oldRun
	})

	if err := runAttach(context.Background(), "valet", "", "ses_explicit"); err != nil {
		t.Fatalf("runAttach() error: %v", err)
	}
	if !strings.Contains(strings.Join(capturedArgs, " "), "--session ses_explicit") {
		t.Errorf("args = %v, want explicit session", capturedArgs)
	}
}

func TestAttachEnvReplacesExistingValues(t *testing.T) {
	env := attachEnv([]string{"A=1", "OPENCODE_SERVER_PASSWORD=old", "OPENCODE_SERVER_USERNAME=old"}, "new")
	if len(env) != 3 || env[0] != "A=1" || env[1] != "OPENCODE_SERVER_USERNAME=opencode" || env[2] != "OPENCODE_SERVER_PASSWORD=new" {
		t.Errorf("env = %v", env)
	}
}
