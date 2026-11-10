package traceexport

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/mem"
	"github.com/martins6/acolyte/internal/store/orch"
)

type fakeOpenCode struct {
	sessions  []opencode.SessionInfo
	messages  map[string][]opencode.Message
	truncated map[string]bool
	exports   map[string]json.RawMessage
}

func (f *fakeOpenCode) ListSessions(context.Context) ([]opencode.SessionInfo, error) {
	return f.sessions, nil
}

func (f *fakeOpenCode) SessionMessages(_ context.Context, sessionID string, _ int) ([]opencode.Message, bool, error) {
	return f.messages[sessionID], f.truncated[sessionID], nil
}

func (f *fakeOpenCode) ExportSession(_ context.Context, sessionID string) (json.RawMessage, error) {
	return f.exports[sessionID], nil
}

func TestExportWritesCompleteTrace(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")
	writeFile(t, ws, "opencode.jsonc", "{}\n")
	writeFile(t, ws, ".acolyte/SOUL.md", "# Soul\n")
	writeFile(t, ws, ".agents/skills/demo/SKILL.md", "# Demo skill\n")

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", now); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := st.Orch().AppendLog("info", "orchestrator", "turn completed", `{"turn":"t1"}`, now); err != nil {
		t.Fatalf("append log: %v", err)
	}
	if err := st.Orch().AddCronJob(orch.CronJob{
		Title: "heartbeat", Kind: "routine", Spec: "@every 1h", Routine: "heartbeat",
		AgentTarget: "valet", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("add cron job: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("upsert chat: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: 1, ChatID: 10, EnvelopeID: "env_in", OpenCodeSessionID: "ses_x",
		OpenCodeMessageID: "msg_user", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("record inbound: %v", err)
	}
	out, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_out", ChatID: 10, Text: "reply", OpenCodeSessionID: "ses_x",
		SourceMessageID: "msg_assistant", CreatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("enqueue outbound: %v", err)
	}
	sent := now.Add(2 * time.Second)
	if err := st.CommTG().MarkOutboundSent(out.ID, 1, 42, sent); err != nil {
		t.Fatalf("mark outbound sent: %v", err)
	}
	if err := st.Mem().RecordDream(mem.Dream{
		RanAt: now, SessionsScanned: 1, SkillsCreated: 1, Note: "created demo",
	}); err != nil {
		t.Fatalf("record dream: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only store: %v", err)
	}
	defer readOnly.Close()

	fake := &fakeOpenCode{
		sessions: []opencode.SessionInfo{{
			ID:       "ses_x",
			Agent:    "build",
			Title:    "Test conversation",
			Time:     opencode.SessionTime{Created: float64(now.UnixMilli()), Updated: float64(now.Add(time.Minute).UnixMilli())},
			Location: opencode.SessionLocation{Directory: ws},
		}},
		messages: map[string][]opencode.Message{
			"ses_x": {
				mustMessage(t, `{"id":"msg_user","type":"user","time":{"created":1},"text":"hello"}`),
				mustMessage(t, `{"id":"msg_assistant","type":"assistant","time":{"created":2},"agent":"build","model":{"id":"gpt","providerID":"test"},"finish":"stop","content":[{"type":"reasoning","text":"thinking"},{"type":"tool","id":"tool_1","name":"read","executed":true,"time":{"created":2},"state":{"status":"completed","input":{"path":"/tmp/x"},"content":[{"type":"text","text":"file body"}]}},{"type":"text","text":"reply"}]}`),
				mustMessage(t, `{"id":"msg_idle","type":"idle","time":{"created":3},"outcome":"succeeded"}`),
			},
		},
		truncated: map[string]bool{"ses_x": false},
		exports: map[string]json.RawMessage{
			"ses_x": json.RawMessage(`{"info":{"id":"ses_x"},"messages":[]}`),
		},
	}

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	result, err := Export(context.Background(), Request{
		Agent: registry.Agent{
			Name: "valet", Workspace: ws, Description: "test agent", Engine: "opencode",
			RegisteredAt: now, LastSeenAt: now,
		},
		Store:          readOnly,
		OpenCode:       fake,
		OutPath:        outPath,
		MaxPages:       10,
		AcolyteVersion: "test",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if result.Sessions != 1 || result.Messages != 3 || result.Logs != 1 || result.PersonaFiles != 4 {
		t.Errorf("result = %+v", result)
	}

	files := readZip(t, outPath)
	for _, name := range []string{
		"README.md", "manifest.json", "session-index.md",
		"sessions/ses_x.md", "sessions/ses_x.json",
		"persona/AGENTS.md", "persona/.acolyte/SOUL.md", "persona/.agents/skills/demo/SKILL.md",
		"orchestrator/logs.md", "orchestrator/sessions.json", "orchestrator/cron-jobs.json",
		"communication/chats.json", "communication/inbox.json", "communication/outbox.json",
		"memory/dreams.json",
	} {
		if _, ok := files[name]; !ok {
			t.Errorf("zip missing %s", name)
		}
	}

	session := files["sessions/ses_x.md"]
	for _, want := range []string{
		"hello", "reply", "thinking", "Tool call `read`", `"/tmp/x"`, "file body",
		"communication inbound", "communication outbound", "Raw message JSON",
		"Test conversation", "telegram:10",
	} {
		if !strings.Contains(session, want) {
			t.Errorf("session markdown missing %q:\n%s", want, session)
		}
	}
	if strings.Count(session, "reply") < 2 {
		t.Errorf("session markdown should contain the reply in the message and the outbound routing event")
	}
	if !strings.Contains(files["orchestrator/logs.md"], "turn completed") {
		t.Errorf("logs missing entry")
	}
	if !strings.Contains(files["communication/inbox.json"], "env_in") {
		t.Errorf("inbox json missing row")
	}
	if !strings.Contains(files["manifest.json"], `"sessions": 1`) {
		t.Errorf("manifest missing count:\n%s", files["manifest.json"])
	}
}

func TestExportSkipsSymlinks(t *testing.T) {
	ws := t.TempDir()
	now := time.Now().UTC()
	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".agents", "skills"), 0755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".agents", "skills", "leak.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()
	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer readOnly.Close()

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	result, err := Export(context.Background(), Request{
		Agent:   registry.Agent{Name: "valet", Workspace: ws},
		Store:   readOnly,
		OutPath: outPath,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	files := readZip(t, outPath)
	for name := range files {
		if strings.Contains(name, "leak.md") {
			t.Errorf("symlink was exported as %s", name)
		}
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "skipped symlink") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want skipped symlink", result.Warnings)
	}
}

func TestExportRefusesOverwriteWithoutForce(t *testing.T) {
	ws := t.TempDir()
	now := time.Now().UTC()
	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()
	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer readOnly.Close()

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	if err := os.WriteFile(outPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("seed out: %v", err)
	}
	req := Request{
		Agent:   registry.Agent{Name: "valet", Workspace: ws},
		Store:   readOnly,
		OutPath: outPath,
		Now:     now,
	}
	if _, err := Export(context.Background(), req); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Export() error = %v, want already exists", err)
	}
	req.Force = true
	if _, err := Export(context.Background(), req); err != nil {
		t.Fatalf("Export(force) error: %v", err)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func mustMessage(t *testing.T, raw string) opencode.Message {
	t.Helper()
	var message opencode.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return message
}

func readZip(t *testing.T, filePath string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()
	files := make(map[string]string)
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		raw, err := io.ReadAll(handle)
		handle.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		files[file.Name] = string(raw)
	}
	return files
}
