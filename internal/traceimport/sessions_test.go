package traceimport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// twoSessionManifest renders a manifest listing two sessions with the given
// ids and native payload references.
func twoSessionManifest(firstID, firstPayload, secondID, secondPayload string) string {
	return fmt.Sprintf(`{
  "format": "acolyte-memory-trace/v1",
  "agent": {"name": "valet", "workspace": "/source/ws"},
  "exported_at": "2026-11-10T12:00:00Z",
  "sessions": [
    {"id": %q, "title": "a", "messages": 1, "markdown": "sessions/first.md", "native_json": %q},
    {"id": %q, "title": "b", "messages": 1, "markdown": "sessions/second.md", "native_json": %q}
  ],
  "counts": {"sessions": 2, "messages": 2}
}`, firstID, firstPayload, secondID, secondPayload)
}

// TestRewriteIDsTouchesOnlyIdentifierFields guards the conflict-retry
// rewrite: content that happens to equal an old id must stay byte-identical,
// while every identifier/reference field holding an old id is rewritten.
func TestRewriteIDsTouchesOnlyIdentifierFields(t *testing.T) {
	t.Parallel()

	raw := `{
		"info": {"id": "ses_old", "parentID": "ses_old", "title": "msg_old", "fork": {"sessionID": "ses_old"}},
		"messages": [
			{
				"id": "msg_old",
				"text": "ses_old",
				"summary": "msg_old",
				"metadata": {"tool": "msg_old"},
				"parts": [{"type": "tool", "input": {"path": "msg_old"}, "output": "msg_old"}],
				"revert": {"messageID": "msg_old"}
			}
		]
	}`
	payload, err := decodeTransfer([]byte(raw))
	if err != nil {
		t.Fatalf("decodeTransfer: %v", err)
	}
	payload.rewriteIDs()

	info := payload.info
	if got, _ := info["id"].(string); got == "ses_old" || !strings.HasPrefix(got, "ses_") {
		t.Errorf("info.id = %q, want a new ses_ id", got)
	}
	if got, _ := info["parentID"].(string); got == "ses_old" {
		t.Error("info.parentID was not rewritten")
	}
	fork, ok := info["fork"].(map[string]any)
	if !ok {
		t.Fatal("info.fork is not an object")
	}
	if got, _ := fork["sessionID"].(string); got == "ses_old" {
		t.Error("info.fork.sessionID was not rewritten")
	}
	if got, _ := info["title"].(string); got != "msg_old" {
		t.Errorf("info.title = %q, want it untouched (content, not an id)", got)
	}

	if len(payload.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(payload.messages))
	}
	message, ok := payload.messages[0].(map[string]any)
	if !ok {
		t.Fatal("message is not an object")
	}
	if got, _ := message["id"].(string); got == "msg_old" || !strings.HasPrefix(got, "msg_") {
		t.Errorf("message id = %q, want a new msg_ id", got)
	}
	revert, ok := message["revert"].(map[string]any)
	if !ok {
		t.Fatal("message.revert is not an object")
	}
	if got, _ := revert["messageID"].(string); got == "msg_old" {
		t.Error("message.revert.messageID was not rewritten")
	}

	// Content must be byte-identical even though it equals an old id.
	if got, _ := message["text"].(string); got != "ses_old" {
		t.Errorf("message text = %q, want %q untouched", got, "ses_old")
	}
	if got, _ := message["summary"].(string); got != "msg_old" {
		t.Errorf("message summary = %q, want %q untouched", got, "msg_old")
	}
	metadata, ok := message["metadata"].(map[string]any)
	if !ok {
		t.Fatal("message.metadata is not an object")
	}
	if got, _ := metadata["tool"].(string); got != "msg_old" {
		t.Errorf("metadata tool = %q, want %q untouched", got, "msg_old")
	}
	parts, ok := message["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("message.parts = %v, want one part", message["parts"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatal("part is not an object")
	}
	input, ok := part["input"].(map[string]any)
	if !ok {
		t.Fatal("part.input is not an object")
	}
	if got, _ := input["path"].(string); got != "msg_old" {
		t.Errorf("tool input path = %q, want %q untouched", got, "msg_old")
	}
	if got, _ := part["output"].(string); got != "msg_old" {
		t.Errorf("tool output = %q, want %q untouched", got, "msg_old")
	}

	// And the re-encoded payload must contain the content bytes verbatim.
	body, err := payload.marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"text":"ses_old"`,
		`"summary":"msg_old"`,
		`"tool":"msg_old"`,
		`"output":"msg_old"`,
		`"title":"msg_old"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rewritten payload lost %s:\n%s", want, body)
		}
	}
	for _, banned := range []string{`"id":"ses_old"`, `"sessionID":"ses_old"`, `"messageID":"msg_old"`} {
		if strings.Contains(string(body), banned) {
			t.Errorf("rewritten payload kept identifier %s:\n%s", banned, body)
		}
	}
}

