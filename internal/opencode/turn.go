package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/lock"
)

// ErrTurnTruncated reports that a turn's marker was delivered but lies older
// than the newest collection window, so its reply cannot be bounded reliably.
var ErrTurnTruncated = errors.New("opencode: turn output exceeded the collection window")

// PromptError is a failed admission request. Ambiguous is true when the request
// may have reached the service (transport failure, timeout, cancellation, or a
// 5xx), so retrying could execute it twice. It is false for a definitive
// rejection that proves the input was not admitted.
type PromptError struct {
	Err       error
	Ambiguous bool
}

func (e *PromptError) Error() string { return e.Err.Error() }
func (e *PromptError) Unwrap() error { return e.Err }

// promptOutcomeUnknown reports whether a failed prompt may still have been
// accepted by the service.
func promptOutcomeUnknown(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return !apiErr.Permanent()
	}
	// Transport errors, timeouts, and cancellations can happen after the
	// request was sent, so the conservative answer is yes.
	return true
}

// syntheticRecheckGap is how long to pause before re-waiting when a queued
// input has not been delivered yet (a var so tests can shorten it).
var syntheticRecheckGap = 2 * time.Second

// TurnConfig carries the per-turn settings the callers already hold.
// Admitted identifies an input that is durably queued on a session before the
// turn is collected. It is the recovery handle for a turn interrupted by a
// restart: the pair can be re-collected without admitting the input again.
type Admitted struct {
	SessionID string
	InputID   string
}

type TurnConfig struct {
	Workspace string
	Agent     string
	Provider  string
	Model     string
	// Timeout, when positive, bounds the turn with an additional deadline.
	// Zero leaves the caller's context as the only deadline.
	Timeout time.Duration
	// Permissions, when non-nil, temporarily replaces the session-scoped
	// permission rules for this turn and restores the previous rules
	// afterwards. Routine turns use it to confine untrusted model output to
	// memory paths instead of relying on prompt instructions.
	Permissions []PermissionRule

	// OnEvent, when non-nil, receives the session's events (idle, text deltas,
	// tool activity) while the turn runs. The event stream is best-effort: the
	// turn waits a bounded time for the subscription to be established before
	// admitting the prompt, but a subscription failure never fails the turn.
	OnEvent EventHandler
	// OnInteraction, when non-nil, receives every headless form/permission
	// settlement outcome. It is best-effort and must not block: settlement is
	// already decided when it runs. A nil callback falls back to the daemon log.
	OnInteraction func(InteractionSettlement)
	// OnAdmitted, when non-nil, runs after the input is durably admitted and
	// before the turn is collected. Callers persist the marker here so the turn
	// can be re-collected after a restart. An error aborts the turn, and the
	// returned result still carries the admitted marker.
	OnAdmitted func(Admitted) error
	// OnCollected, when non-nil, runs after the turn's output is collected and
	// before the session lock is released. Callers use it to consume a recovery
	// marker while the turn still owns the session, so recovery cannot collect
	// the same output concurrently.
	OnCollected func(*TurnResult) error
	// OnLockAcquired, when non-nil, runs after the per-session turn lock is
	// held and before the input is admitted. Callers use it to fence a stale
	// owner (for example an expired route claim) before OpenCode accepts work.
	OnLockAcquired func() error
}

// SyntheticEvent is an external/background event admitted to a session
// (SPEC §5.1: "external events are first-class — no faking the user").
type SyntheticEvent struct {
	Text        string
	Description string
	Metadata    map[string]any
	// Delivery is "steer" (interrupt now) or "queue" (next idle). Empty
	// lets the server default apply (steer).
	Delivery string
}

// ToolActivity is one tool part observed in an assistant message. It is a
// post-turn summary: tools are reported once the message is collected, not
// streamed while they run.
type ToolActivity struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	ID     string `json:"id"`
}

