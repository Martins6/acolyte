package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscribeEventsParsesFrames(t *testing.T) {
	var gotTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/event" {
			http.NotFound(w, r)
			return
		}
		if _, password, ok := r.BasicAuth(); !ok || password != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gotTypes = r.URL.Query()["types"]
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "id: evt_1\nevent: message\ndata: {\"id\":\"evt_1\",\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_x\",\"delta\":\"hi\"}}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"evt_2\",\"type\":\"session.idle\",\"data\":{\"sessionID\":\"ses_x\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	var events []Event
	err := client.SubscribeEvents(ctx, []string{"session.idle", "session.text.delta"}, func(event Event) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if len(events) == 2 {
			cancel()
		}
		return nil
	})
	if err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("SubscribeEvents() error = %v, want context cancellation", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want 2", events)
	}
	if events[0].Type != "session.text.delta" || events[0].SessionID() != "ses_x" {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Type != "session.idle" || events[1].SessionID() != "ses_x" {
		t.Errorf("event[1] = %+v", events[1])
	}
	if len(gotTypes) != 2 || gotTypes[0] != "session.idle" || gotTypes[1] != "session.text.delta" {
		t.Errorf("types query = %v", gotTypes)
	}
}

func TestSubscribeEventsReconnectsAfterEOF(t *testing.T) {
	oldDelay := eventReconnectDelay
	eventReconnectDelay = 10 * time.Millisecond
	t.Cleanup(func() { eventReconnectDelay = oldDelay })

	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			return // drop the first stream without an event
		}
		fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"data\":{\"sessionID\":\"ses_x\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got Event
	err := client.SubscribeEvents(ctx, nil, func(event Event) error {
		got = event
		cancel()
		return nil
	})
	if err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("SubscribeEvents() error = %v", err)
	}
	if got.Type != "session.idle" || got.SessionID() != "ses_x" {
		t.Errorf("event = %+v", got)
	}
	if connections.Load() < 2 {
		t.Errorf("connections = %d, want at least 2", connections.Load())
	}
}

func TestRunUserTurnObservesEvents(t *testing.T) {
	subscribed := make(chan struct{})
	eventsSent := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			writeJSON(w, map[string]any{"data": map[string]any{"id": "ses_x"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush() // establish the stream before the prompt
			}
			once.Do(func() { close(subscribed) })
			<-eventsSent
			fmt.Fprint(w, "data: {\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_x\",\"delta\":\"hello\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"data\":{\"sessionID\":\"ses_x\"}}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			// Wait until the event observer is connected before acknowledging
			// the prompt, so the test is deterministic.
			select {
			case <-subscribed:
			case <-time.After(time.Second):
			}
			writeJSON(w, map[string]any{"data": map[string]any{"id": "inp_1", "sessionID": "ses_x", "type": "user"}})
			close(eventsSent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			writeJSON(w, map[string]any{"data": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			// messagesAfter walks the newest-first (order=desc) timeline.
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "msg_1", "type": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "hello"},
				}},
				map[string]any{"id": "inp_1", "type": "user", "text": "say hi"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	var mu sync.Mutex
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.RunUserTurn(ctx, "", "say hi", TurnConfig{
		Workspace: "/tmp/workspace",
		OnEvent: func(event Event) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunUserTurn() error = %v", err)
	}
	if result.ResponseText != "hello" {
		t.Errorf("reply = %q, want hello", result.ResponseText)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDelta, sawIdle bool
	for _, event := range events {
		if event.Type == "session.text.delta" {
			sawDelta = true
		}
		if event.Type == "session.idle" {
			sawIdle = true
		}
	}
	if !sawDelta || !sawIdle {
		t.Errorf("events = %+v, want text delta and idle", events)
	}
}

func TestRunUserTurnWaitsForEventStream(t *testing.T) {
	streamReady := make(chan struct{})
	observed := make(chan struct{})
	var observedOnce sync.Once
	var mu sync.Mutex
	var events []Event
	streamEstablishedAtPrompt := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			writeJSON(w, map[string]any{"data": map[string]any{"id": "ses_x"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/event":
			// Delay the response headers so a prompt admitted before the
			// handshake would demonstrably miss the start of the stream.
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			close(streamReady)
			fmt.Fprint(w, "data: {\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_x\",\"delta\":\"hello\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"data\":{\"sessionID\":\"ses_x\"}}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			mu.Lock()
			select {
			case <-streamReady:
				streamEstablishedAtPrompt = true
			default:
			}
			mu.Unlock()
			writeJSON(w, map[string]any{"data": map[string]any{"id": "inp_1", "sessionID": "ses_x", "type": "user"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			// Hold the turn open until the observer has seen the events.
			select {
			case <-observed:
			case <-time.After(2 * time.Second):
			}
			writeJSON(w, map[string]any{"data": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "msg_1", "type": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "hello"},
				}},
				map[string]any{"id": "inp_1", "type": "user", "text": "say hi"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.RunUserTurn(ctx, "", "say hi", TurnConfig{
		Workspace: "/tmp/workspace",
		OnEvent: func(event Event) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			if event.Type == "session.idle" {
				observedOnce.Do(func() { close(observed) })
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunUserTurn() error = %v", err)
	}
	if result.ResponseText != "hello" {
		t.Errorf("reply = %q, want hello", result.ResponseText)
	}
	mu.Lock()
	defer mu.Unlock()
	if !streamEstablishedAtPrompt {
		t.Error("prompt was admitted before the event stream was established")
	}
	var sawDelta, sawIdle bool
	for _, event := range events {
		if event.Type == "session.text.delta" {
			sawDelta = true
		}
		if event.Type == "session.idle" {
			sawIdle = true
		}
	}
	if !sawDelta || !sawIdle {
		t.Errorf("events = %+v, want text delta and idle", events)
	}
}

func TestRunUserTurnProceedsWhenEventStreamStalls(t *testing.T) {
	oldTimeout := eventReadyTimeout
	eventReadyTimeout = 20 * time.Millisecond
	t.Cleanup(func() { eventReadyTimeout = oldTimeout })

	var mu sync.Mutex
	var events []Event

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			writeJSON(w, map[string]any{"data": map[string]any{"id": "ses_x"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/event":
			// Never send response headers: the readiness wait must time out
			// instead of blocking the turn.
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			writeJSON(w, map[string]any{"data": map[string]any{"id": "inp_1", "sessionID": "ses_x", "type": "user"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			writeJSON(w, map[string]any{"data": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "msg_1", "type": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "hello"},
				}},
				map[string]any{"id": "inp_1", "type": "user", "text": "say hi"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	result, err := client.RunUserTurn(ctx, "", "say hi", TurnConfig{
		Workspace: "/tmp/workspace",
		OnEvent: func(event Event) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunUserTurn() error = %v", err)
	}
	if result.ResponseText != "hello" {
		t.Errorf("reply = %q, want hello", result.ResponseText)
	}
	if elapsed := time.Since(start); elapsed < eventReadyTimeout {
		t.Errorf("turn took %s, want a readiness wait of at least %s", elapsed, eventReadyTimeout)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Errorf("events = %+v, want none from a stalled stream", events)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestEventSessionIDIgnoresInvalidData(t *testing.T) {
	if got := (Event{Data: json.RawMessage(`[]`)}).SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want empty", got)
	}
	if got := (Event{Data: json.RawMessage(`{"sessionID":" ses_x "}`)}).SessionID(); got != "ses_x" {
		t.Errorf("SessionID() = %q, want ses_x", got)
	}
}
