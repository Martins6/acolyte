//go:build e2e

// Package e2e exercises the built acolyte binary against a real OpenCode
// service. It is opt-in:
//
//	ACOLYTE_E2E=1 ACOLYTE_E2E_MODEL=opencode-go/gpt-6-luna \
//	  go test -tags=e2e ./e2e/... -v
//
// The suite is deliberately small: two live turns (fresh + continue) and one
// zero-cost failure case. It never uses Telegram, a bot token, or the TUI.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/store"
)

var binaryPath string

func TestMain(m *testing.M) {
	if os.Getenv("ACOLYTE_E2E") != "1" {
		fmt.Fprintln(os.Stderr, "skipping live e2e: set ACOLYTE_E2E=1")
		os.Exit(0)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: resolve working directory: %v\n", err)
		os.Exit(1)
	}
	repoRoot := filepath.Dir(cwd)

	dir, err := os.MkdirTemp("", "acolyte-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create temp dir: %v\n", err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(dir, "acolyte")

	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build acolyte: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type cliRunResult struct {
	Agent      string   `json:"agent"`
	Workspace  string   `json:"workspace"`
	Thread     string   `json:"thread"`
	SessionID  string   `json:"session_id"`
	InputID    string   `json:"input_id"`
	MessageIDs []string `json:"message_ids"`
	Reply      string   `json:"reply"`
	Error      string   `json:"error"`
	DurationMS int64    `json:"duration_ms"`
}

type fixture struct {
	home string
	ws   string
}

// newFixture registers a fresh agent with no Telegram channel. The absence of
// a token, a chat, and channels is the point: run must work anyway.
func newFixture(t *testing.T) fixture {
	t.Helper()

	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "agent")
	model := strings.TrimSpace(os.Getenv("ACOLYTE_E2E_MODEL"))
	if model == "" {
		model = "opencode-go/gpt-6-luna"
	}
	config := fmt.Sprintf(`{"engine":{"model":%q,"agent":"build"},"communication":{"channels":[]}}`, model)

	stdout, stderr, code := runCLI(t, home, "new",
		"--name", "e2e",
		"--workspace-folder", ws,
		"--config", config,
	)
	if code != 0 {
		t.Fatalf("acolyte new failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(ws, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace .env exists or could not be checked: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "opencode.jsonc"))
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	if !strings.Contains(string(raw), model) {
		t.Fatalf("opencode.jsonc does not contain model %q:\n%s", model, raw)
	}
	return fixture{home: home, ws: ws}
}

func TestRunFreshAndContinueWithoutTelegram(t *testing.T) {
	f := newFixture(t)

	first := runJSON(t, f.home, "run",
		"Reply with exactly: ACOLYTE-E2E-OK",
		"--agent-name", "e2e",
		"--json",
	)
	if first.Error != "" {
		t.Fatalf("first run error: %s", first.Error)
	}
	if first.SessionID == "" {
		t.Fatal("first run returned no session id")
	}
	if first.Thread != "cli:e2e" {
		t.Fatalf("thread = %q, want cli:e2e", first.Thread)
	}
	if !strings.Contains(first.Reply, "ACOLYTE-E2E-OK") {
		t.Fatalf("first reply = %q, want canary", first.Reply)
	}

	second := runJSON(t, f.home, "run",
		"Reply with exactly: ACOLYTE-E2E-OK-2",
		"--agent-name", "e2e",
		"--continue",
		"--json",
	)
	if second.Error != "" {
		t.Fatalf("second run error: %s", second.Error)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("second session = %q, want %q (--continue must reuse the CLI session)", second.SessionID, first.SessionID)
	}
	if !strings.Contains(second.Reply, "ACOLYTE-E2E-OK-2") {
		t.Fatalf("second reply = %q, want canary", second.Reply)
	}

	inspectOut, inspectErr, code := runCLI(t, f.home, "inspect",
		"--session-id", first.SessionID,
		"--agent-name", "e2e",
		"--no-comm",
	)
	if code != 0 {
		t.Fatalf("inspect failed (%d)\nstdout:\n%s\nstderr:\n%s", code, inspectOut, inspectErr)
	}
	for _, want := range []string{"ACOLYTE-E2E-OK", "ACOLYTE-E2E-OK-2"} {
		if !strings.Contains(inspectOut, want) {
			t.Errorf("inspect output missing %q", want)
		}
	}

	st, err := store.Open(f.ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	session, ok, err := st.Orch().GetSessionByThread("cli:e2e")
	if err != nil || !ok || session.OpenCodeSessionID != first.SessionID {
		t.Fatalf("cli session = %+v, %v, %v; want %s", session, ok, err, first.SessionID)
	}
	outbox, err := st.CommTG().ListOutbox(0)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("outbox = %+v, want none (run must not deliver through Telegram)", outbox)
	}
}

func TestRunRejectsUnmappedSession(t *testing.T) {
	f := newFixture(t)

	stdout, stderr, code := runCLI(t, f.home, "run",
		"hello",
		"--agent-name", "e2e",
		"--session-id", "ses_missing",
		"--json",
	)
	if code == 0 {
		t.Fatalf("run with unmapped session succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	var result cliRunResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("json output: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(result.Error, "not mapped") {
		t.Fatalf("json error = %q, want not mapped", result.Error)
	}
	if result.SessionID != "" {
		t.Errorf("session id = %q, want empty for an unmapped session", result.SessionID)
	}
	if !strings.Contains(stderr, "not mapped") {
		t.Errorf("stderr = %q, want the ownership error", stderr)
	}
}

func runJSON(t *testing.T, home string, args ...string) cliRunResult {
	t.Helper()
	stdout, stderr, code := runCLI(t, home, args...)
	if code != 0 {
		t.Fatalf("command failed (%d): %v\nstdout:\n%s\nstderr:\n%s", code, args, stdout, stderr)
	}
	var result cliRunResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("json output: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return result
}

func runCLI(t *testing.T, home string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Env = commandEnv(home)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("command timed out: %v", args)
	}
	if err == nil {
		return out.String(), errOut.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.String(), errOut.String(), exitErr.ExitCode()
	}
	t.Fatalf("run %v: %v", args, err)
	return "", "", -1
}

// commandEnv removes Telegram and Acolyte workspace overrides so a developer
// shell cannot accidentally satisfy the test through a real channel.
func commandEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "ACOLYTE_HOME", "ACOLYTE_WORKSPACE_PATH", "TELEGRAM_BOT_TOKEN", "ACOLYTE_TELEGRAM_API_URL":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "ACOLYTE_HOME="+home)
}
