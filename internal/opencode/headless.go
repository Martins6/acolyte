package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ErrInteractionUnsettled reports that a turn could not settle a pending form
// or permission request. The turn must fail instead of waiting forever on an
// interaction no client can answer.
var ErrInteractionUnsettled = errors.New("opencode: headless interaction could not be settled")

// The messages are the same ones `opencode run` sends for a non-interactive
// turn. The question wording is used for question forms; other forms get a
// generic wording instead of claiming they were questions.
const (
	headlessQuestionMessage   = "This non-interactive run cannot ask the user questions, so the question was cancelled. Continue without an answer; make reasonable assumptions and state them."
	headlessPermissionMessage = "This non-interactive run cannot ask the user for permission, so the request was rejected. Continue without this action."
	headlessFormMessage       = "This non-interactive run cannot ask the user for input, so the request was cancelled. Continue without an answer."
)

// Interaction settlement tunables. They are variables so tests can shorten
// them without racing on a shared clock.
var (
	interactionPollInterval    = time.Second
	interactionSweepTimeout    = 5 * time.Second
	interactionRetryGap        = 200 * time.Millisecond
	interactionSettleAttempts  = 3
	interactionMaxFailures     = 3
	interactionSettlerStopWait = 5 * time.Second
	// preTurnIdleTimeout bounds how long a turn waits for the execution a
	// pre-sweep just resumed to go idle before switching agent/model.
	preTurnIdleTimeout = 10 * time.Second
)

const (
	// interactionMaxTreeDepth bounds descendant discovery. Hitting the cap is
	// an error, never a silent "not my session".
	interactionMaxTreeDepth = 8
)

// InteractionSettlement is one audited settlement outcome. Kind is "form" or
// "permission"; Action is "cancelled", "rejected", "already-settled", or
// "sweep-failed". Err is nil for success and non-nil for a failed attempt.
type InteractionSettlement struct {
	Time      time.Time
	SessionID string
	Kind      string
	ID        string
	Action    string
	Message   string
	Err       error
}

// interactionSettler discovers a turn's session tree and settles every pending
// form and permission request. It is pull-based on purpose: the event stream
// is best-effort and cannot be the correctness mechanism.
type interactionSettler struct {
	client        *Client
	rootSessionID string
	onInteraction func(InteractionSettlement)
}

func newInteractionSettler(client *Client, rootSessionID string, onInteraction func(InteractionSettlement)) *interactionSettler {
	return &interactionSettler{
		client:        client,
		rootSessionID: rootSessionID,
		onInteraction: onInteraction,
	}
}

// sweep runs one bounded discovery + settlement pass. The count is the number
// of pending interactions found (including already-settled ones), so a turn can
// wait for the old execution a pre-sweep just resumed. A nil error means every
// discovered session was listed and every pending interaction was settled.
func (s *interactionSettler) sweep(ctx context.Context) (int, error) {
	sessions, err := s.sessions(ctx)
	if err != nil {
		return 0, err
	}
	found := 0
	var errs []error
	for _, sessionID := range sessions {
		count, err := s.settleSession(ctx, sessionID)
		found += count
		if err != nil {
			errs = append(errs, err)
		}
	}
	return found, errors.Join(errs...)
}

// finalSweep runs one last bounded pass after the poller has been stopped.
func (s *interactionSettler) finalSweep(ctx context.Context) error {
	sweepCtx, cancel := context.WithTimeout(ctx, interactionSweepTimeout)
	defer cancel()
	_, err := s.sweep(sweepCtx)
	return err
}

// sessions returns the root and every descendant, breadth first. A 404 while
// listing children means the parent disappeared; that is treated as a leaf.
// Page caps and the depth cap are errors so a missed session can never look
// like a clean sweep.
func (s *interactionSettler) sessions(ctx context.Context) ([]string, error) {
	depth := map[string]int{s.rootSessionID: 0}
	visited := map[string]bool{s.rootSessionID: true}
	queue := []string{s.rootSessionID}
	var out []string
	for len(queue) > 0 {
		sessionID := queue[0]
		queue = queue[1:]
		out = append(out, sessionID)

		children, err := s.client.ListChildSessions(ctx, sessionID)
		if err != nil {
			if isSessionGone(err) {
				continue
			}
			return nil, fmt.Errorf("opencode: list children of %s: %w", sessionID, err)
		}
		if len(children) > 0 && depth[sessionID] >= interactionMaxTreeDepth {
			return nil, fmt.Errorf("opencode: session tree below %s exceeds depth %d", s.rootSessionID, interactionMaxTreeDepth)
		}
		for _, child := range children {
			if visited[child.ID] {
				continue
			}
			visited[child.ID] = true
			depth[child.ID] = depth[sessionID] + 1
			queue = append(queue, child.ID)
		}
	}
	return out, nil
}

// settleSession lists and settles the pending interactions owned by one
// session. A missing session is already settled; every other error is
// returned so the caller can retry or fail the turn.
func (s *interactionSettler) settleSession(ctx context.Context, sessionID string) (int, error) {
	var errs []error
	found := 0

	forms, err := s.client.ListForms(ctx, sessionID)
	if err != nil {
		if !isSessionGone(err) {
			errs = append(errs, fmt.Errorf("opencode: list forms for %s: %w", sessionID, err))
		}
	} else {
		found += len(forms)
		for _, form := range forms {
			message := headlessFormMessage
			if isQuestionForm(form) {
				message = headlessQuestionMessage
			}
			s.settleForm(ctx, sessionID, form, message, &errs)
		}
	}

	permissions, err := s.client.ListPermissions(ctx, sessionID)
	if err != nil {
		if !isSessionGone(err) {
			errs = append(errs, fmt.Errorf("opencode: list permissions for %s: %w", sessionID, err))
		}
	} else {
		found += len(permissions)
		for _, request := range permissions {
			s.settlePermission(ctx, sessionID, request, &errs)
		}
	}

	return found, errors.Join(errs...)
}

