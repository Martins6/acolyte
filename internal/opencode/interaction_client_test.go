package opencode

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestListChildSessionsPaginates(t *testing.T) {
	oldSize, oldPages := childSessionPageSize, childSessionMaxPages
	childSessionPageSize = 2
	childSessionMaxPages = 3
	t.Cleanup(func() {
		childSessionPageSize, childSessionMaxPages = oldSize, oldPages
	})

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		cursor := r.URL.Query().Get("cursor")
		switch cursor {
		case "":
			fmt.Fprint(w, `{"data":[{"id":"ses_1"},{"id":"ses_2"}],"cursor":{"previous":null,"next":"p2"}}`)
		case "p2":
			fmt.Fprint(w, `{"data":[{"id":"ses_3"},{"id":"ses_4"}],"cursor":{"previous":null,"next":"p3"}}`)
		case "p3":
			fmt.Fprint(w, `{"data":[{"id":"ses_5"},{"id":"ses_6"}],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected cursor %q", cursor)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	children, err := client.ListChildSessions(context.Background(), "ses_root")
	if err != nil {
		t.Fatalf("ListChildSessions: %v", err)
	}
	if len(children) != 6 {
		t.Fatalf("children = %d, want 6", len(children))
	}
	for index, child := range children {
		if want := fmt.Sprintf("ses_%d", index+1); child.ID != want {
			t.Errorf("child %d = %q, want %q", index, child.ID, want)
		}
	}
}

func TestListChildSessionsFailsOnPageCap(t *testing.T) {
	oldSize, oldPages := childSessionPageSize, childSessionMaxPages
	childSessionPageSize = 2
	childSessionMaxPages = 2
	t.Cleanup(func() {
		childSessionPageSize, childSessionMaxPages = oldSize, oldPages
	})

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"ses_child"}],"cursor":{"previous":null,"next":"more"}}`)
	})

	if _, err := client.ListChildSessions(context.Background(), "ses_root"); err == nil {
		t.Fatal("ListChildSessions error = nil, want page cap error")
	}
}
