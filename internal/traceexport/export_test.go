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

// TestExportSingleSessionScope proves that --session-id cannot disclose
// unrelated conversations: only the selected session file and its own
// inbox/outbox/chat/media rows survive, global orchestrator and memory data
// is omitted, and the manifest counts match the filtered bundle.
func TestExportSingleSessionScope(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")
	// The media index stores workspace-relative paths here; both files exist
	// so the scoped export can copy only the selected chat's media.
	writeFile(t, ws, "downloads/media/selected.jpg", "selected media bytes")
	writeFile(t, ws, "downloads/media/other.jpg", "other media bytes")

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Two mapped sessions, each with its own chat and routing rows.
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", now); err != nil {
		t.Fatalf("upsert session x: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(20), "ses_y", now.Add(time.Minute)); err != nil {
		t.Fatalf("upsert session y: %v", err)
	}
	// Global orchestrator state that must not leak into a scoped bundle.
	if err := st.Orch().AppendLog("info", "orchestrator", "global turn completed", `{"turn":"t1"}`, now); err != nil {
		t.Fatalf("append log: %v", err)
	}
	if err := st.Orch().AddCronJob(orch.CronJob{
		Title: "heartbeat", Kind: "routine", Spec: "@every 1h", Routine: "heartbeat",
		AgentTarget: "valet", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("add cron job: %v", err)
	}
	if err := st.Orch().AddCronRoute(orch.CronRoute{
		AgentTarget: "valet", ChatID: 10, Text: "global cron route", CreatedAt: now,
	}); err != nil {
		t.Fatalf("add cron route: %v", err)
	}
	if err := st.Orch().SetSetting("notify", "on"); err != nil {
		t.Fatalf("set orch setting: %v", err)
	}
	// Chats and rows: session x uses chat 10, session y uses chat 20.
	if err := st.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("upsert chat 10: %v", err)
	}
	if err := st.CommTG().UpsertChat(20, "bob", false, true, now); err != nil {
		t.Fatalf("upsert chat 20: %v", err)
	}
	if err := st.CommTG().SetSetting("channel", "telegram"); err != nil {
		t.Fatalf("set comm setting: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: 1, ChatID: 10, EnvelopeID: "env_in_x", OpenCodeSessionID: "ses_x",
		OpenCodeMessageID: "msg_x1", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("record inbound x: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: 2, ChatID: 20, EnvelopeID: "env_in_y", OpenCodeSessionID: "ses_y",
		OpenCodeMessageID: "msg_y1", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("record inbound y: %v", err)
	}
	if _, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_out_x", ChatID: 10, Text: "selected reply", OpenCodeSessionID: "ses_x",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("enqueue outbound x: %v", err)
	}
	if _, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_out_y", ChatID: 20, Text: "other chat reply", OpenCodeSessionID: "ses_y",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("enqueue outbound y: %v", err)
	}
	if err := st.CommTG().RecordMedia(commtg.MediaItem{
		TGFileID: "file_x", FilePath: "downloads/media/selected.jpg", Type: "photo", ChatID: 10, SavedAt: now,
	}); err != nil {
		t.Fatalf("record media x: %v", err)
	}
	if err := st.CommTG().RecordMedia(commtg.MediaItem{
		TGFileID: "file_y", FilePath: "downloads/media/other.jpg", Type: "photo", ChatID: 20, SavedAt: now,
	}); err != nil {
		t.Fatalf("record media y: %v", err)
	}
	// Global memory state.
	if err := st.Mem().RecordDream(mem.Dream{
		RanAt: now, SessionsScanned: 2, Note: "global dream note",
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
		sessions: []opencode.SessionInfo{
			{
				ID: "ses_x", Agent: "build", Title: "Selected conversation",
				Time:     opencode.SessionTime{Created: float64(now.UnixMilli())},
				Location: opencode.SessionLocation{Directory: ws},
			},
			{
				ID: "ses_y", Agent: "build", Title: "Other conversation",
				Time:     opencode.SessionTime{Created: float64(now.Add(time.Minute).UnixMilli())},
				Location: opencode.SessionLocation{Directory: ws},
			},
		},
		messages: map[string][]opencode.Message{
			"ses_x": {
				mustMessage(t, `{"id":"msg_x1","type":"user","time":{"created":1},"text":"hello x"}`),
				mustMessage(t, `{"id":"msg_x2","type":"assistant","time":{"created":2},"agent":"build","model":{"id":"gpt","providerID":"test"},"finish":"stop","content":[{"type":"text","text":"reply x"}]}`),
			},
			"ses_y": {
				mustMessage(t, `{"id":"msg_y1","type":"user","time":{"created":1},"text":"hello y"}`),
				mustMessage(t, `{"id":"msg_y2","type":"user","time":{"created":2},"text":"more y"}`),
				mustMessage(t, `{"id":"msg_y3","type":"assistant","time":{"created":3},"agent":"build","model":{"id":"gpt","providerID":"test"},"finish":"stop","content":[{"type":"text","text":"reply y"}]}`),
			},
		},
		exports: map[string]json.RawMessage{
			"ses_x": json.RawMessage(`{"info":{"id":"ses_x"},"messages":[]}`),
			"ses_y": json.RawMessage(`{"info":{"id":"ses_y"},"messages":[]}`),
		},
	}

	outPath := filepath.Join(t.TempDir(), "one-session.zip")
	result, err := Export(context.Background(), Request{
		Agent:          registry.Agent{Name: "valet", Workspace: ws},
		Store:          readOnly,
		OpenCode:       fake,
		OutPath:        outPath,
		SessionID:      "ses_x",
		MaxPages:       10,
		AcolyteVersion: "test",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if result.Sessions != 1 || result.Messages != 2 || result.Logs != 0 {
		t.Errorf("result = %+v, want 1 session / 2 messages / 0 logs", result)
	}
	warned := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "single-session export scoped to ses_x") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want single-session scope warning", result.Warnings)
	}

	files := readZip(t, outPath)

	// The scoped bundle keeps only the selected session's files and the
	// global orchestrator/memory files are omitted entirely.
	for _, name := range []string{
		"README.md", "manifest.json", "session-index.md",
		"sessions/ses_x.md", "sessions/ses_x.json",
		"persona/AGENTS.md",
		"orchestrator/sessions.json",
		"communication/chats.json", "communication/inbox.json",
		"communication/outbox.json", "communication/media.json",
	} {
		if _, ok := files[name]; !ok {
			t.Errorf("zip missing %s", name)
		}
	}
	for _, name := range []string{
		"sessions/ses_y.md", "sessions/ses_y.json",
		"orchestrator/logs.md", "orchestrator/cron-jobs.json",
		"orchestrator/cron-routes.json", "orchestrator/settings.json",
		"communication/settings.json", "memory/dreams.json",
	} {
		if _, ok := files[name]; ok {
			t.Errorf("zip must not contain %s", name)
		}
	}
	for prefix := range files {
		if strings.HasPrefix(prefix, "memory/") {
			t.Errorf("zip must not contain global memory data: %s", prefix)
		}
	}

	// Only the selected session's communication rows, chats, and media.
	if !strings.Contains(files["communication/inbox.json"], "env_in_x") ||
		strings.Contains(files["communication/inbox.json"], "env_in_y") {
		t.Errorf("inbox not scoped to ses_x:\n%s", files["communication/inbox.json"])
	}
	if !strings.Contains(files["communication/outbox.json"], "selected reply") ||
		strings.Contains(files["communication/outbox.json"], "other chat reply") {
		t.Errorf("outbox not scoped to ses_x:\n%s", files["communication/outbox.json"])
	}
	if !strings.Contains(files["communication/chats.json"], "alice") ||
		strings.Contains(files["communication/chats.json"], "bob") {
		t.Errorf("chats not scoped to chat 10:\n%s", files["communication/chats.json"])
	}
	if !strings.Contains(files["communication/media.json"], "selected.jpg") ||
		strings.Contains(files["communication/media.json"], "other.jpg") {
		t.Errorf("media not scoped to chat 10:\n%s", files["communication/media.json"])
	}
	mapping := files["orchestrator/sessions.json"]
	if !strings.Contains(mapping, "ses_x") || strings.Contains(mapping, "ses_y") {
		t.Errorf("session mapping not filtered:\n%s", mapping)
	}

	// Nothing about the other session or global state may appear anywhere.
	for name, content := range files {
		for _, banned := range []string{
			"ses_y", "hello y", "reply y", "env_in_y", "env_out_y",
			"other chat reply", "bob", "other.jpg", "other media bytes",
			"global turn completed", "heartbeat", "global cron route",
			"global dream note", `"notify"`, `"channel"`,
		} {
			if strings.Contains(content, banned) {
				t.Errorf("file %s discloses %q", name, banned)
			}
		}
	}
	// The selected session's own content and routing survive.
	if !strings.Contains(files["sessions/ses_x.md"], "hello x") ||
		!strings.Contains(files["sessions/ses_x.md"], "selected reply") {
		t.Errorf("session markdown lost its own content:\n%s", files["sessions/ses_x.md"])
	}

	// The manifest counts must match the filtered bundle exactly.
	var parsed struct {
		ScopedSession string `json:"scoped_session"`
		Counts        struct {
			Sessions     int `json:"sessions"`
			Messages     int `json:"messages"`
			Logs         int `json:"logs"`
			PersonaFiles int `json:"persona_files"`
			CommInbox    int `json:"comm_inbox"`
			CommOutbox   int `json:"comm_outbox"`
			CommMedia    int `json:"comm_media"`
			MediaFiles   int `json:"media_files"`
			MemoryDreams int `json:"memory_dreams"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &parsed); err != nil {
		t.Fatalf("manifest invalid: %v\n%s", err, files["manifest.json"])
	}
	if parsed.ScopedSession != "ses_x" {
		t.Errorf("scoped_session = %q, want ses_x", parsed.ScopedSession)
	}
	counts := parsed.Counts
	if counts.Sessions != 1 || counts.Messages != 2 || counts.Logs != 0 ||
		counts.CommInbox != 1 || counts.CommOutbox != 1 || counts.CommMedia != 1 ||
		counts.MediaFiles != 1 || counts.MemoryDreams != 0 {
		t.Errorf("counts = %+v, want filtered values", counts)
	}
	personaFiles := 0
	sessionFiles := 0
	for name := range files {
		switch {
		case strings.HasPrefix(name, "persona/"):
			personaFiles++
		case strings.HasPrefix(name, "sessions/") && strings.HasSuffix(name, ".md"):
			sessionFiles++
		}
	}
	if counts.PersonaFiles != personaFiles || personaFiles == 0 {
		t.Errorf("persona_files = %d, want %d (agent identity stays)", counts.PersonaFiles, personaFiles)
	}
	if counts.Sessions != sessionFiles {
		t.Errorf("sessions count = %d, want %d exported session files", counts.Sessions, sessionFiles)
	}
	if got := countJSONRows(t, files["communication/inbox.json"]); got != counts.CommInbox {
		t.Errorf("inbox rows = %d, manifest comm_inbox = %d", got, counts.CommInbox)
	}
	if got := countJSONRows(t, files["communication/outbox.json"]); got != counts.CommOutbox {
		t.Errorf("outbox rows = %d, manifest comm_outbox = %d", got, counts.CommOutbox)
	}
	if got := countJSONRows(t, files["communication/media.json"]); got != counts.CommMedia {
		t.Errorf("media rows = %d, manifest comm_media = %d", got, counts.CommMedia)
	}
	copiedMedia := mediaEntries(files)
	if len(copiedMedia) != counts.MediaFiles || counts.MediaFiles != 1 {
		t.Errorf("copied media files = %d, manifest media_files = %d", len(copiedMedia), counts.MediaFiles)
	}
	for name, content := range copiedMedia {
		if content != "selected media bytes" {
			t.Errorf("media %s content = %q, want selected media bytes", name, content)
		}
		if strings.Contains(content, "other media bytes") {
			t.Errorf("media %s leaked another chat's media", name)
		}
	}
	if got := countJSONRows(t, files["orchestrator/sessions.json"]); got != 1 {
		t.Errorf("orchestrator mappings = %d, want 1", got)
	}
	if !strings.Contains(files["README.md"], "single-session export") ||
		!strings.Contains(files["README.md"], "ses_x") {
		t.Errorf("README missing the single-session scope note:\n%s", files["README.md"])
	}
}

// TestExportCopiesMediaFiles proves referenced media files are copied into
// the zip byte-for-byte under a deterministic collision-free name, and that
// manifest media_files counts actual copies (not duplicate source rows).
func TestExportCopiesMediaFiles(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")

	absPath := filepath.Join(ws, "media", "inbound", "images", "photo.jpg")
	absBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x01, 0x02, 0x03, 0x00}
	writeBytes(t, absPath, absBytes)
	relPath := "media/inbound/documents/photo.jpg"
	relBytes := []byte("documents photo bytes\x00with nul")
	writeBytes(t, filepath.Join(ws, filepath.FromSlash(relPath)), relBytes)

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for index, item := range []commtg.MediaItem{
		{TGFileID: "file_abs", FilePath: absPath, Type: "photo", ChatID: 10, SavedAt: now},
		{TGFileID: "file_rel", FilePath: relPath, Type: "document", ChatID: 10, SavedAt: now.Add(time.Second)},
		// A second row for the same source file must not duplicate the copy.
		{TGFileID: "file_dup", FilePath: absPath, Type: "photo", ChatID: 10, SavedAt: now.Add(2 * time.Second)},
	} {
		if err := st.CommTG().RecordMedia(item); err != nil {
			t.Fatalf("record media %d: %v", index, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only store: %v", err)
	}
	defer readOnly.Close()

	outPath := filepath.Join(t.TempDir(), "media.zip")
	if _, err := Export(context.Background(), Request{
		Agent:   registry.Agent{Name: "valet", Workspace: ws},
		Store:   readOnly,
		OutPath: outPath,
		Now:     now,
	}); err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	files := readZip(t, outPath)
	copied := mediaEntries(files)
	if len(copied) != 2 {
		t.Fatalf("copied media files = %d, want 2: %v", len(copied), copied)
	}
	resolvedRel := filepath.Join(ws, filepath.FromSlash(relPath))
	expected := map[string]string{
		mediaZipDir + "/" + mediaArchiveName(absPath, 0):     string(absBytes),
		mediaZipDir + "/" + mediaArchiveName(resolvedRel, 0): string(relBytes),
	}
	for name, want := range expected {
		got, ok := copied[name]
		if !ok {
			t.Errorf("zip missing media copy %s; have %v", name, mediaNames(copied))
			continue
		}
		if got != want {
			t.Errorf("media %s = %q, want byte-for-byte %q", name, got, want)
		}
	}
	for name := range copied {
		base := strings.TrimPrefix(name, mediaZipDir+"/")
		if !strings.HasPrefix(base, "photo-") || !strings.HasSuffix(base, ".jpg") {
			t.Errorf("media name %q does not keep the original base name and extension", base)
		}
	}

	var parsed struct {
		Counts struct {
			CommMedia  int `json:"comm_media"`
			MediaFiles int `json:"media_files"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &parsed); err != nil {
		t.Fatalf("manifest invalid: %v\n%s", err, files["manifest.json"])
	}
	if parsed.Counts.CommMedia != 3 || parsed.Counts.MediaFiles != len(copied) {
		t.Errorf("counts = %+v, want 3 media rows and %d copied files", parsed.Counts, len(copied))
	}
	if !strings.Contains(files["README.md"], "communication/media/") ||
		!strings.Contains(files["README.md"], "Copied media files: 2") {
		t.Errorf("README missing media copy description:\n%s", files["README.md"])
	}
}

// TestExportSkipsUnavailableMediaSources proves missing, symlinked, and
// non-regular media files are skipped with manifest warnings instead of
// failing the export, and that no symlink target leaks into the zip.
func TestExportSkipsUnavailableMediaSources(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")

	goodPath := filepath.Join(ws, "media", "inbound", "images", "ok.jpg")
	goodBytes := []byte("good media bytes")
	writeBytes(t, goodPath, goodBytes)

	missingPath := filepath.Join(ws, "media", "inbound", "images", "missing.jpg")

	outside := filepath.Join(t.TempDir(), "secret.jpg")
	writeBytes(t, outside, []byte("leaked secret bytes"))
	symlinkPath := filepath.Join(ws, "media", "inbound", "images", "link.jpg")
	if err := os.MkdirAll(filepath.Dir(symlinkPath), 0755); err != nil {
		t.Fatalf("mkdir media dir: %v", err)
	}
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dirPath := filepath.Join(ws, "media", "inbound", "images", "dir.jpg")
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		t.Fatalf("mkdir non-regular source: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, item := range []commtg.MediaItem{
		{TGFileID: "file_ok", FilePath: goodPath, Type: "photo", ChatID: 10, SavedAt: now},
		{TGFileID: "file_missing", FilePath: missingPath, Type: "photo", ChatID: 10, SavedAt: now.Add(time.Second)},
		{TGFileID: "file_symlink", FilePath: symlinkPath, Type: "photo", ChatID: 10, SavedAt: now.Add(2 * time.Second)},
		{TGFileID: "file_dir", FilePath: dirPath, Type: "document", ChatID: 10, SavedAt: now.Add(3 * time.Second)},
	} {
		if err := st.CommTG().RecordMedia(item); err != nil {
			t.Fatalf("record media: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only store: %v", err)
	}
	defer readOnly.Close()

	outPath := filepath.Join(t.TempDir(), "skipped-media.zip")
	if _, err := Export(context.Background(), Request{
		Agent:   registry.Agent{Name: "valet", Workspace: ws},
		Store:   readOnly,
		OutPath: outPath,
		Now:     now,
	}); err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	files := readZip(t, outPath)
	copied := mediaEntries(files)
	if len(copied) != 1 {
		t.Fatalf("copied media files = %d, want only the regular file: %v", len(copied), mediaNames(copied))
	}
	for name, content := range copied {
		if content != string(goodBytes) {
			t.Errorf("media %s = %q, want %q", name, content, goodBytes)
		}
	}
	for name, content := range files {
		if strings.Contains(content, "leaked secret bytes") {
			t.Errorf("zip entry %s leaked a symlink target", name)
		}
	}

	var parsed struct {
		Counts struct {
			CommMedia  int `json:"comm_media"`
			MediaFiles int `json:"media_files"`
		} `json:"counts"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &parsed); err != nil {
		t.Fatalf("manifest invalid: %v\n%s", err, files["manifest.json"])
	}
	if parsed.Counts.CommMedia != 4 || parsed.Counts.MediaFiles != 1 {
		t.Errorf("counts = %+v, want 4 media rows and 1 copied file", parsed.Counts)
	}
	warnings := strings.Join(parsed.Warnings, "\n")
	for _, want := range []string{"skipped media", "missing", "symlink", "non-regular"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("manifest warnings missing %q:\n%s", want, warnings)
		}
	}
}

// mediaEntries returns only the copied media entries from a read zip.
func mediaEntries(files map[string]string) map[string]string {
	entries := make(map[string]string)
	for name, content := range files {
		if strings.HasPrefix(name, mediaZipDir+"/") {
			entries[name] = content
		}
	}
	return entries
}

// mediaNames lists zip entry names for debugging failed assertions.
func mediaNames(entries map[string]string) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	return names
}

func writeBytes(t *testing.T, filePath string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

// countJSONRows decodes a JSON array and returns its length.
func countJSONRows(t *testing.T, raw string) int {
	t.Helper()
	var rows []any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("decode rows: %v\n%s", err, raw)
	}
	return len(rows)
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