func (s *interactionSettler) settleForm(ctx context.Context, sessionID string, form FormInfo, message string, errs *[]error) {
	err := s.withRetry(ctx, func(ctx context.Context) error {
		return s.client.CancelForm(ctx, sessionID, form.ID, message)
	})
	settlement := InteractionSettlement{
		SessionID: sessionID,
		Kind:      "form",
		ID:        form.ID,
		Action:    "cancelled",
		Message:   message,
		Err:       err,
	}
	if err != nil {
		if IsInteractionAlreadySettled(err) {
			settlement.Err = nil
			settlement.Action = "already-settled"
		} else {
			settlement.Action = "sweep-failed"
			*errs = append(*errs, fmt.Errorf("opencode: cancel form %s in %s: %w", form.ID, sessionID, err))
		}
	}
	s.record(settlement)
}

func (s *interactionSettler) settlePermission(ctx context.Context, sessionID string, request PermissionRequest, errs *[]error) {
	err := s.withRetry(ctx, func(ctx context.Context) error {
		return s.client.ReplyPermission(ctx, sessionID, request.ID, PermissionDecisionReject, headlessPermissionMessage)
	})
	settlement := InteractionSettlement{
		SessionID: sessionID,
		Kind:      "permission",
		ID:        request.ID,
		Action:    "rejected",
		Message:   headlessPermissionMessage,
		Err:       err,
	}
	if err != nil {
		if IsInteractionAlreadySettled(err) {
			settlement.Err = nil
			settlement.Action = "already-settled"
		} else {
			settlement.Action = "sweep-failed"
			*errs = append(*errs, fmt.Errorf("opencode: reject permission %s in %s: %w", request.ID, sessionID, err))
		}
	}
	s.record(settlement)
}

// withRetry retries transport and 5xx failures a bounded number of times.
// Permanent 4xx responses and already-settled outcomes are returned at once.
func (s *interactionSettler) withRetry(ctx context.Context, call func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < interactionSettleAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interactionRetryGap):
			}
		}
		err = call(ctx)
		if err == nil || IsInteractionAlreadySettled(err) || IsPermanentError(err) {
			return err
		}
	}
	return err
}

// SettlementAuditMeta renders the IDs-only audit payload for one settlement.
// It deliberately excludes form titles, permission resources, and error text.
func SettlementAuditMeta(settlement InteractionSettlement) string {
	raw, err := json.Marshal(map[string]string{
		"session_id": settlement.SessionID,
		"kind":       settlement.Kind,
		"id":         settlement.ID,
		"action":     settlement.Action,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// record sends one outcome to the audit callback and always logs failures to
// the daemon log. Callback panics are contained so a broken consumer cannot
// kill the settler and, with it, the turn's headless guarantee.
func (s *interactionSettler) record(settlement InteractionSettlement) {
	settlement.Time = time.Now().UTC()
	if settlement.Err != nil {
		log.Printf("opencode: headless settlement failed session=%s kind=%s id=%s action=%s err=%v",
			settlement.SessionID, settlement.Kind, settlement.ID, settlement.Action, settlement.Err)
	}
	if s.onInteraction != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("opencode: interaction callback panicked: %v", recovered)
				}
			}()
			s.onInteraction(settlement)
		}()
		return
	}
	if settlement.Err == nil {
		log.Printf("opencode: headless settlement session=%s kind=%s id=%s action=%s",
			settlement.SessionID, settlement.Kind, settlement.ID, settlement.Action)
	}
}

// start runs the periodic sweep. The returned stop function cancels and joins
// the loop with a bound; a settler that cannot stop is surfaced to the caller.
// fail is called once when consecutive sweeps make no progress.
func (s *interactionSettler) start(ctx context.Context, fail func(error)) func() error {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interactionPollInterval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
			}
			sweepCtx, sweepCancel := context.WithTimeout(loopCtx, interactionSweepTimeout)
			_, err := s.sweep(sweepCtx)
			sweepCancel()
			if loopCtx.Err() != nil {
				// The turn ended while this sweep was in flight; a canceled
				// list is not a settlement failure.
				return
			}
			if err == nil {
				failures = 0
				continue
			}
			failures++
			s.record(InteractionSettlement{Kind: "sweep", Action: "sweep-failed", Err: err})
			if failures >= interactionMaxFailures {
				fail(fmt.Errorf("%w: %v", ErrInteractionUnsettled, err))
				return
			}
		}
	}()

	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(interactionSettlerStopWait):
				stopErr = fmt.Errorf("opencode: interaction settler did not stop within %s", interactionSettlerStopWait)
			}
		})
		return stopErr
	}
}

// isQuestionForm reports whether the form was created by the question tool.
func isQuestionForm(form FormInfo) bool {
	if form.Metadata == nil {
		return false
	}
	kind, _ := form.Metadata["kind"].(string)
	return strings.EqualFold(strings.TrimSpace(kind), "question")
}

// isSessionGone reports whether err means the session no longer exists.
func isSessionGone(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == 404
}

// failFastInteraction reports whether the caller is aborting because a
// headless interaction could not be settled. Service recovery would only add
// delay to that failure path.
func failFastInteraction(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrInteractionUnsettled)
}
