package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/store"
)

func canonicalWorkspace(t *testing.T, ws string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	return canonical
}

func seedCLISession(t *testing.T, ws, thread, sessionID string) {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if _, err := st.Orch().UpsertSession(thread, sessionID, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
}

func TestRunCreatesSessionAndPrintsReply(t *testing.T) {
	ws := setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out, errOut bytes.Buffer
	err := runRun(context.Background(), &out, &errOut, "  hello  ", runOptions{AgentName: "valet"})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	if fake.userCalls != 1 || fake.userSessionID != "" {
		t.Errorf("turn calls = %d, session = %q; want one new-session call", fake.userCalls, fake.userSessionID)
	}
	if fake.userText != "hello" {
		t.Errorf("text = %q, want hello", fake.userText)
	}
	if fake.userConfig.Workspace != canonicalWorkspace(t, ws) {
		t.Errorf("workspace = %q, want %q", fake.userConfig.Workspace, canonicalWorkspace(t, ws))
	}
	if fake.userConfig.Timeout != 0 {
		t.Errorf("timeout = %v, want no additional deadline", fake.userConfig.Timeout)
	}
	if out.String() != "reply\n" {
		t.Errorf("stdout = %q, want reply", out.String())
	}
	if !strings.Contains(errOut.String(), "session: ses_cli") {
		t.Errorf("stderr = %q, want session line", errOut.String())
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	session, ok, err := st.Orch().GetSessionByThread("cli:valet")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_cli" {
		t.Fatalf("cli session = %+v, %v, %v; want ses_cli", session, ok, err)
	}
	outbox, err := st.CommTG().ListOutbox(0)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("outbox = %+v, %v; want none", outbox, err)
	}
}

func TestRunContinuesCLISession(t *testing.T) {
	ws := setupCronCLI(t)
	seedCLISession(t, ws, "cli:valet", "ses_prev")
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out, errOut bytes.Buffer
	err := runRun(context.Background(), &out, &errOut, "again", runOptions{
		AgentName: "valet",
		Continue:  true,
	})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	if fake.userSessionID != "ses_prev" {
		t.Errorf("session = %q, want ses_prev", fake.userSessionID)
	}
}

