package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/storepool"
	"github.com/spf13/cobra"
)

var (
	inboxAgent     string
	inboxWorkspace string
	inboxSessionID string
)

var inboxCmd = &cobra.Command{
	Use:   "inbox \"message\"",
	Short: "Deliver an external event to the agent on a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInbox(cmd.Context(), cmd.OutOrStdout(), args[0], inboxAgent, inboxWorkspace, inboxSessionID)
	},
}

func init() {
	rootCmd.AddCommand(inboxCmd)
	inboxCmd.Flags().StringVar(&inboxAgent, "agent-name", "", "target agent name")
	inboxCmd.Flags().StringVar(&inboxWorkspace, "workspace-folder", "", "target agent workspace")
	inboxCmd.Flags().StringVar(&inboxSessionID, "session-id", "", "optional OpenCode session id")
}

func runInbox(ctx context.Context, w io.Writer, text, agentName, workspace, sessionID string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message is required")
	}
	agent, err := resolveTargetAgent(agentName, workspace)
	if err != nil {
		return err
	}

	pool := storepool.New()
	defer pool.CloseAll()
	explicit := strings.TrimSpace(sessionID) != ""
	if explicit {
		st, release, err := pool.Acquire(agent.Workspace)
		if err != nil {
			return err
		}
		err = verifySessionMapping(st, sessionID)
		release()
		if err != nil {
			return err
		}
	}

	client, err := openCodeEnsure(ctx)
	if err != nil {
		return fmt.Errorf("opencode service unavailable: %w", err)
	}
	sink := comm.OutboxSink{Stores: pool, Turn: client}
	reply, resultSession, err := sink.Inbox(ctx, agent, sessionID, text, map[string]any{"channel": "inbox"})
	if err != nil {
		return err
	}

	if !explicit && resultSession != "" {
		st, release, err := pool.Acquire(agent.Workspace)
		if err != nil {
			return err
		}
		if _, err := st.Orch().UpsertSession("inbox:"+agent.Name, resultSession, time.Now().UTC()); err != nil {
			release()
			return err
		}
		release()
	}

	if resultSession == "" {
		fmt.Fprintln(w, "Inbox delivered.")
	} else {
		fmt.Fprintf(w, "Inbox delivered to session %s.\n", resultSession)
	}
	if strings.TrimSpace(reply) != "" {
		fmt.Fprintf(w, "Reply:\n%s\n", reply)
	}
	return nil
}
