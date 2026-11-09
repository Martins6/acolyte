package cron

import (
	"context"
	"fmt"

	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
)

// Sink routes cron output into the user channel or the agent inbox.
type Sink interface {
	NotifyUser(ctx context.Context, agent registry.Agent, text string) error
	// Inbox returns the agent reply and the OpenCode session used for the turn.
	Inbox(ctx context.Context, agent registry.Agent, sessionID, text string, meta map[string]any) (string, string, error)
}

// LegacySink is the transitional Phase 2 sink. It queues user notifications in
// the legacy notifier database and runs inbox events through the OpenCode turn
// runner. Phase 3 replaces it with the Communication/Envelope adapter.
type LegacySink struct {
	UserID int64
	Turn   TurnRunner
}

// NotifyUser queues a user-only notification.
func (s LegacySink) NotifyUser(_ context.Context, _ registry.Agent, text string) error {
	if s.UserID == 0 {
		return fmt.Errorf("cron: no legacy user chat id is resolved yet")
	}
	if _, err := database.InsertNotification(s.UserID, text); err != nil {
		return fmt.Errorf("cron: queue notification: %w", err)
	}
	return nil
}

// Inbox admits a synthetic event and queues the agent's reply for the user.
func (s LegacySink) Inbox(ctx context.Context, agent registry.Agent, sessionID, text string, meta map[string]any) (string, string, error) {
	if s.Turn == nil {
		return "", "", fmt.Errorf("cron: inbox turn runner is not configured")
	}
	result, err := s.Turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        text,
		Description: "Inbox event",
		Metadata:    meta,
	}, opencode.TurnConfig{Workspace: agent.Workspace})
	if err != nil {
		created := ""
		if result != nil {
			created = result.SessionID
		}
		return "", created, err
	}
	if result.ResponseText != "" {
		if err := s.NotifyUser(ctx, agent, result.ResponseText); err != nil {
			return result.ResponseText, result.SessionID, err
		}
	}
	return result.ResponseText, result.SessionID, nil
}