func TestRunContinueWithoutSession(t *testing.T) {
	setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	err := runRun(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "again", runOptions{
		AgentName: "valet",
		Continue:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "no previous CLI run session") {
		t.Fatalf("runRun() error = %v, want no-previous-session", err)
	}
	if fake.userCalls != 0 {
		t.Errorf("turn calls = %d, want 0", fake.userCalls)
	}
}

func TestRunExplicitSession(t *testing.T) {
	ws := setupCronCLI(t)
	seedCLISession(t, ws, "telegram:10", "ses_explicit")
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out bytes.Buffer
	err := runRun(context.Background(), &out, &bytes.Buffer{}, "hello", runOptions{
		AgentName: "valet",
		SessionID: "ses_explicit",
	})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	if fake.userSessionID != "ses_explicit" {
		t.Errorf("session = %q, want ses_explicit", fake.userSessionID)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	session, ok, err := st.Orch().GetSessionByThread("cli:valet")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_explicit" {
		t.Fatalf("cli session = %+v, %v, %v; want ses_explicit", session, ok, err)
	}
}

func TestRunRejectsUnmappedSession(t *testing.T) {
	setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out bytes.Buffer
	err := runRun(context.Background(), &out, &bytes.Buffer{}, "hello", runOptions{
		AgentName: "valet",
		SessionID: "ses_missing",
		JSON:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("runRun() error = %v, want not-mapped", err)
	}
	if fake.userCalls != 0 {
		t.Errorf("turn calls = %d, want 0", fake.userCalls)
	}

	var got runResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output: %v; raw %q", err, out.String())
	}
	if got.Error == "" || !strings.Contains(got.Error, "not mapped") {
		t.Errorf("json error = %q", got.Error)
	}
}

func TestRunJSONOutput(t *testing.T) {
	ws := setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out, errOut bytes.Buffer
	err := runRun(context.Background(), &out, &errOut, "hello", runOptions{
		AgentName: "valet",
		JSON:      true,
	})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	var got runResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output: %v; raw %q", err, out.String())
	}
	if got.Agent != "valet" || got.Thread != "cli:valet" {
		t.Errorf("agent/thread = %q/%q", got.Agent, got.Thread)
	}
	if got.Workspace != canonicalWorkspace(t, ws) {
		t.Errorf("workspace = %q, want %q", got.Workspace, canonicalWorkspace(t, ws))
	}
	if got.SessionID != "ses_cli" || got.InputID != "msg_in" || got.Reply != "reply" {
		t.Errorf("result = %+v", got)
	}
	if len(got.MessageIDs) != 1 || got.MessageIDs[0] != "msg_out" {
		t.Errorf("message ids = %v", got.MessageIDs)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty", got.Error)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestRunTurnErrorKeepsSession(t *testing.T) {
	ws := setupCronCLI(t)
	fake := &fakeCLITurn{userErr: errors.New("boom")}
	withFakeTurn(t, fake)

	var out, errOut bytes.Buffer
	err := runRun(context.Background(), &out, &errOut, "hello", runOptions{AgentName: "valet"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("runRun() error = %v, want boom", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty on error", out.String())
	}
	if !strings.Contains(errOut.String(), "session: ses_cli") {
		t.Errorf("stderr = %q, want session line", errOut.String())
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	session, ok, err := st.Orch().GetSessionByThread("cli:valet")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_cli" {
		t.Fatalf("cli session = %+v, %v, %v; want ses_cli", session, ok, err)
	}
}

func TestRunVerbose(t *testing.T) {
	setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out, errOut bytes.Buffer
	err := runRun(context.Background(), &out, &errOut, "hello", runOptions{
		AgentName: "valet",
		Verbose:   true,
	})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	for _, want := range []string{"agent=valet", "input=msg_in", "elapsed=", "messages: msg_out"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q: %q", want, errOut.String())
		}
	}
}

func TestRunValidation(t *testing.T) {
	t.Setenv("ACOLYTE_HOME", t.TempDir())

	if err := runRun(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "  ", runOptions{}); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Errorf("empty message error = %v", err)
	}
	if err := runRun(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "hello", runOptions{
		SessionID: "ses_x",
		Continue:  true,
	}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("session+continue error = %v", err)
	}
	if err := runRun(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "hello", runOptions{
		Timeout: -time.Second,
	}); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("negative timeout error = %v", err)
	}
}

func TestRunAcceptsTimeoutOverFiveMinutes(t *testing.T) {
	setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	err := runRun(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "hello", runOptions{
		AgentName: "valet",
		Timeout:   10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("runRun() error: %v", err)
	}
	if fake.userConfig.Timeout != 10*time.Minute {
		t.Errorf("turn timeout = %v, want 10m", fake.userConfig.Timeout)
	}
}

func TestRunTimeoutFlag(t *testing.T) {
	flag := runCmd.Flags().Lookup("timeout")
	if flag == nil {
		t.Fatal("--timeout flag is not registered")
	}
	if flag.DefValue != "0s" {
		t.Errorf("--timeout default = %q, want 0s", flag.DefValue)
	}

	t.Cleanup(func() { runTimeout = 0 })
	if err := runCmd.Flags().Set("timeout", "10m"); err != nil {
		t.Fatalf("parse --timeout 10m: %v", err)
	}
	if runTimeout != 10*time.Minute {
		t.Errorf("--timeout 10m = %v, want 10m", runTimeout)
	}
	if err := runCmd.Flags().Set("timeout", "-1s"); err != nil {
		t.Fatalf("parse --timeout -1s: %v", err)
	}
	if runTimeout != -time.Second {
		t.Errorf("--timeout -1s = %v, want -1s", runTimeout)
	}
}