// TurnResult is the outcome of one agent turn (mirrors the retired
// runner.RunResult so callers keep their shape).
type TurnResult struct {
	SessionID    string
	ResponseText string
	// InputID is the admitted inbox item that started the turn.
	InputID string
	// MessageIDs are the assistant message IDs produced after InputID.
	MessageIDs []string
	// ToolActivity lists the tool parts of those assistant messages in
	// message/content order.
	ToolActivity []ToolActivity
}

// RunUserTurn drives one real user turn: ensure session, sync agent/model,
// admit a prompt, wait for the agent loop to go idle, and collect the reply.
// An empty sessionID creates a fresh session rooted at cfg.Workspace.
//
// Every turn is headless: pending forms and permission requests are settled
// before the prompt and while the turn runs. A turn that cannot settle one
// fails with ErrInteractionUnsettled instead of waiting forever.
func (c *Client) RunUserTurn(ctx context.Context, sessionID, text string, cfg TurnConfig) (result *TurnResult, err error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	sessionID = strings.TrimSpace(sessionID)

	turnCtx, cancelTurn := context.WithCancelCause(ctx)
	defer cancelTurn(nil)

	var held *lock.Lock
	defer func() {
		if held == nil {
			return
		}
		if releaseErr := held.Release(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("opencode: release session lock: %w", releaseErr))
		}
	}()

	// Existing sessions are locked before any session mutation so two
	// callers cannot interleave prompts on the same conversation.
	if sessionID != "" {
		held, err = lock.AcquireSession(turnCtx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("opencode: lock session %s: %w", sessionID, err)
		}
	}

	info, created, err := c.getOrCreateSession(turnCtx, sessionID, cfg)
	if err != nil {
		return nil, turnCause(turnCtx, err)
	}

	// A new session id is unique to this call, so it only needs a lock once
	// the server has assigned it.
	if created {
		held, err = lock.AcquireSession(turnCtx, info.ID)
		if err != nil {
			return &TurnResult{SessionID: info.ID}, fmt.Errorf("opencode: lock session %s: %w", info.ID, err)
		}
	}

	if err := callLockHook(cfg); err != nil {
		return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, err)
	}

	settler := newInteractionSettler(c, info.ID, cfg.OnInteraction)
	stopSettler := func() error { return nil }
	settlerStarted := false
	settlerStopped := false
	restore := func() error { return nil }
	// Cleanup order matters: stop the poller before restoring the session's
	// previous permissions, and keep the session lock until both are done.
	defer func() {
		if settlerStarted && !settlerStopped {
			if stopErr := stopSettler(); stopErr != nil {
				err = errors.Join(err, stopErr)
			}
		}
		if restoreErr := restore(); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	if created {
		stopSettler = settler.start(turnCtx, cancelTurn)
		settlerStarted = true
	} else {
		settled, sweepErr := c.preSweepTurn(turnCtx, settler)
		if sweepErr != nil {
			return &TurnResult{SessionID: info.ID}, sweepErr
		}
		stopSettler = settler.start(turnCtx, cancelTurn)
		settlerStarted = true
		if settled > 0 {
			if err := c.waitSettledSessionIdle(turnCtx, info.ID); err != nil {
				return &TurnResult{SessionID: info.ID}, err
			}
		}
	}

	if !created {
		if err := c.syncSessionSettings(turnCtx, info, cfg); err != nil {
			return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, err)
		}
	}

	restoreFn, restoreErr := c.restrictSession(turnCtx, info, cfg.Permissions)
	if restoreErr != nil {
		return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, restoreErr)
	}
	restore = restoreFn

	stopEvents := c.startEventObserver(turnCtx, info.ID, cfg.OnEvent)
	defer stopEvents()

	item, err := c.Prompt(turnCtx, info.ID, text)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, &PromptError{
			Err:       turnCause(turnCtx, err),
			Ambiguous: promptOutcomeUnknown(err),
		}
	}
	if err := admitInput(cfg, info.ID, item); err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, turnCause(turnCtx, err)
	}
	turn, err := c.collectTurn(turnCtx, info.ID, item.ID)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
	}
	if cfg.OnCollected != nil {
		if err := cfg.OnCollected(turn); err != nil {
			return turn, turnCause(turnCtx, err)
		}
	}

	if stopErr := stopSettler(); stopErr != nil {
		settlerStopped = true
		return turn, stopErr
	}
	settlerStopped = true
	if sweepErr := settler.finalSweep(turnCtx); sweepErr != nil {
		return turn, fmt.Errorf("%w: final sweep: %v", ErrInteractionUnsettled, sweepErr)
	}
	return turn, nil
}

