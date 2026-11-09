package cmd

import (
	"context"

	"github.com/martins6/acolyte/internal/opencode"
)

// turnClient is the OpenCode surface used by the CLI primitives. It is an
// interface so command tests can use a fake turn runner.
type turnClient interface {
	RunUserTurn(ctx context.Context, sessionID, text string, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
	RunSyntheticTurn(ctx context.Context, sessionID string, ev opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
}

var openCodeEnsure = func(ctx context.Context) (turnClient, error) {
	client, err := opencode.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}
