package traceimport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

func agentFor(ws string) registry.Agent {
	return registry.Agent{Name: "valet", Workspace: ws}
}

// openStore opens the per-agent store for a workspace test fixture.
func openStore(t *testing.T, ws string) *store.Store {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeTrace, false},
		{"trace", ModeTrace, false},
		{"sessions", ModeSessions, false},
		{"all", ModeAll, false},
		{" TRACE ", ModeTrace, false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseMode(%q) = %v, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q) error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestImportTraceModeExtractsAndIndexes(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)

	result, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
	if err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if result.TraceID == "" || len(result.TraceID) != 16 {
		t.Fatalf("trace id = %q, want 16 hex chars", result.TraceID)
	}
	if result.Files != 5 {
		t.Errorf("files = %d, want 5", result.Files)
	}
	if !result.IndexUpdated {
		t.Error("index_updated = false, want true")
	}
	if result.SourceAgent != "valet" {
		t.Errorf("source agent = %q, want valet", result.SourceAgent)
	}
	if !result.ExportedAt.Equal(exportedAt) {
		t.Errorf("exported_at = %v, want %v", result.ExportedAt, exportedAt)
	}

	folder := traceFolder(ws, result.TraceID)
	for _, name := range []string{
		"manifest.json", "README.md", "sessions/ses_src.md", "sessions/ses_src.json", "session-index.md",
	} {
		if _, err := os.Stat(filepath.Join(folder, filepath.FromSlash(name))); err != nil {
			t.Errorf("extracted file %s: %v", name, err)
		}
	}

	index := readAll(t, indexFile(ws))
	for _, want := range []string{
		indexStartMark, indexEndMark,
		"`" + result.TraceID + "`", "valet", exportedAt.Format(time.RFC3339),
		"`downloads/memory-traces/" + result.TraceID + "`",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index missing %q:\n%s", want, index)
		}
	}
}

func TestImportTraceModeIsDeterministic(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)

	first, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
	if err != nil {
		t.Fatalf("first Import() error: %v", err)
	}
	second, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace, Force: true})
	if err != nil {
		t.Fatalf("second Import() error: %v", err)
	}
	if first.TraceID != second.TraceID {
		t.Errorf("trace id not deterministic: %q vs %q", first.TraceID, second.TraceID)
	}
	// The index row is identical after a re-import of the same content, so
	// the file must be left untouched rather than rewritten.
	if second.IndexUpdated {
		t.Error("index_updated = true after identical re-import, want false")
	}
	index := readAll(t, indexFile(ws))
	if got := countOccurrences(index, "`"+first.TraceID+"`"); got != 1 {
		t.Errorf("index has %d rows for trace %s, want 1:\n%s", got, first.TraceID, index)
	}
}

func TestImportRefusesExistingTraceWithoutForce(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)

	if _, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace}); err != nil {
		t.Fatalf("first Import() error: %v", err)
	}
	_, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second Import() error = %v, want --force hint", err)
	}
}

func TestImportRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries map[string]string
	}{
		{"parent traversal", map[string]string{"../evil.txt": "x"}},
		{"nested traversal", map[string]string{"sessions/../../evil.txt": "x"}},
		{"absolute path", map[string]string{"/etc/evil.txt": "x"}},
		{"backslash traversal", map[string]string{`..\..\evil.txt`: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ws := tempWorkspace(t)
			zipPath := filepath.Join(t.TempDir(), "trace.zip")
			files := map[string]string{"manifest.json": manifestJSON()}
			for k, v := range tt.entries {
				files[k] = v
			}
			buildZip(t, zipPath, files, nil)

			_, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
			if err == nil {
				t.Fatal("Import() error = nil, want rejection")
			}
			if _, statErr := os.Stat(filepath.Join(ws, "evil.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("escaped file exists (stat: %v)", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(ws), "evil.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("file escaped workspace (stat: %v)", statErr)
			}
		})
	}
}

func TestImportRejectsSymlinkEntries(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{"manifest.json": manifestJSON()},
		map[string]string{"evil-link": "/etc/passwd"})

	_, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Import() error = %v, want symlink rejection", err)
	}
	if _, err := os.Lstat(filepath.Join(traceFolder(ws, "x"), "evil-link")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink was materialized (stat: %v)", err)
	}
}

