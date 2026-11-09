package opencode

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestSessionMessagesPagination(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_x/message" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			if r.URL.Query().Get("order") != "desc" {
				t.Errorf("first page order = %q, want desc", r.URL.Query().Get("order"))
			}
			fmt.Fprint(w, `{"data":[{"id":"m3","type":"assistant"},{"id":"m2","type":"user"}],"cursor":{"previous":"page1","next":"page2"}}`)
		case "page2":
			if r.URL.Query().Get("order") != "" {
				t.Errorf("cursor page must not combine order, got %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"data":[{"id":"m1","type":"user"}],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	})

	messages, truncated, err := client.SessionMessages(context.Background(), "ses_x", 10)
	if err != nil {
		t.Fatalf("SessionMessages() error: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	if len(messages) != 3 || messages[0].ID != "m1" || messages[2].ID != "m3" {
		t.Errorf("messages = %+v, want m1,m2,m3", messages)
	}
}

func TestSessionMessagesTruncated(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m2"}],"cursor":{"previous":null,"next":"more"}}`)
	})
	messages, truncated, err := client.SessionMessages(context.Background(), "ses_x", 1)
	if err != nil {
		t.Fatalf("SessionMessages() error: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true")
	}
	if len(messages) != 1 || messages[0].ID != "m2" {
		t.Errorf("messages = %+v", messages)
	}
}
