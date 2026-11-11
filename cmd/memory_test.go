package cmd

import (
	"archive/zip"
	"bytes"
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
	"github.com/martins6/acolyte/internal/store"
)

// readZip reads every entry of a zip into a map keyed by entry name.
func readZip(t *testing.T, filePath string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()
	files := make(map[string]string, len(reader.File))
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

func TestRunMemoryExport(t *testing.T) {
	ws := setupCronCLI(t)
	now := time.Now().UTC()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", now); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if _, err := st.Orch().UpsertSession("cli:valet", "ses_y", now.Add(time.Minute)); err != nil {
		t.Fatalf("upsert second session: %v", err)
	}
	st.Close()

	fake := &fakeCLITurn{
		sessions: []opencode.SessionInfo{
			{ID: "ses_x", Location: opencode.SessionLocation{Directory: ws}, Time: opencode.SessionTime{Created: float64(now.UnixMilli())}},
			{ID: "ses_y", Location: opencode.SessionLocation{Directory: ws}, Time: opencode.SessionTime{Created: float64(now.Add(time.Minute).UnixMilli())}},
		},
		bySession: map[string][]opencode.Message{
			"ses_x": {{ID: "msg_x", Type: "user", Text: "hello"}},
			"ses_y": {{ID: "msg_y", Type: "assistant", Text: "second"}},
		},
	}
	withFakeTurn(t, fake)

	outPath := filepath.Join(t.TempDir(), "memory.zip")
	var out bytes.Buffer
	if err := runMemoryExport(context.Background(), &out, memoryExportOptions{
		AgentName: "valet",
		OutPath:   outPath,
		MaxPages:  10,
		JSON:      true,
	}); err != nil {
		t.Fatalf("runMemoryExport() error: %v", err)
	}
	var result struct {
		Path     string `json:"path"`
		Sessions int    `json:"sessions"`
		Messages int    `json:"messages"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out.String())
	}
	if result.Path != outPath || result.Sessions != 2 || result.Messages != 2 {
		t.Errorf("result = %+v", result)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("zip not written: %v", err)
	}
}

func TestRunMemoryExportSessionFilter(t *testing.T) {
	ws := setupCronCLI(t)
	now := time.Now().UTC()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", now); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	st.Close()

	fake := &fakeCLITurn{
		sessions: []opencode.SessionInfo{
			{ID: "ses_x", Location: opencode.SessionLocation{Directory: ws}},
			{ID: "ses_y", Location: opencode.SessionLocation{Directory: ws}},
		},
		bySession: map[string][]opencode.Message{"ses_y": {{ID: "msg_y", Type: "user", Text: "only"}}},
	}
	withFakeTurn(t, fake)

	outPath := filepath.Join(t.TempDir(), "one.zip")
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{
		AgentName: "valet",
		OutPath:   outPath,
		SessionID: "ses_y",
		MaxPages:  10,
	}); err != nil {
		t.Fatalf("runMemoryExport() error: %v", err)
	}
	// The zip must exist; content is covered by the traceexport package tests.
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("zip not written: %v", err)
	}
}

func TestRunMemoryExportValidation(t *testing.T) {
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{}); err == nil || !strings.Contains(err.Error(), "--out is required") {
		t.Errorf("missing out error = %v", err)
	}
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{
		OutPath:  filepath.Join(t.TempDir(), "x.zip"),
		MaxPages: -1,
	}); err == nil || !strings.Contains(err.Error(), "--max-pages") {
		t.Errorf("bad max-pages error = %v", err)
	}
}

func TestRunMemoryExportNoOpenCode(t *testing.T) {
	setupCronCLI(t)
	outPath := filepath.Join(t.TempDir(), "store-only.zip")
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{
		AgentName:  "valet",
		OutPath:    outPath,
		MaxPages:   10,
		NoOpenCode: true,
	}); err != nil {
		t.Fatalf("runMemoryExport(no-opencode) error: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("zip not written: %v", err)
	}
}

func TestRunMemoryExportSanitizeFlag(t *testing.T) {
	ws := setupCronCLI(t)
	now := time.Now().UTC()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession("cli:valet", "ses_x", now); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	st.Close()

	fake := &fakeCLITurn{
		sessions: []opencode.SessionInfo{
			{ID: "ses_x", Location: opencode.SessionLocation{Directory: ws}, Time: opencode.SessionTime{Created: float64(now.UnixMilli())}},
		},
		bySession: map[string][]opencode.Message{
			"ses_x": {{ID: "msg_x", Type: "user", Text: "leak OPENAI_API_KEY=sk-abc123def456 please"}},
		},
	}
	withFakeTurn(t, fake)

	// With --sanitize the fake token must be redacted...
	sanitized := filepath.Join(t.TempDir(), "sanitized.zip")
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{
		AgentName: "valet", OutPath: sanitized, MaxPages: 10, Sanitize: true,
	}); err != nil {
		t.Fatalf("runMemoryExport(sanitize) error: %v", err)
	}
	files := readZip(t, sanitized)
	if strings.Contains(files["sessions/ses_x.md"], "sk-abc123def456") {
		t.Errorf("token survived --sanitize:\n%s", files["sessions/ses_x.md"])
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["manifest.json"]), &manifest); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if manifest["sanitized"] != true {
		t.Errorf("manifest sanitized = %v, want true", manifest["sanitized"])
	}

	// ...and without it the trace must be preserved verbatim.
	plain := filepath.Join(t.TempDir(), "plain.zip")
	if err := runMemoryExport(context.Background(), &bytes.Buffer{}, memoryExportOptions{
		AgentName: "valet", OutPath: plain, MaxPages: 10,
	}); err != nil {
		t.Fatalf("runMemoryExport() error: %v", err)
	}
	files = readZip(t, plain)
	if !strings.Contains(files["sessions/ses_x.md"], "sk-abc123def456") {
		t.Errorf("token missing without --sanitize:\n%s", files["sessions/ses_x.md"])
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &manifest); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if manifest["sanitized"] != false {
		t.Errorf("manifest sanitized = %v, want false", manifest["sanitized"])
	}
}
