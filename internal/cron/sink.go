package cron

import (
	"context"

	"github.com/martins6/acolyte/internal/registry"
)

// Sink routes cron output into the user channel or the agent inbox.
type Sink interface {
	NotifyUser(ctx context.Context, agent registry.Agent, text string) error
	// Inbox returns the agent reply and the OpenCode session used for the turn.
	Inbox(ctx context.Context, agent registry.Agent, sessionID, text string, meta map[string]any) (string, string, error)
}
