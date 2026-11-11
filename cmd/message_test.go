package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store"
)

type fakeCLITurn struct {
	userCalls  int
	synthCalls int
	result     *opencode.TurnResult
	messages   []opencode.Message
	messageErr error
	sessions   []opencode.SessionInfo
	bySession  map[string][]opencode.Message
	exports    map[string]json.RawMessage

	userSessionID string
	userText      string
	userConfig    opencode.TurnConfig
	userErr       error
}

func (f *fakeCLITurn) RunUserTurn(_ context.Context, sessionID, text string, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.userCalls++
	f.userSessionID = sessionID
	f.userText = text
	f.userConfig = cfg
	result := f.turnResult()
	if sessionID != "" {
		result.SessionID = sessionID
	}
	return result, f.userErr
}

func (f *fakeCLITurn) RunSyntheticTurn(context.Context, string, opencode.SyntheticEvent, opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.synthCalls++
	return f.turnResult(), nil
}

func (f *fakeCLITurn) SessionMessages(_ context.Context, sessionID string, _ int) ([]opencode.Message, bool, error) {
	if f.bySession != nil {
		return f.bySession[sessionID], false, f.messageErr
	}
	return f.messages, false, f.messageErr
}

func (f *fakeCLITurn) ListSessions(context.Context) ([]opencode.SessionInfo, error) {
	return f.sessions, nil
}

func (f *fakeCLITurn) ExportSession(_ context.Context, sessionID string) (json.RawMessage, error) {
	if raw, ok := f.exports[sessionID]; ok {
		return raw, nil
	}
	return nil, nil
}

func (f *fakeCLITurn) turnResult() *opencode.TurnResult {
	if f.result != nil {
		copy := *f.result
		return &copy
	}
	return &opencode.TurnResult{
		SessionID:    "ses_cli",
		InputID:      "msg_in",
		ResponseText: "reply",
		MessageIDs:   []string{"msg_out"},
	}
}

func withFakeTurn(t *testing.T, fake turnClient) {
	t.Helper()
	old := openCodeEnsure
	openCodeEnsure = func(context.Context) (turnClient, error) { return fake, nil }
	t.Cleanup(func() { openCodeEnsure = old })
}

func seedChat(t *testing.T, workspace string) {
	t.Helper()
	st, err := store.Open(workspace)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.CommTG().UpsertChat(10, "alice", true, true, time.Now().UTC()); err != nil {
		t.Fatalf("upsert chat: %v", err)
	}
}

func TestNotifyUserValidation(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())
	if err := runNotifyUser(context.Background(), &bytes.Buffer{}, "  ", "", ""); err == nil {
		t.Fatal("notify-user(empty) error = nil, want error")
	}
	if err := runNotifyUser(context.Background(), &bytes.Buffer{}, "hello", "missing", ""); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("notify-user(missing agent) error = %v, want not registered", err)
	}
}

func TestNotifyUserQueuesOutbox(t *testing.T) {
	ws := setupCronCLI(t)
	seedChat(t, ws)
	var out bytes.Buffer
	if err := runNotifyUser(context.Background(), &out, "hello", "valet", ""); err != nil {
		t.Fatalf("runNotifyUser() error: %v", err)
	}
	if !strings.Contains(out.String(), `queued for agent "valet"`) {
		t.Errorf("output = %q", out.String())
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Text != "hello" {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
}

func TestNotifyUserWithoutChat(t *testing.T) {
	setupCronCLI(t)
	err := runNotifyUser(context.Background(), &bytes.Buffer{}, "hello", "valet", "")
	if err == nil || !strings.Contains(err.Error(), "message the bot first") {
		t.Fatalf("runNotifyUser(no chat) error = %v, want message-the-bot error", err)
	}
}

func TestInboxRejectsUnmappedSession(t *testing.T) {
	setupCronCLI(t)
	err := runInbox(context.Background(), &bytes.Buffer{}, "hello", "valet", "", "ses_missing")
	if err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("inbox(unmapped session) error = %v, want not-mapped error", err)
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

func TestInboxRunsPersistsAndQueuesReply(t *testing.T) {
	ws := setupCronCLI(t)
	seedChat(t, ws)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)

	var out bytes.Buffer
	if err := runInbox(context.Background(), &out, "hello", "valet", "", ""); err != nil {
		t.Fatalf("runInbox() error: %v", err)
	}
	if fake.synthCalls != 1 || fake.userCalls != 0 {
		t.Errorf("turn calls = synth:%d user:%d", fake.synthCalls, fake.userCalls)
	}
	if !strings.Contains(out.String(), "ses_cli") || !strings.Contains(out.String(), "reply") {
		t.Errorf("output = %q", out.String())
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	session, ok, err := st.Orch().GetSessionByThread("inbox:valet")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_cli" {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Text != "reply" {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
	if outbox[0].SourceMessageID != "msg_out" {
		t.Errorf("source message = %q, want msg_out", outbox[0].SourceMessageID)
	}
}

func TestInboxWithoutChatStillPrintsReply(t *testing.T) {
	ws := setupCronCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	var out bytes.Buffer
	if err := runInbox(context.Background(), &out, "hello", "valet", "", ""); err != nil {
		t.Fatalf("runInbox() error: %v", err)
	}
	if !strings.Contains(out.String(), "Reply:") || !strings.Contains(out.String(), "reply") {
		t.Errorf("output = %q, want reply printed", out.String())
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	outbox, err := st.CommTG().ListOutbox(0)
	if err != nil || len(outbox) != 0 {
		t.Errorf("outbox = %+v, %v; want none", outbox, err)
	}
}
