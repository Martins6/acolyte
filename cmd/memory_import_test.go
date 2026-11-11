package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/traceimport"
)

// buildImportZip writes a minimal trace zip for command tests.
func buildImportZip(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer out.Close()

	manifest := `{
  "format": "acolyte-memory-trace/v1",
  "agent": {"name": "source", "workspace": "/source"},
  "exported_at": "2026-11-10T12:00:00Z",
  "sessions": [
    {"id": "ses_src", "title": "t", "messages": 1, "markdown": "sessions/ses_src.md", "native_json": "sessions/ses_src.json"}
  ],
  "counts": {"sessions": 1, "messages": 1}
}`
	native := `{"info":{"id":"ses_src","agent":"build","projectID":"p","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1794340000000,"updated":1794340001000},"location":{"directory":"/source"}},"messages":[{"id":"msg_1","type":"user","time":{"created":1794340000000},"text":"hi"}]}`

	w := zip.NewWriter(out)
	writeZipEntry(t, w, "manifest.json", manifest)
	writeZipEntry(t, w, "README.md", "# trace\n")
	writeZipEntry(t, w, "sessions/ses_src.json", native)
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

func writeZipEntry(t *testing.T, w *zip.Writer, name, content string) {
	t.Helper()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(fs.FileMode(0o644))
	entry, err := w.CreateHeader(header)
	if err != nil {
		t.Fatalf("create entry %q: %v", name, err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatalf("write entry %q: %v", name, err)
	}
}

type importCLIFixture struct {
	home string
	ws   string
	zip  string
}

// setupImportCLI registers a fresh agent for import command tests.
func setupImportCLI(t *testing.T) importCLIFixture {
	t.Helper()
	ws := setupCronCLI(t) // sets ACOLYTE_HOME and registers agent "valet"
	return importCLIFixture{home: os.Getenv("ACOLYTE_HOME"), ws: ws, zip: buildImportZip(t)}
}

func TestRunMemoryImportTraceMode(t *testing.T) {
	f := setupImportCLI(t)

	var out bytes.Buffer
	err := runMemoryImport(context.Background(), &out, memoryImportOptions{
		ZipPath: f.zip, AgentName: "valet", Mode: "trace",
	})
	if err != nil {
		t.Fatalf("runMemoryImport() error: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Imported trace") {
		t.Errorf("output = %q, want summary", out.String())
	}
	entries, err := os.ReadDir(filepath.Join(f.ws, "downloads", "memory-traces"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("trace folders = %v, %v, want exactly one", entries, err)
	}
	index := filepath.Join(f.ws, ".acolyte", "MEMORY-TRACES.md")
	if _, err := os.Stat(index); err != nil {
		t.Errorf("index not written: %v", err)
	}
}

func TestRunMemoryImportJSONOutput(t *testing.T) {
	f := setupImportCLI(t)

	var out bytes.Buffer
	err := runMemoryImport(context.Background(), &out, memoryImportOptions{
		ZipPath: f.zip, AgentName: "valet", Mode: "trace", JSON: true,
	})
	if err != nil {
		t.Fatalf("runMemoryImport() error: %v", err)
	}
	var result struct {
		TraceID string `json:"trace_id"`
		Mode    string `json:"mode"`
		Files   int    `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out.String())
	}
	if result.TraceID == "" || result.Mode != "trace" || result.Files != 3 {
		t.Errorf("result = %+v", result)
	}
}

func TestRunMemoryImportDryRun(t *testing.T) {
	f := setupImportCLI(t)

	var out bytes.Buffer
	err := runMemoryImport(context.Background(), &out, memoryImportOptions{
		ZipPath: f.zip, AgentName: "valet", Mode: "trace", DryRun: true,
	})
	if err != nil {
		t.Fatalf("runMemoryImport(dry-run) error: %v", err)
	}
	if !strings.Contains(out.String(), "Dry run") {
		t.Errorf("output = %q, want dry run header", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.ws, "downloads", "memory-traces")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run wrote files (stat: %v)", err)
	}
}

func TestRunMemoryImportSessions(t *testing.T) {
	f := setupImportCLI(t)
	st, err := store.Open(f.ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	fake := &fakeImportClient{}
	withFakeImport(t, fake)

	var out bytes.Buffer
	err = runMemoryImport(context.Background(), &out, memoryImportOptions{
		ZipPath: f.zip, AgentName: "valet", Mode: "sessions",
		OpenStore: func(string) (*store.Store, error) { return st, nil },
	})
	if err != nil {
		t.Fatalf("runMemoryImport(sessions) error: %v\n%s", err, out.String())
	}
	if fake.calls != 1 {
		t.Fatalf("ImportSession calls = %d, want 1", fake.calls)
	}
	if !strings.Contains(out.String(), "1 imported") {
		t.Errorf("output = %q, want import summary", out.String())
	}
}

func TestRunMemoryImportSessionsUnavailableOpenCode(t *testing.T) {
	f := setupImportCLI(t)
	st, err := store.Open(f.ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	withFakeImportFailure(t, errors.New("connection refused"))

	err = runMemoryImport(context.Background(), &bytes.Buffer{}, memoryImportOptions{
		ZipPath: f.zip, AgentName: "valet", Mode: "sessions",
		OpenStore: func(string) (*store.Store, error) { return st, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "OpenCode service unavailable") {
		t.Fatalf("error = %v, want clear OpenCode unavailable error", err)
	}
}

func TestRunMemoryImportValidation(t *testing.T) {
	setupImportCLI(t)

	tests := []memoryImportOptions{
		{}, // missing zip
		{ZipPath: "/no/such/file.zip", Mode: "trace"}, // missing file
		{ZipPath: buildImportZip(t), Mode: "bogus"},   // bad mode
	}
	for _, opts := range tests {
		if err := runMemoryImport(context.Background(), &bytes.Buffer{}, opts); err == nil {
			t.Errorf("runMemoryImport(%+v) error = nil, want failure", opts)
		}
	}

	// Unregistered agent.
	err := runMemoryImport(context.Background(), &bytes.Buffer{}, memoryImportOptions{
		ZipPath: buildImportZip(t), AgentName: "ghost",
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("unregistered agent error = %v", err)
	}
}

func TestRunMemoryImportRefusesExistingTrace(t *testing.T) {
	f := setupImportCLI(t)

	opts := memoryImportOptions{ZipPath: f.zip, AgentName: "valet", Mode: "trace"}
	if err := runMemoryImport(context.Background(), &bytes.Buffer{}, opts); err != nil {
		t.Fatalf("first import: %v", err)
	}
	err := runMemoryImport(context.Background(), &bytes.Buffer{}, opts)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second import error = %v, want --force hint", err)
	}
	opts.Force = true
	if err := runMemoryImport(context.Background(), &bytes.Buffer{}, opts); err != nil {
		t.Fatalf("forced import: %v", err)
	}
}

// fakeImportClient implements traceimport.OpenCode for command tests.
type fakeImportClient struct {
	calls int
}

func (f *fakeImportClient) ImportSession(_ context.Context, _ json.RawMessage, _ string) (*opencode.SessionInfo, error) {
	f.calls++
	return &opencode.SessionInfo{ID: "ses_imported"}, nil
}

func withFakeImport(t *testing.T, fake *fakeImportClient) {
	t.Helper()
	old := openCodeImportEnsure
	openCodeImportEnsure = func(context.Context) (traceimport.OpenCode, error) { return fake, nil }
	t.Cleanup(func() { openCodeImportEnsure = old })
}

func withFakeImportFailure(t *testing.T, err error) {
	t.Helper()
	old := openCodeImportEnsure
	openCodeImportEnsure = func(context.Context) (traceimport.OpenCode, error) { return nil, err }
	t.Cleanup(func() { openCodeImportEnsure = old })
}