// TestImportRejectsDuplicateManifestSessionIDs: both entries would import and
// the second would overwrite the first's thread mapping, orphaning it, so the
// manifest is rejected before any import request — dry-run included.
func TestImportRejectsDuplicateManifestSessionIDs(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json":       twoSessionManifest("ses_dup", "sessions/ses_a.json", "ses_dup", "sessions/ses_b.json"),
		"sessions/ses_a.json": `{"info":{"id":"ses_dup"},"messages":[{"id":"msg_a"}]}`,
		"sessions/ses_b.json": `{"info":{"id":"ses_dup"},"messages":[{"id":"msg_b"}]}`,
	}, nil)
	st := openStore(t, ws)
	fake := &fakeImportClient{}

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions, DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate session id "ses_dup"`) {
		t.Fatalf("Import(dry-run) error = %v, want duplicate session id rejection", err)
	}
	_, err = Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate session id "ses_dup"`) {
		t.Fatalf("Import() error = %v, want duplicate session id rejection", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("issued %d import calls, want 0", len(fake.calls))
	}
}

// TestImportRejectsDuplicateMessageIDsAcrossPayloads: the same message id in
// two payloads would collide inside the service, so both imports are rejected
// before any import request — dry-run included.
func TestImportRejectsDuplicateMessageIDsAcrossPayloads(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json":       twoSessionManifest("ses_a", "sessions/ses_a.json", "ses_b", "sessions/ses_b.json"),
		"sessions/ses_a.json": `{"info":{"id":"ses_a"},"messages":[{"id":"msg_dup","text":"one"}]}`,
		"sessions/ses_b.json": `{"info":{"id":"ses_b"},"messages":[{"id":"msg_dup","text":"two"}]}`,
	}, nil)
	st := openStore(t, ws)
	fake := &fakeImportClient{}

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions, DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate message id "msg_dup"`) {
		t.Fatalf("Import(dry-run) error = %v, want duplicate message id rejection", err)
	}
	_, err = Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate message id "msg_dup"`) {
		t.Fatalf("Import() error = %v, want duplicate message id rejection", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("issued %d import calls, want 0", len(fake.calls))
	}
}

// TestImportRejectsPayloadSessionIDMismatch: a payload whose info.id does not
// match its manifest source id would map the thread to the wrong session, so
// it is rejected before any import request — dry-run included.
func TestImportRejectsPayloadSessionIDMismatch(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json":         manifestJSON(),
		"sessions/ses_src.json": `{"info":{"id":"ses_other"},"messages":[{"id":"msg_1"}]}`,
	}, nil)
	st := openStore(t, ws)
	fake := &fakeImportClient{}

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions, DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match the manifest session id") {
		t.Fatalf("Import(dry-run) error = %v, want info.id mismatch rejection", err)
	}
	_, err = Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match the manifest session id") {
		t.Fatalf("Import() error = %v, want info.id mismatch rejection", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("issued %d import calls, want 0", len(fake.calls))
	}
}
