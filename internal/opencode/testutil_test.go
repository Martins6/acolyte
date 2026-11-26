package opencode

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// settlementStub answers the headless settlement list routes with empty
// results. It is a legacy-test shim: tests that are about settlement must use
// newRawTestClient and control every response themselves.
type settlementStub struct {
	mu    sync.Mutex
	calls []string
}

func (s *settlementStub) handle(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/form"):
		s.record(r.Method + " " + path)
		fmt.Fprint(w, `{"data":[]}`)
		return true
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/permission"):
		s.record(r.Method + " " + path)
		fmt.Fprint(w, `{"data":[]}`)
		return true
	case r.Method == http.MethodGet && path == "/api/session" && r.URL.Query().Get("parentID") != "":
		s.record(r.Method + " " + r.URL.String())
		fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		return true
	default:
		return false
	}
}

func (s *settlementStub) record(call string) {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
}

func (s *settlementStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// newTestClient wraps the handler in a settlement stub so tests that are not
// about settlement keep their original request sequences.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	stub := &settlementStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stub.handle(w, r) {
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "pw")
}

// newRawTestClient leaves every request to the caller. Use it for settlement
// tests, where an auto-mock would hide a missing or broken settler.
func newRawTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "pw")
}

// wrapSettlementStub is for tests that build their own httptest server.
func wrapSettlementStub(handler http.HandlerFunc) http.HandlerFunc {
	stub := &settlementStub{}
	return func(w http.ResponseWriter, r *http.Request) {
		if stub.handle(w, r) {
			return
		}
		handler(w, r)
	}
}