func TestImportEnforcesSizeCaps(t *testing.T) {
	// Not parallel: this test mutates the package-level caps.
	origFile, origTotal := maxFileBytes, maxTotalBytes
	t.Cleanup(func() { maxFileBytes, maxTotalBytes = origFile, origTotal })

	t.Run("per-file cap", func(t *testing.T) {
		maxFileBytes = 16
		ws := tempWorkspace(t)
		zipPath := filepath.Join(t.TempDir(), "trace.zip")
		buildZip(t, zipPath, map[string]string{
			"manifest.json": manifestJSON(),
			"big.txt":       strings.Repeat("x", 64),
		}, nil)

		_, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
		if err == nil || !strings.Contains(err.Error(), "bytes") {
			t.Fatalf("Import() error = %v, want size error", err)
		}
	})

	t.Run("total cap", func(t *testing.T) {
		maxFileBytes, maxTotalBytes = 1024, 100
		ws := tempWorkspace(t)
		zipPath := filepath.Join(t.TempDir(), "trace.zip")
		buildZip(t, zipPath, map[string]string{
			"manifest.json": manifestJSON(),
			"a.txt":         strings.Repeat("x", 80),
			"b.txt":         strings.Repeat("y", 80),
		}, nil)

		_, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
		if err == nil || !strings.Contains(err.Error(), "uncompressed") {
			t.Fatalf("Import() error = %v, want total size error", err)
		}
	})

	t.Run("readEntry enforces per-file cap", func(t *testing.T) {
		// Large enough for manifest.json (read by openTrace) but smaller
		// than the native session payload.
		maxFileBytes = 400
		ws := tempWorkspace(t)
		zipPath := filepath.Join(t.TempDir(), "trace.zip")
		buildTraceZip(t, zipPath)
		bundle, err := openTrace(zipPath)
		if err != nil {
			t.Fatalf("openTrace: %v", err)
		}
		defer bundle.Close()
		_ = ws
		if _, err := bundle.readEntry("sessions/ses_src.json"); err == nil || !strings.Contains(err.Error(), "bytes") {
			t.Fatalf("readEntry() error = %v, want size error", err)
		}
	})
}

func TestImportDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)
	st := openStore(t, ws)

	result, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, ZipPath: zipPath, Mode: ModeAll, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Import(dry-run) error: %v", err)
	}
	if !result.DryRun {
		t.Error("dry_run = false, want true")
	}
	if len(result.Actions) == 0 {
		t.Fatal("no planned actions")
	}
	joined := strings.Join(result.Actions, "\n")
	if !strings.Contains(joined, "extract") || !strings.Contains(joined, "import session ses_src") {
		t.Errorf("actions = %v, want extract and session import plans", result.Actions)
	}

	if _, err := os.Stat(traceFolder(ws, result.TraceID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run extracted the trace (stat: %v)", err)
	}
	if _, err := os.Stat(indexFile(ws)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run wrote the index (stat: %v)", err)
	}
	if len(result.Sessions) != 0 {
		t.Errorf("dry-run imported sessions: %+v", result.Sessions)
	}
}

func TestImportSessionsMapsThreads(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)
	st := openStore(t, ws)
	fake := &fakeImportClient{}

	result, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
		Now: exportedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("ImportSession calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].workspace != ws {
		t.Errorf("import workspace = %q, want %q", fake.calls[0].workspace, ws)
	}
	if !strings.Contains(fake.calls[0].raw, `"id":"ses_src"`) {
		t.Errorf("payload missing source info: %s", fake.calls[0].raw)
	}

	if len(result.Sessions) != 1 {
		t.Fatalf("imported sessions = %+v, want 1", result.Sessions)
	}
	session := result.Sessions[0]
	wantThread := "memory:" + result.TraceID + ":ses_src"
	if session.Thread != wantThread {
		t.Errorf("thread = %q, want %q", session.Thread, wantThread)
	}
	if session.SessionID != "ses_imported" {
		t.Errorf("session id = %q, want ses_imported", session.SessionID)
	}

	mapped, ok, err := st.Orch().GetSessionByThread(wantThread)
	if err != nil || !ok {
		t.Fatalf("GetSessionByThread(%q) = %v, %v", wantThread, ok, err)
	}
	if mapped.OpenCodeSessionID != "ses_imported" {
		t.Errorf("mapped session = %q, want ses_imported", mapped.OpenCodeSessionID)
	}
}

func TestImportSessionsSkipsMappedUnlessForce(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)
	st := openStore(t, ws)
	fake := &fakeImportClient{}

	first, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err != nil {
		t.Fatalf("first Import() error: %v", err)
	}

	second, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err != nil {
		t.Fatalf("second Import() error: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("ImportSession calls after re-import = %d, want 1 (skip)", len(fake.calls))
	}
	if len(second.Skipped) != 1 || second.Skipped[0] != "ses_src" {
		t.Errorf("skipped = %v, want [ses_src]", second.Skipped)
	}
	_ = first

	third, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions, Force: true,
	})
	if err != nil {
		t.Fatalf("forced Import() error: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Errorf("ImportSession calls after --force = %d, want 2", len(fake.calls))
	}
	if len(third.Sessions) != 1 {
		t.Errorf("forced imported = %+v, want 1", third.Sessions)
	}
	// The mapping must follow the forced re-import.
	thread := "memory:" + third.TraceID + ":ses_src"
	mapped, ok, err := st.Orch().GetSessionByThread(thread)
	if err != nil || !ok {
		t.Fatalf("GetSessionByThread(%q) = %v, %v", thread, ok, err)
	}
	if mapped.OpenCodeSessionID != "ses_imported" {
		t.Errorf("mapped session = %q, want ses_imported", mapped.OpenCodeSessionID)
	}
}

