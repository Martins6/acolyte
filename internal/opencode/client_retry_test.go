package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromptRetriesWhileServiceStops(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/session/ses_x/prompt" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		if calls.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"code":"service_stopping"}`)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
	})
	client.retryInterval = 0

	item, err := client.Prompt(context.Background(), "ses_x", "hello")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if item.ID != "msg_in" {
		t.Errorf("item = %+v, want msg_in", item)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("prompt calls = %d, want 2 (one rejected while stopping, one admitted)", got)
	}
}

func TestWaitRetriesServiceStarting(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"code":"service_starting"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	client.retryInterval = 0

	if err := client.Wait(context.Background(), "ses_x"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("wait calls = %d, want 2", got)
	}
}

func TestPromptTransportFailureIsNotReplayed(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		closeConnection(t, w)
	})
	client.retryInterval = 0
	client.retryTimeout = 50 * time.Millisecond
	recoveries := 0
	client.recover = func(ctx context.Context) (*Client, error) {
		recoveries++
		return client, nil
	}

	if _, err := client.Prompt(context.Background(), "ses_x", "hello"); err == nil {
		t.Fatal("Prompt() error = nil, want transport error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("prompt calls = %d, want 1 (a replayed prompt could admit a duplicate turn)", got)
	}
	if recoveries != 1 {
		t.Errorf("recoveries = %d, want 1 (the endpoint must recover even when the call cannot be replayed)", recoveries)
	}
}

func TestClientRecoversAfterTransportFailure(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(live.Close)

	client := NewClient(dead.URL, "pw")
	client.retryInterval = 0
	recoveries := 0
	client.recover = func(ctx context.Context) (*Client, error) {
		recoveries++
		return NewClient(live.URL, "pw"), nil
	}

	if err := client.Wait(context.Background(), "ses_x"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if recoveries != 1 {
		t.Errorf("recoveries = %d, want 1", recoveries)
	}
	if client.baseURL != live.URL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, live.URL)
	}
}

func TestStoppingServicePromotesToNewEndpoint(t *testing.T) {
	stopping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"code":"service_stopping"}`)
	}))
	t.Cleanup(stopping.Close)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
	}))
	t.Cleanup(live.Close)

	client := NewClient(stopping.URL, "pw")
	client.retryInterval = 0
	client.recover = func(ctx context.Context) (*Client, error) {
		return NewClient(live.URL, "pw"), nil
	}

	item, err := client.Prompt(context.Background(), "ses_x", "hello")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if item.ID != "msg_in" {
		t.Errorf("item = %+v, want msg_in", item)
	}
	if client.baseURL != live.URL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, live.URL)
	}
}

func TestNonRestartServiceErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"code":"service_failed","message":"broken"}`)
	})
	client.retryInterval = 0
	client.retryTimeout = 50 * time.Millisecond

	if err := client.Wait(context.Background(), "ses_x"); err == nil {
		t.Fatal("Wait() error = nil, want service error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("wait calls = %d, want 1 (service_failed needs an operator, not a retry)", got)
	}
}

func TestStoppingServiceOutsideRetryWindowStillRecovers(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"code":"service_stopping"}`)
	})
	client.retryTimeout = 10 * time.Millisecond
	recoveries := 0
	client.recover = func(ctx context.Context) (*Client, error) {
		recoveries++
		return client, nil
	}

	if _, err := client.Prompt(context.Background(), "ses_x", "hello"); err == nil {
		t.Fatal("Prompt() error = nil, want service error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("prompt calls = %d, want 1 (no replay outside the retry window)", got)
	}
	if recoveries != 1 {
		t.Errorf("recoveries = %d, want 1 (the endpoint must recover for later calls)", recoveries)
	}
}

func TestAPIErrorCarriesRetryHint(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"code":"service_stopping"}`)
	})
	client.retryInterval = 0
	client.retryTimeout = 10 * time.Millisecond

	err := client.Wait(context.Background(), "ses_x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Wait() error = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", apiErr.Status)
	}
	if apiErr.Code != "service_stopping" {
		t.Errorf("code = %q, want service_stopping", apiErr.Code)
	}
	if apiErr.RetryAfter != 2*time.Second {
		t.Errorf("retry after = %v, want 2s", apiErr.RetryAfter)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"absent", "", 0},
		{"seconds", "1", time.Second},
		{"zero", "0", 0},
		{"garbage", "soon", 0},
		{"overflow", "999999999999999999", 0},
		{"past date", "Mon, 02 Jan 2006 15:04:05 GMT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.raw); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func closeConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer does not support hijacking")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	_ = conn.Close()
}