// RunSyntheticTurn admits an external event and collects the agent's reaction.
// An empty sessionID creates a fresh session rooted at cfg.Workspace.
func (c *Client) RunSyntheticTurn(ctx context.Context, sessionID string, ev SyntheticEvent, cfg TurnConfig) (result *TurnResult, err error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	sessionID = strings.TrimSpace(sessionID)

	turnCtx, cancelTurn := context.WithCancelCause(ctx)
	defer cancelTurn(nil)

	var held *lock.Lock
	defer func() {
		if held == nil {
			return
		}
		if releaseErr := held.Release(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("opencode: release session lock: %w", releaseErr))
		}
	}()

	if sessionID != "" {
		held, err = lock.AcquireSession(turnCtx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("opencode: lock session %s: %w", sessionID, err)
		}
	}

	info, created, err := c.getOrCreateSession(turnCtx, sessionID, cfg)
	if err != nil {
		return nil, turnCause(turnCtx, err)
	}

	if created {
		held, err = lock.AcquireSession(turnCtx, info.ID)
		if err != nil {
			return &TurnResult{SessionID: info.ID}, fmt.Errorf("opencode: lock session %s: %w", info.ID, err)
		}
	}

	if err := callLockHook(cfg); err != nil {
		return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, err)
	}

	settler := newInteractionSettler(c, info.ID, cfg.OnInteraction)
	stopSettler := func() error { return nil }
	settlerStarted := false
	settlerStopped := false
	restore := func() error { return nil }
	// Cleanup order matters: stop the poller before restoring the session's
	// previous permissions, and keep the session lock until both are done.
	defer func() {
		if settlerStarted && !settlerStopped {
			if stopErr := stopSettler(); stopErr != nil {
				err = errors.Join(err, stopErr)
			}
		}
		if restoreErr := restore(); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	if created {
		stopSettler = settler.start(turnCtx, cancelTurn)
		settlerStarted = true
	} else {
		settled, sweepErr := c.preSweepTurn(turnCtx, settler)
		if sweepErr != nil {
			return &TurnResult{SessionID: info.ID}, sweepErr
		}
		stopSettler = settler.start(turnCtx, cancelTurn)
		settlerStarted = true
		if settled > 0 {
			if err := c.waitSettledSessionIdle(turnCtx, info.ID); err != nil {
				return &TurnResult{SessionID: info.ID}, err
			}
		}
	}

	if !created {
		if err := c.syncSessionSettings(turnCtx, info, cfg); err != nil {
			return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, err)
		}
	}

	restoreFn, restoreErr := c.restrictSession(turnCtx, info, cfg.Permissions)
	if restoreErr != nil {
		return &TurnResult{SessionID: info.ID}, turnCause(turnCtx, restoreErr)
	}
	restore = restoreFn

	stopEvents := c.startEventObserver(turnCtx, info.ID, cfg.OnEvent)
	defer stopEvents()

	item, err := c.Synthetic(turnCtx, info.ID, ev.Text, ev.Description, ev.Metadata, ev.Delivery)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, &PromptError{
			Err:       turnCause(turnCtx, err),
			Ambiguous: promptOutcomeUnknown(err),
		}
	}
	if err := admitInput(cfg, info.ID, item); err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, turnCause(turnCtx, err)
	}
	turn, err := c.collectTurn(turnCtx, info.ID, item.ID)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
	}
	if cfg.OnCollected != nil {
		if err := cfg.OnCollected(turn); err != nil {
			return turn, turnCause(turnCtx, err)
		}
	}

	if stopErr := stopSettler(); stopErr != nil {
		settlerStopped = true
		return turn, stopErr
	}
	settlerStopped = true
	if sweepErr := settler.finalSweep(turnCtx); sweepErr != nil {
		return turn, fmt.Errorf("%w: final sweep: %v", ErrInteractionUnsettled, sweepErr)
	}
	return turn, nil
}

