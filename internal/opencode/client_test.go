package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newTestClient returns a client wired to a scripted test server.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "pw")
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func TestCreateSession(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/session" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "opencode" || pass != "pw" {
			t.Errorf("missing/incorrect basic auth: %q %q %v", user, pass, ok)
		}
		gotBody = decodeBody(t, r)
		fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build","model":{"id":"m1","providerID":"p1"}}}`)
	})

	info, err := client.CreateSession(context.Background(), "/ws", "build", &ModelRef{ID: "m1", ProviderID: "p1"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ID != "ses_x" || info.Agent != "build" {
		t.Errorf("unexpected session info: %+v", info)
	}

	loc, _ := gotBody["location"].(map[string]any)
	if loc["directory"] != "/ws" {
		t.Errorf("location = %v, want /ws", gotBody["location"])
	}
	if gotBody["agent"] != "build" {
		t.Errorf("agent = %v, want build", gotBody["agent"])
	}
	model, _ := gotBody["model"].(map[string]any)
	if model["id"] != "m1" || model["providerID"] != "p1" {
		t.Errorf("model = %v, want {m1 p1}", gotBody["model"])
	}
}

func TestPromptAndSyntheticBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, c *Client) (*InboxItem, error)
		want map[string]any
	}{
		{
			name: "prompt",
			call: func(ctx context.Context, c *Client) (*InboxItem, error) {
				return c.Prompt(ctx, "ses_x", "hello")
			},
			want: map[string]any{"text": "hello"},
		},
		{
			name: "synthetic",
			call: func(ctx context.Context, c *Client) (*InboxItem, error) {
				return c.Synthetic(ctx, "ses_x", "mail body", "New email received",
					map[string]any{"source": "mail"}, "queue")
			},
			want: map[string]any{
				"text":        "mail body",
				"description": "New email received",
				"delivery":    "queue",
				"metadata":    map[string]any{"source": "mail"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotBody map[string]any
			var gotPath string
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody = decodeBody(t, r)
				fmt.Fprint(w, `{"data":{"id":"msg_m","sessionID":"ses_x","type":"user"}}`)
			})

			item, err := tt.call(context.Background(), client)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if item.ID != "msg_m" {
				t.Errorf("marker id = %q, want msg_m", item.ID)
			}
			wantPath := "/api/session/ses_x/" + map[string]string{"prompt": "prompt", "synthetic": "synthetic"}[tt.name]
			if gotPath != wantPath {
				t.Errorf("path = %q, want %q", gotPath, wantPath)
			}
			for k, v := range tt.want {
				if !reflect.DeepEqual(gotBody[k], v) {
					t.Errorf("body[%q] = %v, want %v", k, gotBody[k], v)
				}
			}
		})
	}
}

