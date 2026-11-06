package opencode

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// turnTimeout matches the retired CLI runner's 5-minute cap.
const turnTimeout = 5 * time.Minute

// syntheticRecheckGap is how long to pause before re-waiting when a queued
// input has not been delivered yet.
const syntheticRecheckGap = 2 * time.Second

// TurnConfig carries the per-turn settings the callers already hold.
type TurnConfig struct {
	Workspace string
	Agent     string
	Provider  string
	Model     string
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
}

// RunUserTurn drives one real user turn: ensure session, sync agent/model,
// admit a prompt, wait for the agent loop to go idle, and collect the reply.
// An empty sessionID creates a fresh session rooted at cfg.Workspace.
func (c *Client) RunUserTurn(ctx context.Context, sessionID, text string, cfg TurnConfig) (*TurnResult, error) {
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	sessionID, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
	}

	item, err := c.Prompt(ctx, sessionID, text)
	if err != nil {
		return nil, err
	}
	return c.collectTurn(ctx, sessionID, item.ID)
}

// RunSyntheticTurn admits an external event and collects the agent's reaction.
// An empty sessionID creates a fresh session rooted at cfg.Workspace.
func (c *Client) RunSyntheticTurn(ctx context.Context, sessionID string, ev SyntheticEvent, cfg TurnConfig) (*TurnResult, error) {
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	sessionID, err := c.ensureSession(ctx, sessionID, cfg)
	if err != nil {
		return nil, err
	}

	item, err := c.Synthetic(ctx, sessionID, ev.Text, ev.Description, ev.Metadata, ev.Delivery)
	if err != nil {
		return nil, err
	}
	return c.collectTurn(ctx, sessionID, item.ID)
}

// ensureSession creates the session when missing and brings its agent/model in
// line with cfg on existing sessions.
func (c *Client) ensureSession(ctx context.Context, sessionID string, cfg TurnConfig) (string, error) {
	model := modelRef(cfg)

	if sessionID == "" {
		info, err := c.CreateSession(ctx, cfg.Workspace, cfg.Agent, model)
		if err != nil {
			return "", err
		}
		return info.ID, nil
	}

	info, err := c.GetSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if cfg.Agent != "" && info.Agent != cfg.Agent {
		if err := c.SwitchAgent(ctx, sessionID, cfg.Agent); err != nil {
			return "", err
		}
	}
	if model != nil && (info.Model == nil || info.Model.ID != model.ID || info.Model.ProviderID != model.ProviderID) {
		if err := c.SwitchModel(ctx, sessionID, *model); err != nil {
			return "", err
		}
	}
	return sessionID, nil
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
		msgs, delivered, err := c.messagesAfter(ctx, sessionID, markerID)
		if err != nil {
			return nil, err
		}
		if delivered {
			return &TurnResult{SessionID: sessionID, ResponseText: assistantText(msgs)}, nil
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

// modelRef maps the caller's provider/model strings to a Model.Ref, or nil when
// either is unset.
func modelRef(cfg TurnConfig) *ModelRef {
	if cfg.Model == "" || cfg.Provider == "" {
		return nil
	}
	return &ModelRef{ID: cfg.Model, ProviderID: cfg.Provider}
}