func inboxID(item *InboxItem) string {
	if item == nil {
		return ""
	}
	return item.ID
}

// admitInput hands the newly admitted marker to the caller's persistence hook.
func admitInput(cfg TurnConfig, sessionID string, item *InboxItem) error {
	if cfg.OnAdmitted == nil {
		return nil
	}
	return cfg.OnAdmitted(Admitted{SessionID: sessionID, InputID: inboxID(item)})
}

func callLockHook(cfg TurnConfig) error {
	if cfg.OnLockAcquired == nil {
		return nil
	}
	return cfg.OnLockAcquired()
}

// getOrCreateSession creates the session when missing. It deliberately does not
// mutate an existing session: the headless pre-sweep runs before the agent/model
// switches, so a session blocked on a stale interaction can be cleared first.
func (c *Client) getOrCreateSession(ctx context.Context, sessionID string, cfg TurnConfig) (*SessionInfo, bool, error) {
	if sessionID == "" {
		info, err := c.CreateSession(ctx, cfg.Workspace, cfg.Agent, modelRef(cfg))
		if err != nil {
			return nil, false, err
		}
		return info, true, nil
	}
	info, err := c.GetSession(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	return info, false, nil
}

// syncSessionSettings brings an existing session's agent/model in line with cfg
// after the pre-sweep has settled any stale interaction.
func (c *Client) syncSessionSettings(ctx context.Context, info *SessionInfo, cfg TurnConfig) error {
	model := modelRef(cfg)
	if cfg.Agent != "" && info.Agent != cfg.Agent {
		if err := c.SwitchAgent(ctx, info.ID, cfg.Agent); err != nil {
			return err
		}
	}
	if model != nil && (info.Model == nil || info.Model.ID != model.ID || info.Model.ProviderID != model.ProviderID) {
		if err := c.SwitchModel(ctx, info.ID, *model); err != nil {
			return err
		}
	}
	return nil
}

// preSweepTurn clears interactions left by an earlier turn. It is fail-closed:
// if the sweep cannot verify and settle them, the turn does not start.
func (c *Client) preSweepTurn(ctx context.Context, settler *interactionSettler) (int, error) {
	sweepCtx, cancel := context.WithTimeout(ctx, interactionSweepTimeout)
	defer cancel()
	settled, err := settler.sweep(sweepCtx)
	if err != nil {
		return 0, fmt.Errorf("%w: pre-sweep: %v", ErrInteractionUnsettled, err)
	}
	return settled, nil
}

// waitSettledSessionIdle waits for an execution that the pre-sweep just
// resumed. Without this, agent/model switches would race a busy session.
func (c *Client) waitSettledSessionIdle(ctx context.Context, sessionID string) error {
	idleCtx, cancel := context.WithTimeout(ctx, preTurnIdleTimeout)
	defer cancel()
	err := c.Wait(idleCtx, sessionID)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) && context.Cause(ctx) == nil {
		return fmt.Errorf("%w: session did not go idle after pre-sweep", ErrInteractionUnsettled)
	}
	return turnCause(ctx, err)
}

