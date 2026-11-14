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

// syntheticRecheckGap is how long to pause before re-waiting when a queued
// input has not been delivered yet (a var so tests can shorten it).
var syntheticRecheckGap = 2 * time.Second

// TurnConfig carries the per-turn settings the callers already hold.
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
func (c *Client) RunUserTurn(ctx context.Context, sessionID, text string, cfg TurnConfig) (result *TurnResult, err error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	sessionID = strings.TrimSpace(sessionID)

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
		held, err = lock.AcquireSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("opencode: lock session %s: %w", sessionID, err)
		}
	}

	info, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
	}

	// A new session id is unique to this call, so it only needs a lock once
	// the server has assigned it.
	if sessionID == "" {
		held, err = lock.AcquireSession(ctx, info.ID)
		if err != nil {
			return &TurnResult{SessionID: info.ID}, fmt.Errorf("opencode: lock session %s: %w", info.ID, err)
		}
	}

	restore, err := c.restrictSession(ctx, info, cfg.Permissions)
	if err != nil {
		return &TurnResult{SessionID: info.ID}, err
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	stopEvents := c.startEventObserver(ctx, info.ID, cfg.OnEvent)
	defer stopEvents()

	item, err := c.Prompt(ctx, info.ID, text)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
	}
	turn, err := c.collectTurn(ctx, info.ID, item.ID)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
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
		held, err = lock.AcquireSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("opencode: lock session %s: %w", sessionID, err)
		}
	}

	info, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
	}

	if sessionID == "" {
		held, err = lock.AcquireSession(ctx, info.ID)
		if err != nil {
			return &TurnResult{SessionID: info.ID}, fmt.Errorf("opencode: lock session %s: %w", info.ID, err)
		}
	}

	restore, err := c.restrictSession(ctx, info, cfg.Permissions)
	if err != nil {
		return &TurnResult{SessionID: info.ID}, err
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	stopEvents := c.startEventObserver(ctx, info.ID, cfg.OnEvent)
	defer stopEvents()

	item, err := c.Synthetic(ctx, info.ID, ev.Text, ev.Description, ev.Metadata, ev.Delivery)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
	}
	turn, err := c.collectTurn(ctx, info.ID, item.ID)
	if err != nil {
		return &TurnResult{SessionID: info.ID, InputID: inboxID(item)}, err
	}
	return turn, nil
}

func inboxID(item *InboxItem) string {
	if item == nil {
		return ""
	}
	return item.ID
}

// ensureSession creates the session when missing and brings its agent/model in
// line with cfg on existing sessions.
func (c *Client) ensureSession(ctx context.Context, sessionID string, cfg TurnConfig) (*SessionInfo, error) {
	model := modelRef(cfg)

	if sessionID == "" {
		info, err := c.CreateSession(ctx, cfg.Workspace, cfg.Agent, model)
		if err != nil {
			return nil, err
		}
		return info, nil
	}

	info, err := c.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if cfg.Agent != "" && info.Agent != cfg.Agent {
		if err := c.SwitchAgent(ctx, sessionID, cfg.Agent); err != nil {
			return nil, err
		}
	}
	if model != nil && (info.Model == nil || info.Model.ID != model.ID || info.Model.ProviderID != model.ProviderID) {
		if err := c.SwitchModel(ctx, sessionID, *model); err != nil {
			return nil, err
		}
	}
	return info, nil
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
			return nil, err
		}
		msgs, delivered, truncated, err := c.messagesAfter(ctx, sessionID, markerID)
		if err != nil {
			return nil, err
		}
		if delivered {
			return &TurnResult{
				SessionID:    sessionID,
				ResponseText: assistantText(msgs),
				InputID:      markerID,
				MessageIDs:   assistantMessageIDs(msgs),
				ToolActivity: assistantToolActivity(msgs),
			}, nil
		}
		if truncated {
			// The marker lies beyond the collection window: it is either
			// still pending (long session, keep waiting) or buried under a
			// huge turn (its reply cannot be bounded, fail fast).
			pending, err := c.InboxPending(ctx, sessionID, markerID)
			if err != nil {
				return nil, err
			}
			if !pending {
				return nil, ErrTurnTruncated
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("opencode: timed out waiting for turn %s: %w", markerID, ctx.Err())
		case <-time.After(syntheticRecheckGap):
		}
	}
}

// assistantText concatenates the text parts of the assistant messages.
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
			b.WriteString("\n")
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
