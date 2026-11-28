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

func TestPromptOutcomeUnknown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "definitive bad request", err: &APIError{Status: http.StatusBadRequest}, want: false},
		{name: "definitive not found", err: &APIError{Status: http.StatusNotFound}, want: false},
		{name: "server error", err: &APIError{Status: http.StatusServiceUnavailable}, want: true},
		{name: "rate limited", err: &APIError{Status: http.StatusTooManyRequests}, want: true},
		{name: "request timeout", err: &APIError{Status: http.StatusRequestTimeout}, want: true},
		{name: "transport", err: errors.New("connection reset"), want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := promptOutcomeUnknown(tc.err); got != tc.want {
				t.Errorf("promptOutcomeUnknown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
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

func TestAdmitSyntheticTurnHonorsTimeout(t *testing.T) {
	t.Parallel()

	client := NewClient("http://opencode.test", "")
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	_, err := client.AdmitSyntheticTurn(context.Background(), "ses_x", SyntheticEvent{
		Text: "board ping", Delivery: "queue",
	}, TurnConfig{Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AdmitSyntheticTurn() error = %v, want deadline exceeded", err)
	}
}

func TestAdmitSyntheticTurnCreatesSessionWithoutWaiting(t *testing.T) {
	t.Parallel()

	var syntheticBody map[string]any
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/synthetic":
			syntheticBody = decodeBody(t, r)
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_new","type":"synthetic"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.AdmitSyntheticTurn(context.Background(), "", SyntheticEvent{
		Text:        "board ping",
		Description: "Board automation",
		Metadata:    map[string]any{"channel": "agent-board"},
		Delivery:    "queue",
	}, TurnConfig{Workspace: "/ws"})
	if err != nil {
		t.Fatalf("AdmitSyntheticTurn() error: %v", err)
	}
	if result.SessionID != "ses_new" || result.InputID != "msg_in" {
		t.Errorf("result = %+v", result)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want 2 (create + synthetic, no wait)", calls)
	}
	if syntheticBody["text"] != "board ping" || syntheticBody["delivery"] != "queue" {
		t.Errorf("synthetic body = %+v", syntheticBody)
	}
	metadata, _ := syntheticBody["metadata"].(map[string]any)
	if metadata["channel"] != "agent-board" {
		t.Errorf("metadata = %+v", metadata)
	}
}

func TestAdmitSyntheticTurnAppliesPermissions(t *testing.T) {
	t.Parallel()

	var patchBody map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_new","agent":"build"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/session/ses_new":
			patchBody = decodeBody(t, r)
			fmt.Fprint(w, `{"data":{"id":"ses_new","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/synthetic":
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_new","type":"synthetic"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := client.AdmitSyntheticTurn(context.Background(), "", SyntheticEvent{
		Text: "board ping", Delivery: "queue",
	}, TurnConfig{
		Workspace: "/ws",
		Permissions: []PermissionRule{
			{Action: "*", Resource: "*", Effect: PermissionDeny},
			{Action: "board_read", Resource: "*", Effect: PermissionAllow},
		},
	})
	if err != nil {
		t.Fatalf("AdmitSyntheticTurn() error: %v", err)
	}
	rules, ok := patchBody["permissions"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("permission patch = %+v", patchBody)
	}
	first, _ := rules[0].(map[string]any)
	if first["action"] != "*" || first["effect"] != "deny" {
		t.Errorf("first permission rule = %+v", first)
	}
}

func TestCollectDeliveredReturnsCompletedTurn(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_x/message" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"data":[
			{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[
				{"type":"tool","id":"tool_1","name":"bash","state":{"status":"completed"}},
				{"type":"text","text":"recovered"}
			]},
			{"id":"msg_in","type":"user","time":{"created":1},"text":"question"}
		],"cursor":{"previous":null,"next":null}}`)
	})

	result, delivered, err := client.CollectDelivered(context.Background(), "ses_x", "msg_in")
	if err != nil {
		t.Fatalf("CollectDelivered() error: %v", err)
	}
	if !delivered {
		t.Fatal("CollectDelivered() delivered = false, want true")
	}
	if result.SessionID != "ses_x" || result.InputID != "msg_in" {
		t.Errorf("result = %+v, want session ses_x and input msg_in", result)
	}
	if result.ResponseText != "recovered" {
		t.Errorf("response = %q, want recovered", result.ResponseText)
	}
	wantActivity := []ToolActivity{{Name: "bash", Status: "completed", ID: "tool_1"}}
	if !reflect.DeepEqual(result.ToolActivity, wantActivity) {
		t.Errorf("tool activity = %+v, want %+v", result.ToolActivity, wantActivity)
	}
}

