package routine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

func TestHeartbeatPromptGuidesNotification(t *testing.T) {
	ws := t.TempDir()
	writeRoutineFile(t, filepath.Join(ws, ".acolyte", "HEARTBEAT.md"), "Watch for urgent mail.")
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if _, err := st.Orch().UpsertSession("telegram:1", "ses_user", now); err != nil {
		t.Fatalf("upsert user session: %v", err)
	}
	if _, err := st.Orch().UpsertSession("cron:heartbeat", "ses_hb", now.Add(time.Minute)); err != nil {
		t.Fatalf("upsert heartbeat session: %v", err)
	}

	var prompt string
	var permissions permissionRecorder
	client := routineTestClientWithPermissions(t, &permissions, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_user/message":
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user","text":"urgent mail"}],"cursor":{"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb":
			fmt.Fprint(w, `{"data":{"id":"ses_hb"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_hb/synthetic":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompt, _ = body["text"].(string)
			fmt.Fprint(w, `{"data":{"id":"msg_hb","sessionID":"ses_hb","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_hb/wait":
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb/message":
			fmt.Fprint(w, `{"data":[{"id":"msg_out","type":"assistant","content":[{"type":"text","text":"NO_ACTION"}]},{"id":"msg_hb","type":"synthetic","text":"prompt"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	env := Env{
		Agent: registry.Agent{Name: "valet", Workspace: ws}, Workspace: ws,
		Thread: "cron:heartbeat", SessionID: "ses_hb", Meta: map[string]any{},
		Now: func() time.Time { return now }, Store: st, OpenCode: client,
		Log: func(string, string, map[string]any) {},
	}
	if _, err := (heartbeatRoutine{}).Run(context.Background(), env, map[string]any{"interval": 60}); err != nil {
		t.Fatalf("heartbeat Run() error: %v", err)
	}
	for _, want := range []string{
		"Watch for urgent mail.",
		"NOTIFY: <one concise message for the user>",
		"NO_ACTION",
		"Never use the heartbeat session as the target",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "acolyte inbox --agent-name") {
		t.Errorf("prompt still instructs the model to run acolyte inbox:\n%s", prompt)
	}
	if strings.Contains(prompt, "Latest user session: ses_hb") {
		t.Error("heartbeat selected its own session as the user target")
	}
	if len(permissions) != 2 {
		t.Fatalf("permission updates = %+v, want restrict+restore", permissions)
	}
	if got := permissionEffect(permissions[0].rules, "shell", `acolyte inbox --agent-name valet --session-id ses_user "hi"`); got != opencode.PermissionDeny {
		t.Errorf("heartbeat inbox permission = %s, want deny", got)
	}
	if got := permissionEffect(permissions[0].rules, "edit", filepath.Join(ws, ".acolyte", "HEARTBEAT.md")); got != opencode.PermissionDeny {
		t.Errorf("heartbeat edit permission = %s, want deny", got)
	}
	if len(permissions[1].rules) != 0 {
		t.Errorf("restored rules = %+v, want empty", permissions[1].rules)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:heartbeat")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_hb" {
		t.Errorf("session = %+v, %v, %v", session, ok, err)
	}
}

func TestHeartbeatNoUserSession(t *testing.T) {
	ws := t.TempDir()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if _, err := st.Orch().UpsertSession("cron:heartbeat", "ses_hb", now); err != nil {
		t.Fatalf("upsert heartbeat session: %v", err)
	}
	var prompt string
	var permissions permissionRecorder
	client := routineTestClientWithPermissions(t, &permissions, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb":
			fmt.Fprint(w, `{"data":{"id":"ses_hb"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_hb/synthetic":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompt, _ = body["text"].(string)
			fmt.Fprint(w, `{"data":{"id":"msg_hb","sessionID":"ses_hb","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_hb/wait":
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb/message":
			fmt.Fprint(w, `{"data":[{"id":"msg_out","type":"assistant","content":[{"type":"text","text":"NO_ACTION"}]},{"id":"msg_hb","type":"synthetic","text":"prompt"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	env := Env{
		Agent: registry.Agent{Name: "valet", Workspace: ws}, Workspace: ws,
		Thread: "cron:heartbeat", SessionID: "ses_hb", Meta: map[string]any{},
		Now: func() time.Time { return now }, Store: st, OpenCode: client,
		Log: func(string, string, map[string]any) {},
	}
	if _, err := (heartbeatRoutine{}).Run(context.Background(), env, map[string]any{"interval": 60}); err != nil {
		t.Fatalf("heartbeat Run() error: %v", err)
	}
	if !strings.Contains(prompt, "No user session is available") {
		t.Errorf("prompt = %s", prompt)
	}
}

func TestHeartbeatNotification(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  string
		ok    bool
	}{
		{"no action", "NO_ACTION", "", false},
		{"notify", "NOTIFY: urgent mail", "urgent mail", true},
		{"multiline", "some context\n  NOTIFY:   hello  \nmore", "hello", true},
		{"empty message", "NOTIFY:", "", false},
		{"lowercase", "notify: hello", "", false},
		{"no prefix", "urgent mail", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := heartbeatNotification(tt.reply)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("heartbeatNotification(%q) = %q, %v; want %q, %v", tt.reply, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestHeartbeatDeliversNotification(t *testing.T) {
	ws := t.TempDir()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if _, err := st.Orch().UpsertSession("telegram:1", "ses_user", now); err != nil {
		t.Fatalf("upsert user session: %v", err)
	}
	if _, err := st.Orch().UpsertSession("cron:heartbeat", "ses_hb", now.Add(time.Minute)); err != nil {
		t.Fatalf("upsert heartbeat session: %v", err)
	}

	var permissions permissionRecorder
	client := routineTestClientWithPermissions(t, &permissions, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_user/message":
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user","text":"urgent mail"}],"cursor":{"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb":
			fmt.Fprint(w, `{"data":{"id":"ses_hb"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_hb/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_hb","sessionID":"ses_hb","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_hb/wait":
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_hb/message":
			fmt.Fprint(w, `{"data":[{"id":"msg_out","type":"assistant","content":[{"type":"text","text":"NOTIFY: urgent mail"}]},{"id":"msg_hb","type":"synthetic","text":"prompt"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	var deliveredSession, deliveredText string
	var deliveredMeta map[string]any
	env := Env{
		Agent: registry.Agent{Name: "valet", Workspace: ws}, Workspace: ws,
		Thread: "cron:heartbeat", SessionID: "ses_hb", Meta: map[string]any{},
		Now: func() time.Time { return now }, Store: st, OpenCode: client,
		InboxSession: func(_ context.Context, sessionID, text string, meta map[string]any) (string, string, error) {
			deliveredSession = sessionID
			deliveredText = text
			deliveredMeta = meta
			return "reply", sessionID, nil
		},
		Log: func(string, string, map[string]any) {},
	}
	if _, err := (heartbeatRoutine{}).Run(context.Background(), env, map[string]any{"interval": 60}); err != nil {
		t.Fatalf("heartbeat Run() error: %v", err)
	}
	if deliveredSession != "ses_user" || deliveredText != "urgent mail" {
		t.Fatalf("delivery = %q/%q, want ses_user/urgent mail", deliveredSession, deliveredText)
	}
	if deliveredMeta["routine"] != "heartbeat" {
		t.Errorf("delivery meta = %+v", deliveredMeta)
	}
	if got := permissionEffect(permissions[0].rules, "shell", `acolyte inbox --agent-name valet --session-id ses_user "hi"`); got != opencode.PermissionDeny {
		t.Errorf("heartbeat shell permission = %s, want deny", got)
	}
}

func TestHeartbeatRejectsUnsafeNotes(t *testing.T) {
	t.Run("symlinked file", func(t *testing.T) {
		ws := t.TempDir()
		external := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(external, []byte("SECRET-CONTENT"), 0600); err != nil {
			t.Fatalf("write secret: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(ws, ".acolyte"), 0755); err != nil {
			t.Fatalf("create .acolyte: %v", err)
		}
		if err := os.Symlink(external, filepath.Join(ws, ".acolyte", "HEARTBEAT.md")); err != nil {
			t.Fatalf("symlink notes: %v", err)
		}

		env := Env{
			Workspace: ws,
			OpenCode:  opencode.NewClient("http://127.0.0.1:1", ""),
			Log:       func(string, string, map[string]any) {},
		}
		if _, err := (heartbeatRoutine{}).Run(context.Background(), env, map[string]any{"interval": 60}); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("heartbeat Run() error = %v, want symlink refusal", err)
		}
	})

	t.Run("symlinked root", func(t *testing.T) {
		ws := t.TempDir()
		externalDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(externalDir, "HEARTBEAT.md"), []byte("SECRET-CONTENT"), 0600); err != nil {
			t.Fatalf("write secret notes: %v", err)
		}
		if err := os.Symlink(externalDir, filepath.Join(ws, ".acolyte")); err != nil {
			t.Fatalf("symlink .acolyte: %v", err)
		}

		env := Env{
			Workspace: ws,
			OpenCode:  opencode.NewClient("http://127.0.0.1:1", ""),
			Log:       func(string, string, map[string]any) {},
		}
		if _, err := (heartbeatRoutine{}).Run(context.Background(), env, map[string]any{"interval": 60}); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("heartbeat Run() error = %v, want symlink refusal", err)
		}
	})
}