// turnCause surfaces a settler cancellation instead of the bare
// context.Canceled that net/http wraps around it.
func turnCause(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(err, cause) {
		return err
	}
	return errors.Join(cause, err)
}

// permissionRestoreTimeout bounds the restore call after a turn; it must not
// inherit the turn's canceled/deadline context.
var permissionRestoreTimeout = 10 * time.Second

// restrictSession applies a per-turn permission ruleset and returns a restore
// function. It is a no-op when permissions is nil, so ordinary user turns do
// not touch session state. A failed restriction aborts the turn (fail closed);
// a failed restore is surfaced to the caller but leaves the safer rules in
// place.
func (c *Client) restrictSession(ctx context.Context, info *SessionInfo, permissions []PermissionRule) (func() error, error) {
	if permissions == nil {
		return func() error { return nil }, nil
	}
	previous := info.Permissions
	if err := c.SetSessionPermissions(ctx, info.ID, permissions); err != nil {
		return nil, fmt.Errorf("opencode: apply session permissions: %w", err)
	}
	return func() error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), permissionRestoreTimeout)
		defer cancel()
		if err := c.SetSessionPermissions(restoreCtx, info.ID, previous); err != nil {
			return fmt.Errorf("opencode: restore session permissions: %w", err)
		}
		return nil
	}, nil
}

// eventReadyTimeout bounds how long a turn waits for the event stream to be
// established before admitting the prompt. A var so tests can shorten it.
var eventReadyTimeout = 2 * time.Second

// startEventObserver runs an optional best-effort event subscription for the
// duration of a turn. When a handler is set it waits, bounded by
// eventReadyTimeout, for the stream to be established so events emitted at the
// start of the turn are not missed; a failure to establish never fails the
// turn. It returns a cancel function; callers must always call it (normally
// with defer).
func (c *Client) startEventObserver(ctx context.Context, sessionID string, handle EventHandler) func() {
	if handle == nil {
		return func() {}
	}
	eventCtx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	var readyOnce sync.Once
	signalReady := func() {
		readyOnce.Do(func() { close(ready) })
	}
	go func() {
		_ = c.subscribeEvents(eventCtx, []string{
			"session.idle",
			"session.text.delta",
			"session.tool.called",
			"session.tool.success",
			"session.tool.failed",
		}, func(event Event) error {
			if event.SessionID() != sessionID {
				return nil
			}
			return handle(event)
		}, signalReady)
	}()
	select {
	case <-ready:
	case <-ctx.Done():
	case <-time.After(eventReadyTimeout):
	}
	return cancel
}

// collectTurn waits for the agent loop to go idle and returns the assistant
// text produced after markerID. A queued input can still be pending when the
// loop first reports idle, so it re-waits until the marker is delivered or the
// deadline passes.
func (c *Client) collectTurn(ctx context.Context, sessionID, markerID string) (*TurnResult, error) {
	for {
		if err := c.Wait(ctx, sessionID); err != nil {
			return nil, turnCause(ctx, err)
		}
		msgs, delivered, truncated, err := c.messagesAfter(ctx, sessionID, markerID)
		if err != nil {
			return nil, turnCause(ctx, err)
		}
		if delivered {
			return newTurnResult(sessionID, markerID, msgs), nil
		}
		if truncated {
			// The marker lies beyond the collection window: it is either
			// still pending (long session, keep waiting) or buried under a
			// huge turn (its reply cannot be bounded, fail fast).
			pending, err := c.InboxPending(ctx, sessionID, markerID)
			if err != nil {
				return nil, turnCause(ctx, err)
			}
			if !pending {
				return nil, ErrTurnTruncated
			}
		}
		select {
		case <-ctx.Done():
			return nil, turnCause(ctx, fmt.Errorf("opencode: timed out waiting for turn %s: %w", markerID, ctx.Err()))
		case <-time.After(syntheticRecheckGap):
		}
	}
}

