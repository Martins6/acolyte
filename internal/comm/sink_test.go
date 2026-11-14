package comm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/storepool"
)

type fakeSynthTurn struct {
	result  *opencode.TurnResult
	err     error
	calls   int
	configs []opencode.TurnConfig
	events  []opencode.Event
}

func (f *fakeSynthTurn) RunSyntheticTurn(_ context.Context, _ string, _ opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.calls++
	f.configs = append(f.configs, cfg)
	for _, event := range f.events {
		if cfg.OnEvent != nil {
			if err := cfg.OnEvent(event); err != nil {
				return nil, err
			}
		}
	}
	if f.result == nil {
		f.result = &opencode.TurnResult{SessionID: "ses_synth", InputID: "msg_in", ResponseText: "reply", MessageIDs: []string{"msg_out"}}
	}
	copy := *f.result
	return &copy, f.err
}

func newSinkHarness(t *testing.T) (*OutboxSink, *storepool.Pool, registry.Agent, func()) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pool := storepool.New()
	now := time.Now().UTC().Truncate(time.Second)
	sink := &OutboxSink{
		Stores:   pool,
		Turn:     &fakeSynthTurn{},
		Now:      func() time.Time { return now },
		Activity: &ActivityRecorder{},
	}
	return sink, pool, registry.Agent{Name: "valet", Workspace: ws}, func() { _ = pool.CloseAll() }
}

func TestOutboxSinkNotifyUser(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	if err := sink.NotifyUser(context.Background(), agent, "hello"); err == nil {
		t.Fatal("NotifyUser(no chat) error = nil, want error")
	}
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	release()
	if err := sink.NotifyUser(context.Background(), agent, "hello"); err != nil {
		t.Fatalf("NotifyUser() error: %v", err)
	}
	st, release, err = pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Text != "hello" {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
	if outbox[0].OpenCodeSessionID != "" {
		t.Errorf("session id = %q, want empty", outbox[0].OpenCodeSessionID)
	}
}

func TestOutboxSinkNotifyUserAttributed(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	release()

	if err := sink.NotifyUserAttributed(context.Background(), agent, "hello", "  ses_x  "); err != nil {
		t.Fatalf("NotifyUserAttributed() error: %v", err)
	}
	st, release, err = pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
	item := outbox[0]
	if item.Text != "hello" || item.OpenCodeSessionID != "ses_x" || item.MediaPath != "" {
		t.Errorf("item = %+v", item)
	}
	if fake := sink.Turn.(*fakeSynthTurn); fake.calls != 0 {
		t.Errorf("turn calls = %d, want none", fake.calls)
	}
}

func TestOutboxSinkNotifyUserMedia(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	release()

	mediaPath := filepath.Join(agent.Workspace, "media", "outbound", "images", "chart.png")
	if err := sink.NotifyUserMedia(context.Background(), agent, "chart", mediaPath, "ses_x"); err != nil {
		t.Fatalf("NotifyUserMedia() error: %v", err)
	}
	st, release, err = pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
	item := outbox[0]
	if item.Text != "chart" || item.MediaPath != mediaPath || item.OpenCodeSessionID != "ses_x" {
		t.Errorf("item = %+v", item)
	}
}

func TestOutboxSinkInboxWithoutChat(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	reply, sessionID, err := sink.Inbox(context.Background(), agent, "", "ping", nil)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if reply != "reply" || sessionID != "ses_synth" {
		t.Errorf("Inbox() = %q, %q", reply, sessionID)
	}
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(0)
	if err != nil || len(outbox) != 0 {
		t.Errorf("outbox = %+v, %v; want none", outbox, err)
	}
	var message string
	if err := st.DB().QueryRow(`SELECT message FROM orch_logs ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if message == "" {
		t.Error("expected a no-channel log entry")
	}
}

func TestOutboxSinkInboxRecordsToolActivity(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	fake := sink.Turn.(*fakeSynthTurn)
	fake.events = []opencode.Event{
		{ID: "evt_1", Type: "session.tool.success", Data: json.RawMessage(
			`{"sessionID":"ses_synth","assistantMessageID":"msg_out","id":"tool_1","content":[{"type":"text","text":"secret"}]}`)},
		{ID: "evt_2", Type: "session.message.content.updated", Data: json.RawMessage(
			`{"sessionID":"ses_synth","content":[{"type":"text","text":"secret"}]}`)},
	}
	if _, _, err := sink.Inbox(context.Background(), agent, "", "ping", nil); err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(fake.configs) != 1 || fake.configs[0].OnEvent == nil {
		t.Fatalf("turn configs = %+v, want OnEvent wired", fake.configs)
	}
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("ListLogs() error: %v", err)
	}
	var activity []string
	for _, entry := range logs {
		if entry.Source == "opencode:valet" {
			activity = append(activity, entry.Level+"/"+entry.Message+"/"+entry.Meta)
			if strings.Contains(entry.Meta, "secret") {
				t.Errorf("meta leaked content: %q", entry.Meta)
			}
		}
	}
	if len(activity) != 1 {
		t.Fatalf("activity logs = %v, want one tool row", activity)
	}
	if !strings.HasPrefix(activity[0], "info/tool succeeded/") || !strings.Contains(activity[0], `"tool_call_id":"tool_1"`) {
		t.Errorf("activity log = %q", activity[0])
	}
}

func TestOutboxSinkInboxWithoutActivityLeavesOnEventNil(t *testing.T) {
	sink, _, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	sink.Activity = nil
	if _, _, err := sink.Inbox(context.Background(), agent, "", "ping", nil); err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	fake := sink.Turn.(*fakeSynthTurn)
	if len(fake.configs) != 1 || fake.configs[0].OnEvent != nil {
		t.Fatalf("turn configs = %+v, want nil OnEvent", fake.configs)
	}
}

func TestOutboxSinkInboxUsesSessionThread(t *testing.T) {
	sink, pool, agent, cleanup := newSinkHarness(t)
	defer cleanup()
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CommTG().UpsertChat(10, "old", true, true, now); err != nil {
		t.Fatalf("UpsertChat(old): %v", err)
	}
	if err := st.CommTG().UpsertChat(20, "new", true, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("UpsertChat(new): %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_synth", now); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	release()

	if _, _, err := sink.Inbox(context.Background(), agent, "", "ping", nil); err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	st, release, err = pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox(10) = %+v, %v; want 1", outbox, err)
	}
	if outbox[0].SourceMessageID != "msg_out" || outbox[0].OpenCodeSessionID != "ses_synth" {
		t.Errorf("outbox item = %+v", outbox[0])
	}
	if other, _ := st.CommTG().ListOutbox(20); len(other) != 0 {
		t.Errorf("outbox(20) = %+v, want none", other)
	}
}
