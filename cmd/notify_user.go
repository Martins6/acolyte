package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/storepool"
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

func runNotifyUser(ctx context.Context, w io.Writer, text, agentName, workspace string) error {
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
	sink := comm.OutboxSink{Stores: pool}
	if err := sink.NotifyUser(ctx, agent, text); err != nil {
		return err
	}
	fmt.Fprintf(w, "Notification queued for agent %q.\n", agent.Name)
	return nil
}
