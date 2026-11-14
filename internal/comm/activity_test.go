package comm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/storepool"
)

func newActivityStore(t *testing.T) *store.Store {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pool := storepool.New()
	t.Cleanup(func() { _ = pool.CloseAll() })
	st, release, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	t.Cleanup(release)
	return st
}

func TestActivityRecorderRecordsOnlyToolEvents(t *testing.T) {
	st := newActivityStore(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	handler := ActivityRecorder{Agent: "valet", Now: func() time.Time { return now }}.Handler(st)

	toolData := `{"sessionID":"ses_1","assistantMessageID":"msg_1","id":"tool_1",` +
		`"input":{"api_key":"secret-input"},"content":[{"type":"text","text":"secret-output"}]}`
	events := []opencode.Event{
		{ID: "evt_called", Type: "session.tool.called", Data: json.RawMessage(toolData)},
		{ID: "evt_text", Type: "session.message.content.updated", Data: json.RawMessage(`{"sessionID":"ses_1","content":[{"type":"text","text":"hello"}]}`)},
		{ID: "evt_reasoning", Type: "session.reasoning.started", Data: json.RawMessage(`{"sessionID":"ses_1"}`)},
		{ID: "evt_success", Type: "session.tool.success", Data: json.RawMessage(toolData)},
		{ID: "evt_idle", Type: "session.idle", Data: json.RawMessage(`{"sessionID":"ses_1"}`)},
		{ID: "evt_failed", Type: "session.tool.failed", Data: json.RawMessage(toolData)},
	}
	for _, event := range events {
		if err := handler(event); err != nil {
			t.Fatalf("Handler(%s) error: %v", event.Type, err)
		}
	}

	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("ListLogs() error: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("logs = %+v, want 3 tool rows", logs)
	}
	want := []struct {
		event string
		level string
		text  string
	}{
		{"evt_called", "info", "tool called"},
		{"evt_success", "info", "tool succeeded"},
		{"evt_failed", "warn", "tool failed"},
	}
	for index, expected := range want {
		entry := logs[index]
		if entry.Level != expected.level || entry.Message != expected.text {
			t.Errorf("log %d = %q/%q, want %q/%q", index, entry.Level, entry.Message, expected.level, expected.text)
		}
		if entry.Source != "opencode:valet" {
			t.Errorf("log %d source = %q", index, entry.Source)
		}
		if entry.CreatedAt.IsZero() {
			t.Errorf("log %d has no timestamp", index)
		}
		var meta map[string]string
		if err := json.Unmarshal([]byte(entry.Meta), &meta); err != nil {
			t.Fatalf("log %d meta %q: %v", index, entry.Meta, err)
		}
		if len(meta) != 5 {
			t.Errorf("log %d meta keys = %v, want only IDs", index, meta)
		}
		if meta["event_id"] != expected.event || meta["event_type"] == "" {
			t.Errorf("log %d meta = %v", index, meta)
		}
		if meta["session_id"] != "ses_1" || meta["assistant_message_id"] != "msg_1" || meta["tool_call_id"] != "tool_1" {
			t.Errorf("log %d meta = %v", index, meta)
		}
		if strings.Contains(entry.Meta, "secret-input") || strings.Contains(entry.Meta, "secret-output") || strings.Contains(entry.Meta, "hello") {
			t.Errorf("log %d leaked content: %q", index, entry.Meta)
		}
	}
}

func TestActivityRecorderHandlesMalformedAndOversizedData(t *testing.T) {
	st := newActivityStore(t)
	handler := ActivityRecorder{Agent: "valet"}.Handler(st)

	huge := strings.Repeat("x", activityFieldMax+50)
	events := []opencode.Event{
		{ID: huge, Type: "session.tool.called", Data: json.RawMessage(`{not json`)},
		{ID: "evt_types", Type: "session.tool.success", Data: json.RawMessage(`{"sessionID":123,"assistantMessageID":[],"id":{}}`)},
	}
	for _, event := range events {
		if err := handler(event); err != nil {
			t.Fatalf("Handler() error: %v", err)
		}
	}

	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("ListLogs() error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %+v, want 2 rows", logs)
	}
	if len(logs[0].Meta) > activityMetaMax || !json.Valid([]byte(logs[0].Meta)) {
		t.Fatalf("meta = %q, want valid JSON within %d bytes", logs[0].Meta, activityMetaMax)
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(logs[0].Meta), &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if got := len([]rune(meta["event_id"])); got != activityFieldMax {
		t.Errorf("event_id length = %d, want %d", got, activityFieldMax)
	}
	if len(meta) != 5 {
		t.Errorf("meta keys = %v, want only IDs", meta)
	}
	if meta["session_id"] != "" || meta["tool_call_id"] != "" {
		t.Errorf("meta = %v, want empty ids for malformed data", meta)
	}
	var typed map[string]string
	if err := json.Unmarshal([]byte(logs[1].Meta), &typed); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if typed["session_id"] != "" || typed["assistant_message_id"] != "" || typed["tool_call_id"] != "" {
		t.Errorf("meta = %v, want empty non-string ids", typed)
	}
}

func TestActivityRecorderNeverReturnsError(t *testing.T) {
	event := opencode.Event{ID: "evt_1", Type: "session.tool.called", Data: json.RawMessage(`{}`)}
	if err := (ActivityRecorder{}).Handler(nil)(event); err != nil {
		t.Fatalf("Handler(nil store) error: %v", err)
	}

	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pool := storepool.New()
	st, release, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	release() // closes the store so AppendLog fails
	if err := pool.CloseAll(); err != nil {
		t.Fatalf("CloseAll() error: %v", err)
	}
	if err := (ActivityRecorder{Agent: "valet"}).Handler(st)(event); err != nil {
		t.Fatalf("Handler(closed store) error: %v", err)
	}
}
