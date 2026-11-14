package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunTurnTimeout(t *testing.T) {
	t.Parallel()

	turns := []struct {
		name string
		run  func(*Client, TurnConfig) error
	}{
		{
			name: "user",
			run: func(client *Client, cfg TurnConfig) error {
				_, err := client.RunUserTurn(context.Background(), "ses_x", "hello", cfg)
				return err
			},
		},
		{
			name: "synthetic",
			run: func(client *Client, cfg TurnConfig) error {
				_, err := client.RunSyntheticTurn(context.Background(), "ses_x", SyntheticEvent{Text: "event"}, cfg)
				return err
			},
		},
	}
	cases := []struct {
		name         string
		timeout      time.Duration
		wantDeadline bool
	}{
		{name: "explicit", timeout: 30 * time.Second, wantDeadline: true},
		{name: "zero", timeout: 0, wantDeadline: false},
	}
	for _, turn := range turns {
		for _, tc := range cases {
			t.Run(turn.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				client := NewClient("http://opencode.test", "")
				var (
					deadline    time.Time
					hasDeadline bool
				)
				client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					deadline, hasDeadline = req.Context().Deadline()
					return nil, errors.New("stop after inspecting context")
				})

				err := turn.run(client, TurnConfig{Timeout: tc.timeout})
				if err == nil {
					t.Fatal("turn error = nil, want transport error")
				}
				if hasDeadline != tc.wantDeadline {
					t.Fatalf("request deadline = %v (set=%v), want set=%v", deadline, hasDeadline, tc.wantDeadline)
				}
				if tc.wantDeadline {
					remaining := time.Until(deadline)
					if remaining <= 0 || remaining > tc.timeout {
						t.Errorf("time until deadline = %v, want within (0, %v]", remaining, tc.timeout)
					}
				}
			})
		}
	}
}

func TestRunUserTurnExplicitShortTimeoutCancels(t *testing.T) {
	t.Parallel()

	client := NewClient("http://opencode.test", "")
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	_, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunUserTurn() error = %v, want deadline exceeded", err)
	}
}

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

func TestRunSyntheticTurnAppliesAndRestoresPermissions(t *testing.T) {
	t.Parallel()

	original := []PermissionRule{{Action: "read", Resource: "*", Effect: PermissionAllow}}
	restrict := []PermissionRule{
		{Action: "*", Resource: "*", Effect: PermissionDeny},
		{Action: "read", Resource: "*", Effect: PermissionAllow},
	}
	var patches [][]PermissionRule

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build","permissions":[{"action":"read","resource":"*","effect":"allow"}]}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/session/ses_x":
			var body struct {
				Permissions []PermissionRule `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode permissions: %v", err)
			}
			patches = append(patches, body.Permissions)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_x","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"done"}]},
				{"id":"msg_ev","type":"synthetic","time":{"created":1},"text":"event"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunSyntheticTurn(context.Background(), "ses_x", SyntheticEvent{Text: "event"}, TurnConfig{
		Workspace:   "/ws",
		Permissions: restrict,
	})
	if err != nil {
		t.Fatalf("RunSyntheticTurn: %v", err)
	}
	if result.ResponseText != "done" {
		t.Errorf("response = %q, want done", result.ResponseText)
	}
	if len(patches) != 2 {
		t.Fatalf("permission PATCHes = %d, want restrict+restore: %+v", len(patches), patches)
	}
	if !reflect.DeepEqual(patches[0], restrict) {
		t.Errorf("restrict rules = %+v, want %+v", patches[0], restrict)
	}
	if !reflect.DeepEqual(patches[1], original) {
		t.Errorf("restored rules = %+v, want %+v", patches[1], original)
	}
}

func TestRunSyntheticTurnRestoresPermissionsOnTurnError(t *testing.T) {
	t.Parallel()

	restrict := []PermissionRule{{Action: "*", Resource: "*", Effect: PermissionDeny}}
	var patches [][]PermissionRule

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/session/ses_x":
			var body struct {
				Permissions []PermissionRule `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode permissions: %v", err)
			}
			patches = append(patches, body.Permissions)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_x","type":"synthetic"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			http.Error(w, "wait failed", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := client.RunSyntheticTurn(context.Background(), "ses_x", SyntheticEvent{Text: "event"}, TurnConfig{
		Workspace:   "/ws",
		Permissions: restrict,
	})
	if err == nil {
		t.Fatal("RunSyntheticTurn() error = nil, want turn error")
	}
	if len(patches) != 2 {
		t.Fatalf("permission PATCHes = %d, want restrict+restore: %+v", len(patches), patches)
	}
	if !reflect.DeepEqual(patches[1], []PermissionRule{}) {
		t.Errorf("restored rules = %+v, want empty ruleset", patches[1])
	}
}

func TestRunSyntheticTurnFailsClosedWhenPermissionUpdateFails(t *testing.T) {
	t.Parallel()

	syntheticCalls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/session/ses_x":
			http.Error(w, "permissions failed", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/synthetic":
			syntheticCalls++
			fmt.Fprint(w, `{"data":{"id":"msg_ev","sessionID":"ses_x","type":"synthetic"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunSyntheticTurn(context.Background(), "ses_x", SyntheticEvent{Text: "event"}, TurnConfig{
		Workspace: "/ws",
		Permissions: []PermissionRule{
			{Action: "*", Resource: "*", Effect: PermissionDeny},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "apply session permissions") {
		t.Fatalf("RunSyntheticTurn() error = %v, want permission failure", err)
	}
	if result == nil || result.SessionID != "ses_x" {
		t.Errorf("result = %+v, want session ses_x", result)
	}
	if syntheticCalls != 0 {
		t.Errorf("synthetic calls = %d, want 0 (turn must not run without restrictions)", syntheticCalls)
	}
}
