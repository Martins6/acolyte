package traceimport

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
)

// buildZip writes a zip at path with the given entries. Names ending in "/"
// become directory entries; a name mapped to symlinkTarget creates a symlink
// entry pointing at symlinkTarget.
func buildZip(t *testing.T, path string, files map[string]string, symlinks map[string]string) {
	t.Helper()
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer out.Close()
	w := zip.NewWriter(out)
	for _, name := range sortedKeys(files) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(fs.FileMode(0o644))
		entry, err := w.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			t.Fatalf("write entry %q: %v", name, err)
		}
	}
	for _, name := range sortedKeys(symlinks) {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(fs.ModeSymlink | 0o777)
		entry, err := w.CreateHeader(header)
		if err != nil {
			t.Fatalf("create symlink entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(symlinks[name])); err != nil {
			t.Fatalf("write symlink entry %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// manifestJSON renders a minimal valid export manifest with one session that
// has a native payload.
func manifestJSON() string {
	return `{
  "format": "acolyte-memory-trace/v1",
  "agent": {"name": "valet", "workspace": "/source/ws"},
  "exported_at": "2026-11-10T12:00:00Z",
  "sessions": [
    {"id": "ses_src", "title": "hello", "messages": 2, "markdown": "sessions/ses_src.md", "native_json": "sessions/ses_src.json"}
  ],
  "counts": {"sessions": 1, "messages": 2}
}`
}

// nativePayload renders an OpenCode-native transfer payload for ses_src.
func nativePayload() string {
	return `{"info":{"id":"ses_src","agent":"build","title":"hello","projectID":"p","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1794340000000,"updated":1794340001000},"location":{"directory":"/source/ws"}},"messages":[{"id":"msg_1","type":"user","time":{"created":1794340000000},"text":"hi"},{"id":"msg_2","type":"assistant","time":{"created":1794340001000,"completed":1794340002000},"agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"text","text":"hello"}],"finish":"stop"}]}`
}

// buildTraceZip writes a complete minimal trace zip at path.
func buildTraceZip(t *testing.T, path string) {
	t.Helper()
	buildZip(t, path, map[string]string{
		"manifest.json":         manifestJSON(),
		"README.md":             "# trace\n",
		"sessions/ses_src.md":   "# ses_src\nhi\nhello\n",
		"sessions/ses_src.json": nativePayload(),
		"session-index.md":      "| Session |\n| --- |\n",
	}, nil)
}

// fakeImportClient records ImportSession calls and returns scripted results.
type fakeImportClient struct {
	calls    []fakeImportCall
	infos    []*fakeSessionInfo // popped in order; nil entries synthesize from the payload
	errs     []error            // popped in order; nil means success
	exported []json.RawMessage
}

type fakeImportCall struct {
	raw       string
	workspace string
}

type fakeSessionInfo struct {
	id string
}

func (f *fakeImportClient) ImportSession(_ context.Context, raw json.RawMessage, workspace string) (*opencode.SessionInfo, error) {
	f.calls = append(f.calls, fakeImportCall{raw: string(raw), workspace: workspace})
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	if err != nil {
		return nil, err
	}
	id := "ses_imported"
	if len(f.infos) > 0 {
		info := f.infos[0]
		f.infos = f.infos[1:]
		if info != nil && info.id != "" {
			id = info.id
		}
	}
	return &opencode.SessionInfo{ID: id}, nil
}

// tempWorkspace returns a workspace directory for import tests.
func tempWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "downloads"), 0o755); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	return ws
}

// readAll reads a file or fails the test.
func readAll(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func mustJSON(t *testing.T, raw string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}

func traceFolder(workspace, traceID string) string {
	return filepath.Join(workspace, "downloads", "memory-traces", traceID)
}

func indexFile(workspace string) string {
	return filepath.Join(workspace, ".acolyte", "MEMORY-TRACES.md")
}

// exportedAt is the fixed timestamp used by test manifests.
var exportedAt = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// countOccurrences counts non-overlapping occurrences of sub in s.
func countOccurrences(s, sub string) int {
	return strings.Count(s, sub)
}
