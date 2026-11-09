package routine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

func routineTestClient(t *testing.T, handler http.HandlerFunc) *opencode.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return opencode.NewClient(server.URL, "pw")
}

func writeRoutineFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDreamRoutineDirectWritesAndAudit(t *testing.T) {
	ws := t.TempDir()
	writeRoutineFile(t, filepath.Join(ws, ".acolyte", "SOUL.md"), "old soul")
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var prompt string
	now := time.Now().UTC()
	client := routineTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprintf(w, `{"data":[
				{"id":"ses_user","title":"Work","time":{"updated":%d},"location":{"directory":%q}},
				{"id":"ses_old","time":{"updated":%d},"location":{"directory":%q}},
				{"id":"ses_other","time":{"updated":%d},"location":{"directory":"/other"}}
			]}`, now.UnixMilli(), ws, now.Add(-30*24*time.Hour).UnixMilli(), ws, now.UnixMilli())
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_user/message":
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user","text":"remember this"},{"id":"m2","type":"assistant","content":[{"type":"text","text":"ok"}]}],"cursor":{"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_dream":
			fmt.Fprint(w, `{"data":{"id":"ses_dream","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_dream/synthetic":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompt, _ = body["text"].(string)
			writeRoutineFile(t, filepath.Join(ws, ".acolyte", "SOUL.md"), "new soul")
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_dream","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_dream/wait":
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_dream/message":
			fmt.Fprint(w, `{"data":[{"id":"msg_out","type":"assistant","content":[{"type":"text","text":"updated soul"}]},{"id":"msg_in","type":"synthetic","text":"prompt"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	env := Env{
		Agent: registry.Agent{Name: "valet", Workspace: ws}, Workspace: ws,
		Thread: "cron:dream", SessionID: "ses_dream", Meta: map[string]any{},
		Now: func() time.Time { return now }, Store: st, OpenCode: client,
		Log: func(string, string, map[string]any) {},
	}
	if _, err := (dreamRoutine{}).Run(context.Background(), env, map[string]any{"days": 3}); err != nil {
		t.Fatalf("dream Run() error: %v", err)
	}
	if !strings.Contains(prompt, "remember this") || !strings.Contains(prompt, "super extra clear") {
		t.Errorf("prompt = %s", prompt)
	}
	if strings.Contains(prompt, "DRY RUN") {
		t.Error("direct prompt unexpectedly contains DRY RUN")
	}
	var scanned, skills, dryRun int
	var note string
	if err := st.DB().QueryRow(`SELECT sessions_scanned, skills_created, dry_run, note FROM mem_dreams`).Scan(&scanned, &skills, &dryRun, &note); err != nil {
		t.Fatalf("read dream: %v", err)
	}
	if scanned != 1 || skills != 0 || dryRun != 0 {
		t.Errorf("dream row = scanned:%d skills:%d dry:%d", scanned, skills, dryRun)
	}
	if !strings.Contains(note, ".acolyte/SOUL.md") {
		t.Errorf("note does not record changed file: %s", note)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:dream")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_dream" {
		t.Errorf("session = %+v, %v, %v", session, ok, err)
	}
}

func TestDreamRoutineDryRunAndNoSessions(t *testing.T) {
	ws := t.TempDir()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	now := time.Now().UTC()
	var prompt string
	client := routineTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprintf(w, `{"data":[{"id":"ses_user","time":{"updated":%d},"location":{"directory":%q}}]}`, now.UnixMilli(), ws)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_user/message":
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user","text":"hello"}],"cursor":{"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_dream":
			fmt.Fprint(w, `{"data":{"id":"ses_dream"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_dream/synthetic":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompt, _ = body["text"].(string)
			writeRoutineFile(t, filepath.Join(ws, ".acolyte", "SOUL.md"), "violation")
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_dream","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_dream/wait":
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_dream/message":
			fmt.Fprint(w, `{"data":[{"id":"msg_out","type":"assistant","content":[{"type":"text","text":"proposal"}]},{"id":"msg_in","type":"synthetic","text":"prompt"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	env := Env{
		Agent: registry.Agent{Name: "valet", Workspace: ws}, Workspace: ws,
		Thread: "cron:dream", SessionID: "ses_dream", Meta: map[string]any{"dry_run": true},
		Now: func() time.Time { return now }, Store: st, OpenCode: client,
		Log: func(string, string, map[string]any) {},
	}
	if _, err := (dreamRoutine{}).Run(context.Background(), env, map[string]any{"days": 3}); err != nil {
		t.Fatalf("dream dry-run error: %v", err)
	}
	if !strings.Contains(prompt, "DRY RUN") || !strings.Contains(prompt, "do not modify") {
		t.Errorf("dry-run prompt = %s", prompt)
	}
	var dryRun int
	var note string
	if err := st.DB().QueryRow(`SELECT dry_run, note FROM mem_dreams`).Scan(&dryRun, &note); err != nil {
		t.Fatalf("read dream: %v", err)
	}
	if dryRun != 1 || !strings.Contains(note, "DRY RUN") || !strings.Contains(note, ".acolyte/SOUL.md") {
		t.Errorf("dry-run row = dry:%d note:%s", dryRun, note)
	}
}
