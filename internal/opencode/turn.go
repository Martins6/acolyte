package opencode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// turnTimeout matches the retired CLI runner's 5-minute cap.
const turnTimeout = 5 * time.Minute

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
	// Permissions, when non-nil, temporarily replaces the session-scoped
	// permission rules for this turn and restores the previous rules
	// afterwards. Routine turns use it to confine untrusted model output to
	// memory paths instead of relying on prompt instructions.
	Permissions []PermissionRule
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

// TurnResult is the outcome of one agent turn (mirrors the retired
// runner.RunResult so callers keep their shape).
type TurnResult struct {
	SessionID    string
	ResponseText string
	// InputID is the admitted inbox item that started the turn.
	InputID string
	// MessageIDs are the assistant message IDs produced after InputID.
	MessageIDs []string
}

// RunUserTurn drives one real user turn: ensure session, sync agent/model,
// admit a prompt, wait for the agent loop to go idle, and collect the reply.
// An empty sessionID creates a fresh session rooted at cfg.Workspace.
func (c *Client) RunUserTurn(ctx context.Context, sessionID, text string, cfg TurnConfig) (result *TurnResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	info, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
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
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	info, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
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

// modelRef maps the caller's provider/model strings to a Model.Ref, or nil when
// either is unset.
func modelRef(cfg TurnConfig) *ModelRef {
	if cfg.Model == "" || cfg.Provider == "" {
		return nil
	}
	return &ModelRef{ID: cfg.Model, ProviderID: cfg.Provider}
}
