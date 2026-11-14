package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
	"github.com/spf13/cobra"
)

var (
	runAgent     string
	runWorkspace string
	runSessionID string
	runContinue  bool
	runJSON      bool
	runVerbose   bool
	runTimeout   time.Duration
)

var runCmd = &cobra.Command{
	Use:   `run "message"`,
	Short: "Run one headless agent turn and print the reply",
	Long: `Run a single real user turn against an agent without Telegram or the TUI.

The session is mapped to the agent's CLI thread (cli:<agent>), so it can be
continued with --continue and inspected with acolyte inspect.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRun(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], runOptions{
			AgentName: runAgent,
			Workspace: runWorkspace,
			SessionID: runSessionID,
			Continue:  runContinue,
			JSON:      runJSON,
			Verbose:   runVerbose,
			Timeout:   runTimeout,
		})
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&runAgent, "agent-name", "", "target agent name")
	runCmd.Flags().StringVar(&runWorkspace, "workspace-folder", "", "target agent workspace")
	runCmd.Flags().StringVar(&runSessionID, "session-id", "", "existing OpenCode session id (must be mapped to the agent)")
	runCmd.Flags().BoolVarP(&runContinue, "continue", "c", false, "continue the agent's last CLI session")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "print one JSON object to stdout")
	runCmd.Flags().BoolVar(&runVerbose, "verbose", false, "print turn diagnostics to stderr")
	runCmd.Flags().DurationVar(&runTimeout, "timeout", 0, "maximum turn duration (0 means no limit)")
}

// runOptions is one `acolyte run` invocation. It is separate from the cobra
// flags so tests can exercise the command without parsing arguments.
type runOptions struct {
	AgentName string
	Workspace string
	SessionID string
	Continue  bool
	JSON      bool
	Verbose   bool
	Timeout   time.Duration
}

// runResult is the --json payload. It is emitted on success and, once the
// agent is resolved, on failure too (with error set and the session id kept
// whenever OpenCode created one).
type runResult struct {
	Agent        string                  `json:"agent"`
	Workspace    string                  `json:"workspace"`
	Thread       string                  `json:"thread"`
	SessionID    string                  `json:"session_id"`
	InputID      string                  `json:"input_id,omitempty"`
	MessageIDs   []string                `json:"message_ids,omitempty"`
	ToolActivity []opencode.ToolActivity `json:"tool_activity,omitempty"`
	Reply        string                  `json:"reply"`
	Error        string                  `json:"error,omitempty"`
	DurationMS   int64                   `json:"duration_ms"`
}

func runRun(ctx context.Context, stdout, stderr io.Writer, text string, opts runOptions) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message is required")
	}
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	if opts.SessionID != "" && opts.Continue {
		return fmt.Errorf("use --session-id or --continue, not both")
	}
	if opts.Timeout < 0 {
		return fmt.Errorf("--timeout must not be negative")
	}

	agent, err := resolveTargetAgent(opts.AgentName, opts.Workspace)
	if err != nil {
		return err
	}
	thread := "cli:" + agent.Name
	out := runResult{
		Agent:     agent.Name,
		Workspace: agent.Workspace,
		Thread:    thread,
	}

	sessionID, err := resolveRunSession(agent.Workspace, agent.Name, thread, opts.SessionID, opts.Continue)
	if err != nil {
		out.Error = err.Error()
		if opts.JSON {
			_ = writeRunJSON(stdout, out)
		}
		return err
	}

	client, err := openCodeEnsure(ctx)
	if err != nil {
		out.Error = err.Error()
		if opts.JSON {
			_ = writeRunJSON(stdout, out)
		}
		return fmt.Errorf("opencode service unavailable: %w", err)
	}

	start := time.Now()
	result, turnErr := client.RunUserTurn(ctx, sessionID, text, opencode.TurnConfig{
		Workspace: agent.Workspace,
		Timeout:   opts.Timeout,
	})
	duration := time.Since(start)
	out.DurationMS = duration.Milliseconds()

	if result != nil {
		out.SessionID = result.SessionID
		out.InputID = result.InputID
		out.MessageIDs = result.MessageIDs
		out.ToolActivity = result.ToolActivity
		out.Reply = result.ResponseText
	}

	var mappingErr error
	if out.SessionID != "" {
		mappingErr = persistRunSession(agent.Workspace, thread, out.SessionID)
	}
	if turnErr != nil {
		turnErr = errors.Join(turnErr, mappingErr)
	} else {
		turnErr = mappingErr
	}
	if turnErr != nil {
		out.Error = turnErr.Error()
	}

	if opts.JSON {
		if encodeErr := writeRunJSON(stdout, out); encodeErr != nil {
			return errors.Join(turnErr, fmt.Errorf("write json output: %w", encodeErr))
		}
	} else if turnErr == nil {
		if writeErr := writeReply(stdout, out.Reply); writeErr != nil {
			return fmt.Errorf("write reply: %w", writeErr)
		}
	}

	if out.SessionID != "" && !opts.JSON {
		fmt.Fprintf(stderr, "session: %s\n", out.SessionID)
	}
	if opts.Verbose && !opts.JSON {
		fmt.Fprintf(stderr, "agent=%s workspace=%s input=%s elapsed=%s\n",
			out.Agent, out.Workspace, out.InputID, duration)
		if len(out.MessageIDs) > 0 {
			fmt.Fprintf(stderr, "messages: %s\n", strings.Join(out.MessageIDs, ", "))
		}
	}

	return turnErr
}

// resolveRunSession resolves the OpenCode session for one run. New runs pass an
// empty id; --continue reads the agent's CLI thread; an explicit session must
// already be mapped to the agent, matching the inbox ownership guard.
func resolveRunSession(workspace, agentName, thread, explicit string, cont bool) (string, error) {
	if explicit == "" && !cont {
		return "", nil
	}
	st, err := store.Open(workspace)
	if err != nil {
		return "", fmt.Errorf("open agent store: %w", err)
	}
	defer st.Close()

	if explicit != "" {
		if err := verifySessionMapping(st, explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}

	session, ok, err := st.Orch().GetSessionByThread(thread)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no previous CLI run session for agent %q; run without --continue or pass --session-id", agentName)
	}
	return session.OpenCodeSessionID, nil
}

func persistRunSession(workspace, thread, sessionID string) error {
	st, err := store.Open(workspace)
	if err != nil {
		return fmt.Errorf("open agent store: %w", err)
	}
	defer st.Close()
	if _, err := st.Orch().UpsertSession(thread, sessionID, time.Now().UTC()); err != nil {
		return fmt.Errorf("persist CLI session: %w", err)
	}
	return nil
}

func writeRunJSON(w io.Writer, out runResult) error {
	return json.NewEncoder(w).Encode(out)
}

func writeReply(w io.Writer, reply string) error {
	if reply == "" {
		return nil
	}
	if !strings.HasSuffix(reply, "\n") {
		reply += "\n"
	}
	_, err := io.WriteString(w, reply)
	return err
}
