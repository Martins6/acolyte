package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastSettlement shrinks the settlement clock for one test. The returned
// cleanup restores the package globals; the test must not run in parallel.
func fastSettlement(t *testing.T) {
	t.Helper()
	oldPoll := interactionPollInterval
	oldGap := interactionRetryGap
	oldAttempts := interactionSettleAttempts
	oldFailures := interactionMaxFailures
	oldStop := interactionSettlerStopWait
	interactionPollInterval = 10 * time.Millisecond
	interactionRetryGap = time.Millisecond
	interactionSettleAttempts = 2
	interactionMaxFailures = 3
	interactionSettlerStopWait = 200 * time.Millisecond
	t.Cleanup(func() {
		interactionPollInterval = oldPoll
		interactionRetryGap = oldGap
		interactionSettleAttempts = oldAttempts
		interactionMaxFailures = oldFailures
		interactionSettlerStopWait = oldStop
	})
}

func TestRunUserTurnSettlesMidTurnForm(t *testing.T) {
	fastSettlement(t)

	var prompted, settled int32
	promptedCh := make(chan struct{})
	settledCh := make(chan struct{})
	var promptOnce, settleOnce sync.Once

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			if atomic.LoadInt32(&prompted) == 0 || atomic.LoadInt32(&settled) != 0 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"frm_1","sessionID":"ses_x","title":"Question","metadata":{"kind":"question"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			atomic.StoreInt32(&prompted, 1)
			promptOnce.Do(func() { close(promptedCh) })
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			select {
			case <-settledCh:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/session/ses_x/form/frm_1":
			if r.URL.Query().Get("message") != headlessQuestionMessage {
				t.Errorf("cancel message = %q, want question message", r.URL.Query().Get("message"))
			}
			atomic.StoreInt32(&settled, 1)
			settleOnce.Do(func() { close(settledCh) })
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"done"}]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := client.RunUserTurn(ctx, "ses_x", "hello", TurnConfig{})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	select {
	case <-promptedCh:
	default:
		t.Fatal("prompt was never admitted")
	}
	if result.ResponseText != "done" {
		t.Errorf("reply = %q, want done", result.ResponseText)
	}
}

func TestRunUserTurnFailsFastWhenSettlementKeepsFailing(t *testing.T) {
	fastSettlement(t)

	var prompted int32
	promptOnce := sync.Once{}
	promptedCh := make(chan struct{})

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			if atomic.LoadInt32(&prompted) == 0 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"frm_bad","sessionID":"ses_x","title":"Form"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			atomic.StoreInt32(&prompted, 1)
			promptOnce.Do(func() { close(promptedCh) })
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			<-r.Context().Done()
		case r.Method == http.MethodDelete && r.URL.Path == "/api/session/ses_x/form/frm_bad":
			http.Error(w, "settlement failed", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := client.RunUserTurn(ctx, "ses_x", "hello", TurnConfig{})
	if err == nil {
		t.Fatal("RunUserTurn error = nil, want ErrInteractionUnsettled")
	}
	if !errors.Is(err, ErrInteractionUnsettled) {
		t.Fatalf("RunUserTurn error = %v, want ErrInteractionUnsettled", err)
	}
	if result == nil || result.SessionID != "ses_x" || result.InputID != "msg_in" {
		t.Errorf("result = %+v, want admitted session and marker", result)
	}
	select {
	case <-promptedCh:
	default:
		t.Fatal("prompt was never admitted")
	}
}

func TestRunUserTurnPreSweepsStaleInteractions(t *testing.T) {
	fastSettlement(t)

	var cancelled, rejected, prompted int32
	var settlements []InteractionSettlement

	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			if atomic.LoadInt32(&cancelled) != 0 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"frm_stale","sessionID":"ses_x","title":"Question","metadata":{"kind":"question"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			if atomic.LoadInt32(&rejected) != 0 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"per_stale","sessionID":"ses_x","action":"read","resources":["*.env"]}]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/session/ses_x/form/frm_stale":
			if got := r.URL.Query().Get("message"); got != headlessQuestionMessage {
				t.Errorf("stale form message = %q, want question message", got)
			}
			atomic.StoreInt32(&cancelled, 1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/permission/per_stale/reply":
			var body struct {
				Decision string `json:"decision"`
				Message  string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Decision != PermissionDecisionReject || body.Message != headlessPermissionMessage {
				t.Errorf("stale permission reply = %+v", body)
			}
			atomic.StoreInt32(&rejected, 1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			if atomic.LoadInt32(&cancelled) == 0 || atomic.LoadInt32(&rejected) == 0 {
				t.Error("prompt admitted before stale interactions were settled")
			}
			atomic.StoreInt32(&prompted, 1)
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"done"}]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{
		OnInteraction: func(settlement InteractionSettlement) { settlements = append(settlements, settlement) },
	})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.ResponseText != "done" {
		t.Errorf("reply = %q, want done", result.ResponseText)
	}
	if atomic.LoadInt32(&prompted) == 0 {
		t.Error("prompt was never admitted")
	}
	if len(settlements) != 2 {
		t.Fatalf("settlements = %+v, want form + permission", settlements)
	}
}