func TestMessagesAfter(t *testing.T) {
	t.Parallel()

	// Timeline (newest first): idle, assistant(b), assistant(a), marker, user(old)
	handler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"msg_idle","type":"idle","time":{"created":5},"outcome":"succeeded"},
			{"id":"msg_b","type":"assistant","time":{"created":4},"content":[{"type":"text","text":"B"}]},
			{"id":"msg_a","type":"assistant","time":{"created":3},"content":[{"type":"text","text":"A"}]},
			{"id":"msg_marker","type":"user","time":{"created":2},"text":"question"},
			{"id":"msg_old","type":"user","time":{"created":1},"text":"earlier"}
		],"cursor":{"previous":null,"next":null}}`)
	}
	client := newTestClient(t, handler)

	msgs, delivered, truncated, err := client.messagesAfter(context.Background(), "ses_x", "msg_marker")
	if err != nil {
		t.Fatalf("messagesAfter: %v", err)
	}
	if !delivered {
		t.Fatal("marker reported as not delivered")
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages after marker, want 3: %+v", len(msgs), msgs)
	}
	// Chronological: assistant A, assistant B, idle.
	if msgs[0].ID != "msg_a" || msgs[1].ID != "msg_b" || msgs[2].ID != "msg_idle" {
		t.Errorf("wrong order: %s %s %s", msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}
}

func TestMessagesAfterStopsAtLaterInput(t *testing.T) {
	t.Parallel()

	// A second input (later turn) is interleaved after the marker: its output
	// must not leak into the marker turn's reply. Walked backward, the
	// collection resets at each later input.
	handler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"msg_idle2","type":"idle","time":{"created":7},"outcome":"succeeded"},
			{"id":"msg_b2","type":"assistant","time":{"created":6},"content":[{"type":"text","text":"later turn reply"}]},
			{"id":"msg_input2","type":"user","time":{"created":5},"text":"later question"},
			{"id":"msg_a2","type":"assistant","time":{"created":4},"content":[{"type":"text","text":"second part"}]},
			{"id":"msg_a1","type":"assistant","time":{"created":3},"content":[{"type":"text","text":"first part"}]},
			{"id":"msg_marker","type":"synthetic","time":{"created":2},"text":"event"},
			{"id":"msg_old","type":"assistant","time":{"created":1},"content":[{"type":"text","text":"older turn"}]}
		],"cursor":{"previous":null,"next":null}}`)
	}
	client := newTestClient(t, handler)

	msgs, delivered, _, err := client.messagesAfter(context.Background(), "ses_x", "msg_marker")
	if err != nil {
		t.Fatalf("messagesAfter: %v", err)
	}
	if !delivered {
		t.Fatal("marker reported as not delivered")
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages after marker, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].ID != "msg_a1" || msgs[1].ID != "msg_a2" {
		t.Errorf("wrong messages: %s %s, want msg_a1 msg_a2", msgs[0].ID, msgs[1].ID)
	}
}

func TestMessagesAfterUndelivered(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"msg_other","type":"assistant","time":{"created":1},"content":[{"type":"text","text":"hi"}]}
		],"cursor":{"previous":null,"next":null}}`)
	})

	msgs, delivered, truncated, err := client.messagesAfter(context.Background(), "ses_x", "msg_marker")
	if err != nil {
		t.Fatalf("messagesAfter: %v", err)
	}
	if delivered {
		t.Error("marker reported as delivered but it is not in the timeline")
	}
	if truncated {
		t.Error("unexpected truncation: the timeline was fully walked")
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages, want 1 (everything up to marker)", len(msgs))
	}
}

func TestMessagesAfterPaging(t *testing.T) {
	t.Parallel()

	page := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			// Newest page does not contain the marker: cursor leads further back.
			fmt.Fprint(w, `{"data":[
				{"id":"msg_new","type":"assistant","time":{"created":3},"content":[{"type":"text","text":"new"}]}
			],"cursor":{"previous":null,"next":"cur2"}}`)
			return
		}
		if r.URL.Query().Get("cursor") != "cur2" {
			t.Errorf("expected cursor=cur2, got %q", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"data":[
			{"id":"msg_marker","type":"user","time":{"created":1},"text":"q"}
		],"cursor":{"previous":"cur1","next":null}}`)
	})

	msgs, delivered, truncated, err := client.messagesAfter(context.Background(), "ses_x", "msg_marker")
	if err != nil {
		t.Fatalf("messagesAfter: %v", err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	if !delivered || len(msgs) != 1 || msgs[0].ID != "msg_new" {
		t.Errorf("delivered=%v msgs=%+v, want [msg_new]", delivered, msgs)
	}
}

func TestMessagesAfterTruncated(t *testing.T) {
	t.Parallel()

	origSize, origPages := messagePageSize, messageMaxPages
	t.Cleanup(func() { messagePageSize, messageMaxPages = origSize, origPages })
	messagePageSize = 1
	messageMaxPages = 2

	// The marker is older than the walk cap: pages never contain it and
	// never run out of cursor.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"msg_new","type":"assistant","time":{"created":3},"content":[{"type":"text","text":"new"}]}
		],"cursor":{"previous":null,"next":"more"}}`)
	})

	msgs, delivered, truncated, err := client.messagesAfter(context.Background(), "ses_x", "msg_marker")
	if err != nil {
		t.Fatalf("messagesAfter: %v", err)
	}
	if delivered {
		t.Error("marker reported as delivered")
	}
	if !truncated {
		t.Error("expected truncation: marker is older than the walk cap")
	}
	_ = msgs
}

func TestInboxPending(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_x/inbox" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"msg_pending","sessionID":"ses_x","type":"synthetic"}]}`)
	})

	if pending, err := client.InboxPending(context.Background(), "ses_x", "msg_pending"); err != nil || !pending {
		t.Errorf("InboxPending = %v, %v; want true, nil", pending, err)
	}
	if pending, err := client.InboxPending(context.Background(), "ses_x", "msg_other"); err != nil || pending {
		t.Errorf("InboxPending = %v, %v; want false, nil", pending, err)
	}
}

