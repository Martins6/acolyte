package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRunUserTurnPreservesInputOnCollectError(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			http.Error(w, "wait failed", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if err == nil {
		t.Fatal("RunUserTurn() error = nil, want error")
	}
	if result == nil {
		t.Fatal("RunUserTurn() result = nil, want partial result")
	}
	if result.SessionID != "ses_x" || result.InputID != "msg_in" {
		t.Errorf("result = %+v, want session ses_x and input msg_in", result)
	}
	if errors.Is(err, context.Canceled) {
		t.Logf("unexpected context error: %v", err)
	}
}
