package cmd

import (
	"context"
	"os"
	"os/exec"

	"github.com/martins6/acolyte/internal/opencode"
)

// turnClient is the OpenCode surface used by the CLI primitives. It is an
// interface so command tests can use a fake turn runner.
type turnClient interface {
	RunUserTurn(ctx context.Context, sessionID, text string, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
	RunSyntheticTurn(ctx context.Context, sessionID string, ev opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
	SessionMessages(ctx context.Context, sessionID string, maxPages int) ([]opencode.Message, bool, error)
	ListSessions(ctx context.Context) ([]opencode.SessionInfo, error)
}

var openCodeEnsure = func(ctx context.Context) (turnClient, error) {
	client, err := opencode.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

var openCodeServiceURL = opencode.ServiceURL
var openCodeServicePassword = opencode.LoadServicePassword

var runAttachCommand = func(ctx context.Context, args, env []string) error {
	command := exec.CommandContext(ctx, "opencode", args...)
	command.Env = env
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
