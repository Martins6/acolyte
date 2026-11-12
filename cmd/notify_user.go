package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/media"
	"github.com/martins6/acolyte/internal/storepool"
	"github.com/spf13/cobra"
)

var (
	notifyUserAgent     string
	notifyUserWorkspace string
	notifyUserMedia     string
	notifyUserSessionID string
)

var notifyUserCmd = &cobra.Command{
	Use:   "notify-user [message]",
	Short: "Notify the user only (never the agent, never a session)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := ""
		if len(args) > 0 {
			text = args[0]
		}
		return runNotifyUserWithMedia(cmd.Context(), cmd.OutOrStdout(), text,
			notifyUserAgent, notifyUserWorkspace, notifyUserMedia, notifyUserSessionID)
	},
}

func init() {
	rootCmd.AddCommand(notifyUserCmd)
	notifyUserCmd.Flags().StringVar(&notifyUserAgent, "agent-name", "", "target agent name (optional)")
	notifyUserCmd.Flags().StringVar(&notifyUserWorkspace, "workspace-folder", "", "target agent workspace (optional)")
	notifyUserCmd.Flags().StringVar(&notifyUserMedia, "media", "", "single image to send (jpg, jpeg, png, or webp)")
	notifyUserCmd.Flags().StringVar(&notifyUserSessionID, "session-id", "", "optional OpenCode session id for attribution only")
}

func runNotifyUser(ctx context.Context, w io.Writer, text, agentName, workspace string) error {
	return runNotifyUserWithMedia(ctx, w, text, agentName, workspace, "", "")
}

func runNotifyUserWithMedia(ctx context.Context, w io.Writer, text, agentName, workspace, mediaPath, sessionID string) error {
	text = strings.TrimSpace(text)
	mediaPath = strings.TrimSpace(mediaPath)
	if text == "" && mediaPath == "" {
		return fmt.Errorf("message or --media is required")
	}
	agent, err := resolveTargetAgent(agentName, workspace)
	if err != nil {
		return err
	}
	pool := storepool.New()
	defer pool.CloseAll()
	sink := comm.OutboxSink{Stores: pool}
	if mediaPath != "" {
		prepared, err := media.PrepareOutboundImage(agent.Workspace, mediaPath)
		if err != nil {
			return err
		}
		if err := sink.NotifyUserMedia(ctx, agent, text, prepared, sessionID); err != nil {
			return err
		}
		fmt.Fprintf(w, "Photo queued for agent %q.\n", agent.Name)
		return nil
	}
	if err := sink.NotifyUser(ctx, agent, text); err != nil {
		return err
	}
	fmt.Fprintf(w, "Notification queued for agent %q.\n", agent.Name)
	return nil
}
