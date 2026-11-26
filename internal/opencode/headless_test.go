package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestInteractionSettlerSettlesFormsAndPermissions(t *testing.T) {
	t.Parallel()

	var cancelled []string
	var decision, replyMessage string

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_root/form":
			fmt.Fprint(w, `{"data":[
				{"id":"frm_q","sessionID":"ses_root","title":"Question","metadata":{"kind":"question"}},
				{"id":"frm_g","sessionID":"ses_root","title":"Form"}
			]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_root/permission":
			fmt.Fprint(w, `{"data":[{"id":"per_1","sessionID":"ses_root","action":"read","resources":["*.env"]}]}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/session/ses_root/form/"):
			cancelled = append(cancelled, r.URL.Path+"?"+r.URL.RawQuery)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_root/permission/per_1/reply":
			var body struct {
				Decision string `json:"decision"`
				Message  string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			decision, replyMessage = body.Decision, body.Message
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	var settlements []InteractionSettlement
	settler := newInteractionSettler(client, "ses_root", func(s InteractionSettlement) {
		settlements = append(settlements, s)
	})
	if _, err := settler.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(settlements) != 3 {
		t.Fatalf("settlements = %d, want 3: %+v", len(settlements), settlements)
	}
	if decision != PermissionDecisionReject || replyMessage != headlessPermissionMessage {
		t.Errorf("permission reply = %q / %q, want reject / headless message", decision, replyMessage)
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled forms = %v, want 2", cancelled)
	}
	questionMessage := ""
	for _, call := range cancelled {
		if strings.Contains(call, "frm_q") {
			questionMessage = formMessageFromCall(t, call)
		}
		if !strings.Contains(call, "frm_g") {
			continue
		}
		if got := formMessageFromCall(t, call); got != headlessFormMessage {
			t.Errorf("generic form message = %q, want %q", got, headlessFormMessage)
		}
	}
	if questionMessage != headlessQuestionMessage {
		t.Errorf("question form message = %q, want %q", questionMessage, headlessQuestionMessage)
	}
}

func TestInteractionSettlerDiscoversChildren(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			parent := r.URL.Query().Get("parentID")
			switch parent {
			case "ses_root":
				// Paginated: the child is on the second page.
				if r.URL.Query().Get("cursor") == "" {
					fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":"p2"}}`)
					return
				}
				fmt.Fprint(w, `{"data":[{"id":"ses_child","parentID":"ses_root"}],"cursor":{"previous":null,"next":null}}`)
			case "ses_child":
				fmt.Fprint(w, `{"data":[{"id":"ses_grand","parentID":"ses_child"}],"cursor":{"previous":null,"next":null}}`)
			case "ses_grand":
				fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
			default:
				t.Errorf("unexpected parentID %q", parent)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_child/form":
			fmt.Fprint(w, `{"data":[{"id":"frm_child","sessionID":"ses_child","title":"Form","metadata":{"kind":"question"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_grand/permission":
			fmt.Fprint(w, `{"data":[{"id":"per_grand","sessionID":"ses_grand","action":"read","resources":["*"]}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/form"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/permission"):
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodDelete:
			seen["form:"+r.URL.Path] = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reply"):
			seen["permission:"+r.URL.Path] = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	settler := newInteractionSettler(client, "ses_root", func(InteractionSettlement) {})
	if _, err := settler.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !seen["form:/api/session/ses_child/form/frm_child"] {
		t.Errorf("child form was not settled: %v", seen)
	}
	if !seen["permission:/api/session/ses_grand/permission/per_grand/reply"] {
		t.Errorf("grandchild permission was not settled: %v", seen)
	}
}

func TestInteractionSettlerTreatsAlreadySettledAsSuccess(t *testing.T) {
	t.Parallel()

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_root/form":
			fmt.Fprint(w, `{"data":[{"id":"frm_1","sessionID":"ses_root","title":"Form"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_root/permission":
			fmt.Fprint(w, `{"data":[{"id":"per_1","sessionID":"ses_root","action":"read","resources":["*"]}]}`)
		case r.Method == http.MethodDelete:
			http.Error(w, "already settled", http.StatusConflict)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reply"):
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	var settlements []InteractionSettlement
	settler := newInteractionSettler(client, "ses_root", func(s InteractionSettlement) {
		settlements = append(settlements, s)
	})
	if _, err := settler.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(settlements) != 2 {
		t.Fatalf("settlements = %d, want 2", len(settlements))
	}
	for _, settlement := range settlements {
		if settlement.Action != "already-settled" || settlement.Err != nil {
			t.Errorf("settlement = %+v, want already-settled without error", settlement)
		}
	}
}

func formMessageFromCall(t *testing.T, call string) string {
	t.Helper()
	parsed, err := url.Parse("http://test" + call)
	if err != nil {
		t.Fatalf("parse %q: %v", call, err)
	}
	return parsed.Query().Get("message")
}

func TestInteractionSettlerDepthCap(t *testing.T) {
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		parent := r.URL.Query().Get("parentID")
		var depth int
		if _, err := fmt.Sscanf(parent, "ses_%d", &depth); err != nil {
			t.Errorf("unexpected parentID %q", parent)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if depth <= interactionMaxTreeDepth {
			fmt.Fprintf(w, `{"data":[{"id":"ses_%d"}],"cursor":{"previous":null,"next":null}}`, depth+1)
			return
		}
		fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
	})

	settler := newInteractionSettler(client, "ses_0", nil)
	if _, err := settler.sweep(context.Background()); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("sweep error = %v, want depth cap error", err)
	}
}

func TestSettlementAuditMetaIsIDsOnly(t *testing.T) {
	raw := SettlementAuditMeta(InteractionSettlement{
		SessionID: "ses_1",
		Kind:      "form",
		ID:        "frm_1",
		Action:    "sweep-failed",
		Message:   "secret form title",
		Err:       context.Canceled,
	})
	for _, leak := range []string{"secret form title", "context canceled"} {
		if strings.Contains(raw, leak) {
			t.Errorf("audit meta leaked %q: %s", leak, raw)
		}
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode meta %q: %v", raw, err)
	}
	if len(fields) != 4 {
		t.Errorf("meta fields = %v, want 4 IDs-only fields", fields)
	}
	if fields["session_id"] != "ses_1" || fields["kind"] != "form" || fields["id"] != "frm_1" || fields["action"] != "sweep-failed" {
		t.Errorf("meta = %v", fields)
	}
}

func TestInteractionSettlerIgnoresDeletedSessions(t *testing.T) {
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			if r.URL.Query().Get("parentID") == "ses_root" {
				fmt.Fprint(w, `{"data":[{"id":"ses_child"}],"cursor":{"previous":null,"next":null}}`)
				return
			}
			http.Error(w, "gone", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/form"),
			r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/permission"):
			http.Error(w, "gone", http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	settler := newInteractionSettler(client, "ses_root", nil)
	found, err := settler.sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if found != 0 {
		t.Errorf("found = %d, want 0", found)
	}
}
