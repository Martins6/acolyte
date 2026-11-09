package opencode

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestListSessions(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[
			{"id":"ses_a","agent":"build","title":"First","time":{"created":1000,"updated":2000},"location":{"directory":"/ws"}},
			{"id":"ses_b","agent":"build","title":"Second","time":{"created":3000,"updated":4000},"location":{"directory":"/ws"}}
		]}`)
	})
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 2 || sessions[0].Title != "First" || sessions[0].Location.Directory != "/ws" {
		t.Errorf("sessions = %+v", sessions)
	}
	if sessions[1].Time.Updated != 4000 {
		t.Errorf("updated = %v, want 4000", sessions[1].Time.Updated)
	}
}

func TestDigestSessions(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_a/message":
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user","text":"hello"},{"id":"m2","type":"assistant","content":[{"type":"text","text":"hi"}]}],"cursor":{"next":null}}`)
		case "/api/session/ses_b/message":
			fmt.Fprint(w, `{"data":[{"id":"m3","type":"user","text":"second"}],"cursor":{"next":null}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	digest, err := client.DigestSessions(context.Background(), []SessionInfo{
		{ID: "ses_a", Title: "A", Time: SessionTime{Updated: 2000}},
		{ID: "ses_b", Title: "B"},
	}, DigestOptions{MaxMessageChars: 10})
	if err != nil {
		t.Fatalf("DigestSessions() error: %v", err)
	}
	for _, want := range []string{"SESSION ses_a | A", "user: hello", "assistant: hi", "SESSION ses_b | B", "user: second"} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest missing %q:\n%s", want, digest)
		}
	}
}

func TestDigestSessionsCaps(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"m1","type":"user","text":"`+strings.Repeat("x", 100)+`"}
		],"cursor":{"next":null}}`)
	})
	digest, err := client.DigestSessions(context.Background(), []SessionInfo{{ID: "ses_a"}},
		DigestOptions{MaxMessageChars: 5, MaxTotalChars: 40})
	if err != nil {
		t.Fatalf("DigestSessions() error: %v", err)
	}
	if len(digest) > 60 {
		t.Errorf("digest too long: %d\n%s", len(digest), digest)
	}
	if !strings.Contains(digest, "truncated") && !strings.Contains(digest, "…") {
		t.Errorf("digest does not show truncation:\n%s", digest)
	}
}