// CollectDelivered re-collects a turn that was admitted earlier without waiting
// for it. It performs a single probe, which makes it safe to call from recovery
// paths: a completed turn is returned in full, and a still-running or
// still-queued one reports delivered=false instead of blocking or re-prompting.
func (c *Client) CollectDelivered(ctx context.Context, sessionID, markerID string) (*TurnResult, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	markerID = strings.TrimSpace(markerID)
	if sessionID == "" || markerID == "" {
		return nil, false, fmt.Errorf("opencode: session id and marker id are required")
	}
	msgs, delivered, truncated, err := c.messagesAfter(ctx, sessionID, markerID)
	if err != nil {
		return nil, false, err
	}
	if delivered {
		return newTurnResult(sessionID, markerID, msgs), true, nil
	}
	if truncated {
		// The marker fell outside the collection window. It is either still
		// queued or buried under a turn too large to bound; only the queue
		// can tell them apart.
		pending, err := c.InboxPending(ctx, sessionID, markerID)
		if err != nil {
			return nil, false, err
		}
		if !pending {
			return nil, false, ErrTurnTruncated
		}
	}
	return nil, false, nil
}

// newTurnResult projects the collected messages after a marker into the turn
// result. Both the waiting and the re-collecting paths use it so a recovered
// turn is shaped exactly like a live one.
func newTurnResult(sessionID, markerID string, msgs []Message) *TurnResult {
	return &TurnResult{
		SessionID:    sessionID,
		ResponseText: assistantText(msgs),
		InputID:      markerID,
		MessageIDs:   assistantMessageIDs(msgs),
		ToolActivity: assistantToolActivity(msgs),
	}
}

// assistantText concatenates the text parts of the assistant messages. Text
// parts within one message stay joined as-is; successive messages are separated
// by a blank line.
func assistantText(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Type != "assistant" {
			continue
		}
		var text strings.Builder
		for _, part := range m.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		if text.Len() == 0 {
			continue
		}
		if b.Len() > 0 {
			// Successive assistant messages are separate paragraphs: a blank
			// line keeps them visually distinct instead of running together.
			b.WriteString("\n\n")
		}
		b.WriteString(text.String())
	}
	return b.String()
}

// assistantMessageIDs returns the IDs of assistant messages that carry text.
func assistantMessageIDs(msgs []Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Type != "assistant" {
			continue
		}
		for _, part := range m.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				ids = append(ids, m.ID)
				break
			}
		}
	}
	return ids
}

// assistantToolActivity returns the tool parts of the assistant messages in
// message and content order. Decoding is defensive: malformed or unrecognized
// payloads are skipped so they can never fail the turn.
func assistantToolActivity(msgs []Message) []ToolActivity {
	var activity []ToolActivity
	for _, m := range msgs {
		if m.Type != "assistant" {
			continue
		}
		activity = append(activity, messageToolActivity(m.Raw)...)
	}
	return activity
}

// messageToolActivity decodes the tool parts from one raw assistant message.
func messageToolActivity(raw json.RawMessage) []ToolActivity {
	if len(raw) == 0 {
		return nil
	}
	var message struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil
	}
	var activity []ToolActivity
	for _, rawPart := range message.Content {
		var part struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil || part.Type != "tool" {
			continue
		}
		entry := ToolActivity{
			Name:   strings.TrimSpace(part.Name),
			Status: strings.TrimSpace(part.State.Status),
			ID:     strings.TrimSpace(part.ID),
		}
		if entry.Name == "" && entry.Status == "" && entry.ID == "" {
			continue
		}
		activity = append(activity, entry)
	}
	return activity
}

// modelRef maps the caller's provider/model strings to a Model.Ref, or nil when
// either is unset.
func modelRef(cfg TurnConfig) *ModelRef {
	if cfg.Model == "" || cfg.Provider == "" {
		return nil
	}
	return &ModelRef{ID: cfg.Model, ProviderID: cfg.Provider}
}