func TestRunUserTurnCreatesSession(t *testing.T) {
	t.Parallel()

	var calls []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_m","sessionID":"ses_new","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_new/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_idle","type":"idle","time":{"created":3},"outcome":"succeeded"},
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"the answer"}]},
				{"id":"msg_m","type":"user","time":{"created":1},"text":"the question"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "", "the question", TurnConfig{
		Workspace: "/ws", Agent: "build", Provider: "p1", Model: "m1",
	})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.SessionID != "ses_new" {
		t.Errorf("session id = %q, want ses_new", result.SessionID)
	}
	if result.ResponseText != "the answer" {
		t.Errorf("response = %q, want %q", result.ResponseText, "the answer")
	}
	if len(calls) != 4 {
		t.Errorf("calls = %v, want create/prompt/wait/messages", calls)
	}
}

func TestRunUserTurnSyncsAgentAndModel(t *testing.T) {
	t.Parallel()

	var calls []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"old-agent","model":{"id":"old","providerID":"p1"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/agent":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/model":
			body := decodeBody(t, r)
			model, _ := body["model"].(map[string]any)
			if model["id"] != "m1" || model["providerID"] != "p1" {
				t.Errorf("model = %v, want {m1 p1}", body["model"])
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_m","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_m","type":"user","time":{"created":1},"text":"q"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "q", TurnConfig{
		Workspace: "/ws", Agent: "build", Provider: "p1", Model: "m1",
	})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.ResponseText != "" {
		t.Errorf("response = %q, want empty", result.ResponseText)
	}
	want := []string{
		"GET /api/session/ses_x",
		"POST /api/session/ses_x/agent",
		"POST /api/session/ses_x/model",
		"POST /api/session/ses_x/prompt",
		"POST /api/experimental/session/ses_x/wait",
		"GET /api/session/ses_x/message",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls mismatch:\ngot:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunSyntheticTurnRewaitsUntilDelivered(t *testing.T) {
	t.Parallel()

	origGap := syntheticRecheckGap
	t.Cleanup(func() { syntheticRecheckGap = origGap })
	syntheticRecheckGap = time.Millisecond

	waits := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_new","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_new/wait":
			waits++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/message":
			if waits < 2 {
				// Still queued: marker not delivered yet.
				fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
				return
			}
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"reacted"}]},
				{"id":"msg_ev","type":"synthetic","time":{"created":1},"text":"event"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunSyntheticTurn(context.Background(), "", SyntheticEvent{
		Text: "You've got mail", Description: "New email received", Delivery: "queue",
	}, TurnConfig{Workspace: "/ws"})
	if err != nil {
		t.Fatalf("RunSyntheticTurn: %v", err)
	}
	if waits != 2 {
		t.Errorf("wait calls = %d, want 2 (re-wait until marker delivered)", waits)
	}
	if result.SessionID != "ses_new" || result.ResponseText != "reacted" {
		t.Errorf("result = %+v, want {ses_new reacted}", result)
	}
}

