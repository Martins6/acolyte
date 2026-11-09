package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/database"
	"github.com/spf13/cobra"
)

var (
	notifyUserAgent     string
	notifyUserWorkspace string
)

var notifyUserCmd = &cobra.Command{
	Use:   "notify-user \"message\"",
	Short: "Notify the user only (never the agent, never a session)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runNotifyUser(cmd.Context(), cmd.OutOrStdout(), args[0], notifyUserAgent, notifyUserWorkspace)
	},
}

func init() {
	rootCmd.AddCommand(notifyUserCmd)
	notifyUserCmd.Flags().StringVar(&notifyUserAgent, "agent-name", "", "target agent name (optional)")
	notifyUserCmd.Flags().StringVar(&notifyUserWorkspace, "workspace-folder", "", "target agent workspace (optional)")
}

func runNotifyUser(_ context.Context, w io.Writer, text, agentName, workspace string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message is required")
	}
	if strings.TrimSpace(agentName) != "" || strings.TrimSpace(workspace) != "" {
		if _, err := resolveTargetAgent(agentName, workspace); err != nil {
			return err
		}
	}
	if _, err := config.Load(""); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := database.Init(legacyWorkspace()); err != nil {
		return fmt.Errorf("initialize legacy notifier database: %w", err)
	}
	userID := config.GetAllowedUserChatID()
	if userID == 0 {
		return fmt.Errorf("no Telegram user chat id is resolved yet; message the bot first")
	}
	if _, err := database.InsertNotification(userID, text); err != nil {
		return fmt.Errorf("queue notification: %w", err)
	}
	fmt.Fprintln(w, "Notification queued for the user.")
	return nil
}
