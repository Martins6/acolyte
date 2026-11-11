package traceimport

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/traceexport"
)

// roundTripService is an in-memory fake of the OpenCode endpoints a round
// trip needs: session import, session export, session listing, and message
// pagination. It mimics the real service's conflict behavior — importing an
// existing session id returns 409.
type roundTripService struct {
	mu       sync.Mutex
	sessions map[string]roundTripSession
}

type roundTripSession struct {
	info     map[string]any
	messages []any
}

func newRoundTripService(t *testing.T) (*roundTripService, *opencode.Client) {
	t.Helper()
	svc := &roundTripService{sessions: map[string]roundTripSession{}}
	server := httptest.NewServer(svc)
	t.Cleanup(server.Close)
	return svc, opencode.NewClient(server.URL, "")
}

func (s *roundTripService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/import":
		s.handleImport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/session":
		s.handleList(w)
	case strings.HasPrefix(r.URL.Path, "/api/experimental/session/") && strings.HasSuffix(r.URL.Path, "/export"):
		s.handleExport(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/session/") && strings.Contains(r.URL.Path, "/message"):
		s.handleMessages(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *roundTripService) handleImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Info     map[string]any `json:"info"`
		Messages []any          `json:"messages"`
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := body.Info["id"].(string)
	if id == "" {
		http.Error(w, `{"message":"missing id"}`, http.StatusBadRequest)
		return
	}
	if _, exists := s.sessions[id]; exists {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"_tag":"ConflictError","message":"Session already exists: ` + id + `"}`))
		return
	}
	if body.Location.Directory != "" {
		body.Info["location"] = map[string]any{"directory": body.Location.Directory}
	}
	s.sessions[id] = roundTripSession{info: body.Info, messages: body.Messages}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": body.Info})
}

func (s *roundTripService) handleList(w http.ResponseWriter) {
	infos := make([]map[string]any, 0, len(s.sessions))
	for _, session := range s.sessions {
		infos = append(infos, session.info)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": infos})
}

func (s *roundTripService) handleExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/experimental/session/"), "/export")
	session, ok := s.sessions[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"not found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"info": session.info, "messages": session.messages},
	})
}

func (s *roundTripService) handleMessages(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/message")
	session, ok := s.sessions[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"not found"}`))
		return
	}
	// The client walks newest-first and reverses the page, so serve the
	// stored chronological messages in reverse.
	newestFirst := make([]any, 0, len(session.messages))
	for i := len(session.messages) - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, session.messages[i])
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data":   newestFirst,
		"cursor": map[string]any{"next": nil},
	})
}

// messageTexts extracts the visible text of each stored message in order.
func messageTexts(messages []any) []string {
	var texts []string
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := message["text"].(string); ok && text != "" {
			texts = append(texts, text)
			continue
		}
		if content, ok := message["content"].([]any); ok {
			for _, part := range content {
				partMap, ok := part.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := partMap["text"].(string); ok && text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	return texts
}

// TestRoundTripImportExportImport proves that a fixture session survives
// import -> export -> re-import against a fake OpenCode service: the
// messages of the twice-imported session must be identical to the fixture.
func TestRoundTripImportExportImport(t *testing.T) {
	t.Parallel()

	svc, client := newRoundTripService(t)
	fixtureTexts := []string{"hi", "hello"}

	// Step 1: import the fixture trace into workspace A.
	wsA := tempWorkspace(t)
	zipA := filepath.Join(t.TempDir(), "a.zip")
	buildTraceZip(t, zipA)
	stA := openStore(t, wsA)

	first, err := Import(context.Background(), Request{
		Agent: agentFor(wsA), Store: stA, OpenCode: client, ZipPath: zipA, Mode: ModeSessions,
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first.Sessions) != 1 {
		t.Fatalf("first import sessions = %+v, want 1", first.Sessions)
	}
	importedID := first.Sessions[0].SessionID
	if got := messageTexts(svc.sessions[importedID].messages); !equalStrings(got, fixtureTexts) {
		t.Fatalf("imported texts = %v, want %v", got, fixtureTexts)
	}

	// Step 2: export workspace A, producing a fresh trace zip.
	zipB := filepath.Join(t.TempDir(), "b.zip")
	exported, err := traceexport.Export(context.Background(), traceexport.Request{
		Agent: agentFor(wsA), Store: stA, OpenCode: client, OutPath: zipB, MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exported.Sessions != 1 || exported.Messages != 2 {
		t.Fatalf("export result = %+v, want 1 session / 2 messages", exported)
	}
	files := readZipFiles(t, zipB)
	if _, ok := files["sessions/"+importedID+".json"]; !ok {
		t.Fatalf("export missing native payload for %s: %v", importedID, keysOf(files))
	}

	// Step 3: re-import the exported trace into workspace B. The source
	// session still exists in the fake service, so the importer must
	// rewrite the ids and retry.
	wsB := tempWorkspace(t)
	stB := openStore(t, wsB)
	second, err := Import(context.Background(), Request{
		Agent: agentFor(wsB), Store: stB, OpenCode: client, ZipPath: zipB, Mode: ModeSessions,
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(second.Sessions) != 1 {
		t.Fatalf("second import sessions = %+v, want 1", second.Sessions)
	}
	reimportedID := second.Sessions[0].SessionID
	if reimportedID == importedID {
		t.Errorf("re-import kept the conflicting session id %s", reimportedID)
	}

	// Step 4: the messages must have survived the whole round trip.
	svc.mu.Lock()
	got := messageTexts(svc.sessions[reimportedID].messages)
	svc.mu.Unlock()
	if !equalStrings(got, fixtureTexts) {
		t.Errorf("round-trip texts = %v, want %v", got, fixtureTexts)
	}

	// And workspace B's store must map the source session to the new id.
	thread := "memory:" + second.TraceID + ":ses_src"
	mapped, ok, err := stB.Orch().GetSessionByThread(thread)
	if err != nil || !ok {
		t.Fatalf("GetSessionByThread(%q) = %v, %v", thread, ok, err)
	}
	if mapped.OpenCodeSessionID != reimportedID {
		t.Errorf("mapped session = %q, want %q", mapped.OpenCodeSessionID, reimportedID)
	}
}

// readZipFiles reads every entry of a zip into a map (test helper).
func readZipFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()
	files := map[string]string{}
	for _, f := range reader.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", f.Name, err)
		}
		files[f.Name] = string(raw)
	}
	return files
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