func TestCollectDeliveredReportsPendingMarker(t *testing.T) {
	t.Parallel()

	var inboxCalls int
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_x/message":
			// The marker is not in the window: the turn never produced output.
			fmt.Fprint(w, `{"data":[
				{"id":"msg_in","type":"user","time":{"created":1},"text":"question"}
			],"cursor":{"previous":null,"next":null}}`)
		case "/api/session/ses_x/inbox":
			inboxCalls++
			t.Errorf("inbox should not be consulted without truncation")
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, delivered, err := client.CollectDelivered(context.Background(), "ses_x", "msg_missing")
	if err != nil {
		t.Fatalf("CollectDelivered() error: %v", err)
	}
	if delivered {
		t.Fatal("CollectDelivered() delivered = true, want false")
	}
	if result != nil {
		t.Errorf("result = %+v, want nil while pending", result)
	}
	if inboxCalls != 0 {
		t.Errorf("inbox calls = %d, want 0", inboxCalls)
	}
}

func TestCollectDeliveredRequiresIDs(t *testing.T) {
	t.Parallel()

	client := NewClient("http://opencode.test", "")
	if _, _, err := client.CollectDelivered(context.Background(), "", "msg_in"); err == nil {
		t.Fatal("CollectDelivered() error = nil, want missing session id error")
	}
	if _, _, err := client.CollectDelivered(context.Background(), "ses_x", "  "); err == nil {
		t.Fatal("CollectDelivered() error = nil, want missing marker error")
	}
}

func TestSessionTimeInFlight(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		time SessionTime
		want bool
	}{
		{name: "idle", time: SessionTime{Busy: 10, Idle: 20}},
		{name: "running", time: SessionTime{Busy: 20, Idle: 10}, want: true},
		{name: "never started", time: SessionTime{}},
		{name: "settled at same instant", time: SessionTime{Busy: 10, Idle: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.time.InFlight(); got != tc.want {
				t.Errorf("InFlight() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunUserTurnCallsOnAdmittedBeforeCollect(t *testing.T) {
	t.Parallel()

	var order []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			order = append(order, "session")
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			order = append(order, "prompt")
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			order = append(order, "wait")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			order = append(order, "message")
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"hi"}]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	var admitted Admitted
	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{
		OnAdmitted: func(a Admitted) error {
			order = append(order, "admitted")
			admitted = a
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunUserTurn() error: %v", err)
	}
	if admitted.SessionID != "ses_x" || admitted.InputID != "msg_in" {
		t.Fatalf("admitted = %+v, want ses_x/msg_in", admitted)
	}
	if result.ResponseText != "hi" {
		t.Errorf("response = %q, want hi", result.ResponseText)
	}

	indexOf := func(name string) int {
		for i, value := range order {
			if value == name {
				return i
			}
		}
		return -1
	}
	if indexOf("prompt") < 0 || indexOf("admitted") < 0 || indexOf("message") < 0 {
		t.Fatalf("order = %v, want prompt, admitted and message to all occur", order)
	}
	if indexOf("admitted") < indexOf("prompt") {
		t.Errorf("order = %v, want admission after the prompt", order)
	}
	if indexOf("admitted") > indexOf("message") {
		t.Errorf("order = %v, want admission before collection", order)
	}
}

func TestRunUserTurnOnAdmittedErrorPreservesMarker(t *testing.T) {
	t.Parallel()

	var collected bool
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			collected = true
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{
		OnAdmitted: func(Admitted) error { return errors.New("store unavailable") },
	})
	if err == nil {
		t.Fatal("RunUserTurn() error = nil, want persistence error")
	}
	if result == nil {
		t.Fatal("RunUserTurn() result = nil, want partial result carrying the marker")
	}
	if result.SessionID != "ses_x" || result.InputID != "msg_in" {
		t.Errorf("result = %+v, want session ses_x and input msg_in", result)
	}
	if collected {
		t.Error("turn was collected despite a failed admission hook")
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
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[
					{"type":"tool","id":"tool_synth","name":"bash","state":{"status":"completed"}},
					{"type":"text","text":"done"}
				]},
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
	wantActivity := []ToolActivity{{Name: "bash", Status: "completed", ID: "tool_synth"}}
	if !reflect.DeepEqual(result.ToolActivity, wantActivity) {
		t.Errorf("tool activity = %+v, want %+v", result.ToolActivity, wantActivity)
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

func TestRunUserTurnReturnsToolActivity(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[
					{"type":"tool","id":"tool_1","name":"bash","state":{"status":"completed"}},
					{"type":"text","text":"done"},
					{"type":"tool","id":"tool_2","name":"read","state":{"status":"error"}}
				]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if err != nil {
		t.Fatalf("RunUserTurn() error = %v", err)
	}
	want := []ToolActivity{
		{Name: "bash", Status: "completed", ID: "tool_1"},
		{Name: "read", Status: "error", ID: "tool_2"},
	}
	if !reflect.DeepEqual(result.ToolActivity, want) {
		t.Errorf("tool activity = %+v, want %+v", result.ToolActivity, want)
	}
	if result.ResponseText != "done" {
		t.Errorf("response = %q, want done", result.ResponseText)
	}
	if !reflect.DeepEqual(result.MessageIDs, []string{"msg_reply"}) {
		t.Errorf("message ids = %v, want [msg_reply]", result.MessageIDs)
	}
}

func TestRunUserTurnToleratesMalformedContent(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_tools","type":"assistant","time":{"created":4},"content":[
					{"type":"tool","id":"tool_1","name":"bash","state":{"status":"completed"}}
				]},
				{"id":"msg_bad_part","type":"assistant","time":{"created":3},"content":[
					{"type":42,"text":"bad"},
					{"type":"text","text":"ok"},
					"junk"
				]},
				{"id":"msg_bad_content","type":"assistant","time":{"created":2},"content":"not-an-array"},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if err != nil {
		t.Fatalf("RunUserTurn() error = %v, want malformed content tolerated", err)
	}
	if result.ResponseText != "ok" {
		t.Errorf("response = %q, want ok", result.ResponseText)
	}
	want := []ToolActivity{{Name: "bash", Status: "completed", ID: "tool_1"}}
	if !reflect.DeepEqual(result.ToolActivity, want) {
		t.Errorf("tool activity = %+v, want %+v", result.ToolActivity, want)
	}
}

func TestAssistantTextSeparatesMessagesWithBlankLine(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		{ID: "msg_1", Type: "assistant", Content: []ContentPart{{Type: "text", Text: "first"}}},
		{ID: "msg_user", Type: "user", Text: "ignored"},
		{ID: "msg_2", Type: "assistant", Content: []ContentPart{{Type: "text", Text: "second"}}},
	}
	if got := assistantText(msgs); got != "first\n\nsecond" {
		t.Errorf("assistantText() = %q, want %q", got, "first\n\nsecond")
	}
}

func TestAssistantTextSingleMessageUnchanged(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		{ID: "msg_1", Type: "assistant", Content: []ContentPart{{Type: "text", Text: "only"}}},
	}
	if got := assistantText(msgs); got != "only" {
		t.Errorf("assistantText() = %q, want %q", got, "only")
	}
}

