//go:build e2e

package e2e

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/store"
)

// importResult mirrors the --json output of `acolyte memory import`.
type importResult struct {
	TraceID  string `json:"trace_id"`
	Mode     string `json:"mode"`
	Sessions []struct {
		SourceSessionID string `json:"source_session_id"`
		SessionID       string `json:"session_id"`
		Thread          string `json:"thread"`
	} `json:"sessions"`
	Warnings []string `json:"warnings"`
}

// TestMemoryImportSessions replays a native session fixture into a fresh
// agent through the built binary and verifies the thread mapping plus the
// imported messages. It runs no model turns: the import endpoint only stores
// the transcript, so the test is CI-safe.
func TestMemoryImportSessions(t *testing.T) {
	f := newFixture(t)
	zipPath := buildFixtureTrace(t)

	stdout, stderr, code := runCLI(t, f.home, "memory", "import", zipPath,
		"--agent-name", "e2e", "--mode", "sessions", "--json")
	if code != 0 {
		t.Fatalf("memory import failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var result importResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode import result: %v\nstdout:\n%s", err, stdout)
	}
	if result.Mode != "sessions" {
		t.Errorf("mode = %q, want sessions", result.Mode)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("imported sessions = %+v, want 1\nstderr: %s", result.Sessions, stderr)
	}
	imported := result.Sessions[0]
	if imported.SourceSessionID == "" || imported.SessionID == "" {
		t.Fatalf("session mapping incomplete: %+v", imported)
	}
	wantThread := fmt.Sprintf("memory:%s:%s", result.TraceID, imported.SourceSessionID)
	if imported.Thread != wantThread {
		t.Errorf("thread = %q, want %q", imported.Thread, wantThread)
	}

	// The mapping must be durable in the agent store.
	st, err := store.Open(f.ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	mapped, ok, err := st.Orch().GetSessionByThread(wantThread)
	if err != nil || !ok {
		t.Fatalf("GetSessionByThread(%q) = %v, %v", wantThread, ok, err)
	}
	if mapped.OpenCodeSessionID != imported.SessionID {
		t.Errorf("mapped session = %q, want %q", mapped.OpenCodeSessionID, imported.SessionID)
	}

	// The imported session must carry the fixture messages in OpenCode.
	inspectOut, inspectErr, inspectCode := runCLI(t, f.home, "inspect",
		"--session-id", imported.SessionID, "--agent-name", "e2e", "--no-comm")
	if inspectCode != 0 {
		t.Fatalf("inspect failed (%d)\nstdout:\n%s\nstderr:\n%s", inspectCode, inspectOut, inspectErr)
	}
	for _, want := range []string{"E2E-IMPORT-QUESTION", "E2E-IMPORT-ANSWER"} {
		if !strings.Contains(inspectOut, want) {
			t.Errorf("inspect output missing %q:\n%s", want, inspectOut)
		}
	}
}

// buildFixtureTrace writes a trace zip holding one OpenCode-native session
// payload. The payload matches the import contract of the pinned service:
// info requires projectID/cost/tokens/time/location, message ids carry the
// msg_ prefix, and assistant messages need time.completed to survive.
// Session and message ids are unique per run so repeated local runs never
// collide with sessions imported earlier.
func buildFixtureTrace(t *testing.T) string {
	t.Helper()
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	sessionID := "ses_e2eimport" + suffix
	messageIDs := [3]string{"msg_q" + suffix, "msg_a" + suffix, "msg_i" + suffix}
	now := float64(time.Now().UnixMilli())

	native := map[string]any{
		"info": map[string]any{
			"id":        sessionID,
			"agent":     "build",
			"title":     "e2e import fixture",
			"projectID": "e2e-fixture",
			"cost":      0,
			"tokens":    map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
			"time":      map[string]any{"created": now, "updated": now + 1000},
			"location":  map[string]any{"directory": "/e2e/source"},
		},
		"messages": []any{
			map[string]any{
				"id":   messageIDs[0],
				"type": "user",
				"time": map[string]any{"created": now},
				"text": "E2E-IMPORT-QUESTION",
			},
			map[string]any{
				"id":      messageIDs[1],
				"type":    "assistant",
				"time":    map[string]any{"created": now + 500, "completed": now + 1000},
				"agent":   "build",
				"model":   map[string]any{"id": "fixture", "providerID": "e2e"},
				"content": []any{map[string]any{"type": "text", "text": "E2E-IMPORT-ANSWER"}},
				"finish":  "stop",
			},
			map[string]any{
				"id":      messageIDs[2],
				"type":    "idle",
				"time":    map[string]any{"created": now + 1000},
				"outcome": "succeeded",
			},
		},
	}
	manifest := map[string]any{
		"format":      "acolyte-memory-trace/v1",
		"agent":       map[string]any{"name": "e2e-source", "workspace": "/e2e/source"},
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"sessions": []any{
			map[string]any{
				"id":          sessionID,
				"title":       "e2e import fixture",
				"messages":    3,
				"markdown":    "sessions/" + sessionID + ".md",
				"native_json": "sessions/" + sessionID + ".json",
			},
		},
		"counts": map[string]any{"sessions": 1, "messages": 3},
	}

	zipPath := filepath.Join(t.TempDir(), "e2e-trace.zip")
	if err := writeTraceZip(zipPath, manifest, sessionID, native); err != nil {
		t.Fatalf("build fixture trace: %v", err)
	}
	return zipPath
}

// writeTraceZip writes manifest.json plus the native session payload.
func writeTraceZip(path string, manifest any, sessionID string, native any) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	add := func(name string, data []byte) error {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := add("manifest.json", manifestJSON); err != nil {
		return err
	}
	if err := add("README.md", []byte("# e2e fixture trace\n")); err != nil {
		return err
	}
	nativeJSON, err := json.Marshal(native)
	if err != nil {
		return err
	}
	if err := add("sessions/"+sessionID+".json", nativeJSON); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return file.Sync()
}
