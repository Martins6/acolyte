package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
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

	st, err := store.Open(agent.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()

	if strings.TrimSpace(sessionID) == "" {
		if session, ok, err := st.Orch().LatestSession(); err != nil {
			return err
		} else if ok {
			sessionID = session.OpenCodeSessionID
		}
	}

	client, err := opencode.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("opencode service unavailable: %w", err)
	}
	result, err := client.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        text,
		Description: "Inbox event for " + agent.Name,
		Metadata:    map[string]any{"channel": "inbox"},
	}, opencode.TurnConfig{Workspace: agent.Workspace})
	if err != nil {
		return err
	}
	if _, err := st.Orch().UpsertSession("inbox:"+agent.Name, result.SessionID, time.Now().UTC()); err != nil {
		return err
	}

	if result.ResponseText != "" {
		if _, err := config.Load(""); err == nil {
			if err := database.Init(legacyWorkspace()); err == nil {
				if userID := config.GetAllowedUserChatID(); userID != 0 {
					_, _ = database.InsertNotification(userID, result.ResponseText)
				}
			}
		}
	}

	fmt.Fprintf(w, "Inbox delivered to session %s.\n", result.SessionID)
	return nil
}