func TestImportSessionsRewritesIDsOnConflict(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)
	st := openStore(t, ws)
	fake := &fakeImportClient{
		errs: []error{
			&opencode.APIError{Status: 409, Message: "Session already exists"},
		},
	}

	result, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("ImportSession calls = %d, want 2 (retry after conflict)", len(fake.calls))
	}
	if strings.Contains(fake.calls[1].raw, `"id":"ses_src"`) {
		t.Errorf("retry did not rewrite the session id: %s", fake.calls[1].raw)
	}
	if strings.Contains(fake.calls[1].raw, `"id":"msg_1"`) {
		t.Errorf("retry did not rewrite message ids: %s", fake.calls[1].raw)
	}
	if !strings.Contains(fake.calls[1].raw, `"text":"hi"`) {
		t.Errorf("retry lost message content: %s", fake.calls[1].raw)
	}
	if len(result.Sessions) != 1 {
		t.Errorf("imported = %+v, want 1", result.Sessions)
	}
}

func TestImportSessionsDropsMissingParentOnNotFound(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json":         manifestJSON(),
		"sessions/ses_src.json": `{"info":{"id":"ses_src","parentID":"ses_gone","agent":"build","projectID":"p","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1794340000000,"updated":1794340001000},"location":{"directory":"/source/ws"}},"messages":[]}`,
	}, nil)
	st := openStore(t, ws)
	fake := &fakeImportClient{
		errs: []error{
			&opencode.APIError{Status: 404, Message: "Session not found: ses_gone"},
		},
	}

	if _, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, OpenCode: fake, ZipPath: zipPath, Mode: ModeSessions,
	}); err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("ImportSession calls = %d, want 2", len(fake.calls))
	}
	if strings.Contains(fake.calls[1].raw, "parentID") {
		t.Errorf("retry kept parentID: %s", fake.calls[1].raw)
	}
}

func TestImportSessionsFailsWithoutOpenCode(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)
	st := openStore(t, ws)

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, ZipPath: zipPath, Mode: ModeSessions,
	})
	if err == nil || !strings.Contains(err.Error(), "OpenCode") {
		t.Fatalf("Import() error = %v, want OpenCode required", err)
	}
}

func TestImportDryRunRejectsMissingNativeEntry(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	// Manifest points at a native payload that is not in the zip.
	buildZip(t, zipPath, map[string]string{
		"manifest.json":       manifestJSON(),
		"sessions/ses_src.md": "# hi\n",
	}, nil)
	st := openStore(t, ws)

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), Store: st, ZipPath: zipPath, Mode: ModeSessions, DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Import(dry-run) error = %v, want missing entry error", err)
	}
}

func TestImportSessionsWithoutStore(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)

	_, err := Import(context.Background(), Request{
		Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeSessions,
	})
	if err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("Import() error = %v, want store required", err)
	}
}

func TestImportRejectsBadZips(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		make func(t *testing.T) string
	}{
		{"missing file", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "nope.zip")
		}},
		{"not a zip", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "bad.zip")
			if err := os.WriteFile(path, []byte("this is not a zip"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			return path
		}},
		{"missing manifest", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "nomanifest.zip")
			buildZip(t, path, map[string]string{"README.md": "# hi\n"}, nil)
			return path
		}},
		{"bad manifest format", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "badformat.zip")
			buildZip(t, path, map[string]string{
				"manifest.json": `{"format":"something-else/v1"}`,
			}, nil)
			return path
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ws := tempWorkspace(t)
			_, err := Import(context.Background(), Request{
				Agent: agentFor(ws), ZipPath: tt.make(t), Mode: ModeTrace,
			})
			if err == nil {
				t.Fatal("Import() error = nil, want failure")
			}
		})
	}
}

func TestImportTraceModeNeedsNoStoreOrOpenCode(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildTraceZip(t, zipPath)

	result, err := Import(context.Background(), Request{Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace})
	if err != nil {
		t.Fatalf("Import() error: %v", err)
	}
	if result.Files == 0 {
		t.Error("no files extracted")
	}
}

func TestTraceIDForDependsOnContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "a.zip")
	second := filepath.Join(dir, "b.zip")
	buildTraceZip(t, first)
	buildTraceZip(t, second)

	idA, err := traceIDFor(first)
	if err != nil {
		t.Fatalf("traceIDFor: %v", err)
	}
	idB, err := traceIDFor(second)
	if err != nil {
		t.Fatalf("traceIDFor: %v", err)
	}
	if idA != idB {
		t.Errorf("same content produced different ids: %q vs %q", idA, idB)
	}
	if _, err := traceIDFor(filepath.Join(dir, "missing.zip")); err == nil {
		t.Error("traceIDFor(missing) error = nil, want failure")
	}
}

// jsonRoundTrip decodes and re-encodes to assert payload validity.
func jsonRoundTrip(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, raw)
	}
	return out
}