func TestRunUserTurnPreSweepFailureFailsClosed(t *testing.T) {
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			http.Error(w, "list failed", http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			t.Error("prompt must not be admitted when the pre-sweep fails")
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if !errors.Is(err, ErrInteractionUnsettled) {
		t.Fatalf("RunUserTurn error = %v, want ErrInteractionUnsettled", err)
	}
}

func TestRunUserTurnRetriesTransientSettlementFailure(t *testing.T) {
	fastSettlement(t)

	var prompted, attempts int32
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			if atomic.LoadInt32(&prompted) == 0 || atomic.LoadInt32(&attempts) > 1 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"frm_flaky","sessionID":"ses_x","title":"Form"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			atomic.StoreInt32(&prompted, 1)
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			if atomic.LoadInt32(&attempts) == 0 {
				select {
				case <-time.After(200 * time.Millisecond):
				case <-r.Context().Done():
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/session/ses_x/form/frm_flaky":
			if atomic.AddInt32(&attempts, 1) == 1 {
				http.Error(w, "transient", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"done"}]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.ResponseText != "done" {
		t.Errorf("reply = %q, want done", result.ResponseText)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("settlement attempts = %d, want 2", got)
	}
}

func TestRunUserTurnFinalSweepSettlesLateForm(t *testing.T) {
	fastSettlement(t)
	interactionPollInterval = time.Hour

	var prompted, collected, cancelled int32
	client := newRawTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x":
			fmt.Fprint(w, `{"data":{"id":"ses_x","agent":"build"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/form":
			if atomic.LoadInt32(&collected) == 0 || atomic.LoadInt32(&cancelled) != 0 {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"frm_late","sessionID":"ses_x","title":"Form"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/permission":
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_x/prompt":
			atomic.StoreInt32(&prompted, 1)
			fmt.Fprint(w, `{"data":{"id":"msg_in","sessionID":"ses_x","type":"user"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_x/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_x/message":
			atomic.StoreInt32(&collected, 1)
			fmt.Fprint(w, `{"data":[
				{"id":"msg_reply","type":"assistant","time":{"created":2},"content":[{"type":"text","text":"done"}]},
				{"id":"msg_in","type":"user","time":{"created":1},"text":"hello"}
			],"cursor":{"previous":null,"next":null}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/session/ses_x/form/frm_late":
			atomic.StoreInt32(&cancelled, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	result, err := client.RunUserTurn(context.Background(), "ses_x", "hello", TurnConfig{})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.ResponseText != "done" {
		t.Errorf("reply = %q, want done", result.ResponseText)
	}
	if atomic.LoadInt32(&cancelled) == 0 {
		t.Error("final sweep did not cancel the late form")
	}
}
