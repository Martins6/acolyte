package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
)

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