func TestRunSyntheticTurnFailsFastWhenTruncated(t *testing.T) {
	t.Parallel()

	origSize, origPages := messagePageSize, messageMaxPages
	t.Cleanup(func() { messagePageSize, messageMaxPages = origSize, origPages })
	messagePageSize = 1
	messageMaxPages = 2

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_new","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_new/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/message":
			// Marker buried past the walk cap.
			fmt.Fprint(w, `{"data":[
				{"id":"msg_new","type":"assistant","time":{"created":9},"content":[{"type":"text","text":"new"}]}
			],"cursor":{"previous":null,"next":"more"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/inbox":
			// Not pending: the marker was delivered and is buried.
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := client.RunSyntheticTurn(context.Background(), "", SyntheticEvent{Text: "event"}, TurnConfig{Workspace: "/ws"})
	if !errors.Is(err, ErrTurnTruncated) {
		t.Fatalf("err = %v, want ErrTurnTruncated", err)
	}
}

func TestRunSyntheticTurnRewaitsWhenTruncatedButPending(t *testing.T) {
	t.Parallel()

	origSize, origPages := messagePageSize, messageMaxPages
	origGap := syntheticRecheckGap
	t.Cleanup(func() {
		messagePageSize, messageMaxPages = origSize, origPages
		syntheticRecheckGap = origGap
	})
	messagePageSize = 1
	messageMaxPages = 2
	syntheticRecheckGap = time.Millisecond

	round := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_new","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_new/wait":
			round++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/message":
			if round < 2 {
				// Still queued: marker beyond the walk cap of a long session.
				fmt.Fprint(w, `{"data":[
					{"id":"msg_new","type":"assistant","time":{"created":9},"content":[{"type":"text","text":"new"}]}
				],"cursor":{"previous":null,"next":"more"}}`)
				return
			}
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"reacted"}]},
				{"id":"msg_ev","type":"synthetic","time":{"created":1},"text":"event"}
			],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_new/inbox":
			// Pending on the first round; delivered later.
			if round < 2 {
				fmt.Fprint(w, `{"data":[{"id":"msg_ev","sessionID":"ses_new","type":"synthetic"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunSyntheticTurn(context.Background(), "", SyntheticEvent{Text: "event"}, TurnConfig{Workspace: "/ws"})
	if err != nil {
		t.Fatalf("RunSyntheticTurn: %v", err)
	}
	if round != 2 {
		t.Errorf("wait calls = %d, want 2 (re-wait while pending)", round)
	}
	if result.ResponseText != "reacted" {
		t.Errorf("response = %q, want %q", result.ResponseText, "reacted")
	}
}

func TestAPIErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		wantPart string
	}{
		{"not found", http.StatusNotFound, "not found"},
		{"invalid", http.StatusBadRequest, "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, `{"_tag":"Error","message":"boom"}`)
			})
			_, err := client.GetSession(context.Background(), "ses_x")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantPart) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantPart)
			}
		})
	}
}

func TestContext(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_x/context" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[
			{"id":"msg_m","type":"user","time":{"created":1},"text":"hello"},
			{"id":"msg_r","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"hi"}]}
		]}`)
	})

	msgs, err := client.Context(context.Background(), "ses_x")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Content[0].Text != "hi" {
		t.Errorf("unexpected context messages: %+v", msgs)
	}
}

func TestAssistantText(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		{Type: "assistant", Content: []ContentPart{{Type: "text", Text: "a"}, {Type: "reasoning", Text: "thinking"}, {Type: "text", Text: "b"}}},
		{Type: "idle", Outcome: "succeeded"},
		{Type: "assistant", Content: []ContentPart{{Type: "text", Text: "c"}}},
	}
	if got := assistantText(msgs); got != "ab\nc" {
		t.Errorf("assistantText = %q, want %q", got, "ab\nc")
	}
}