func TestAssistantToolActivity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msgs []Message
		want []ToolActivity
	}{
		{
			name: "text only",
			msgs: []Message{
				{ID: "msg_1", Type: "assistant", Raw: json.RawMessage(`{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"hello"},{"type":"reasoning","text":"thinking"}]}`)},
			},
		},
		{
			name: "ordered across messages",
			msgs: []Message{
				{ID: "msg_1", Type: "assistant", Raw: json.RawMessage(`{"content":[{"type":"tool","id":"tool_1","name":"bash","state":{"status":"completed"}},{"type":"text","text":"a"},{"type":"tool","id":"tool_2","name":"read","state":{"status":"running"}}]}`)},
				{ID: "msg_user", Type: "user", Raw: json.RawMessage(`{"content":[{"type":"tool","id":"tool_ignored","name":"ignored","state":{"status":"running"}}]}`)},
				{ID: "msg_idle", Type: "idle", Raw: json.RawMessage(`{"outcome":"succeeded"}`)},
				{ID: "msg_2", Type: "assistant", Raw: json.RawMessage(`{"content":[{"type":"tool","id":"tool_3","name":"grep","state":{"status":"error"}}]}`)},
			},
			want: []ToolActivity{
				{Name: "bash", Status: "completed", ID: "tool_1"},
				{Name: "read", Status: "running", ID: "tool_2"},
				{Name: "grep", Status: "error", ID: "tool_3"},
			},
		},
		{
			name: "malformed content is ignored",
			msgs: []Message{
				{ID: "msg_bad_raw", Type: "assistant", Raw: json.RawMessage(`{"content":`)},
				{ID: "msg_bad_shape", Type: "assistant", Raw: json.RawMessage(`{"content":"not-an-array"}`)},
				{ID: "msg_empty", Type: "assistant"},
				{ID: "msg_1", Type: "assistant", Raw: json.RawMessage(`{"content":[
					{"type":"tool","id":"tool_bad_name","name":42,"state":{"status":"completed"}},
					{"type":"tool","id":"tool_bad_state","name":"bad","state":"nope"},
					{"type":"tool"},
					{"type":"tool","id":"tool_ok","name":"bash","state":{"status":"completed"}}
				]}`)},
			},
			want: []ToolActivity{{Name: "bash", Status: "completed", ID: "tool_ok"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := assistantToolActivity(tc.msgs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("assistantToolActivity() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
