package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/martins6/acolyte/internal/store"
	"github.com/spf13/cobra"
)

var (
	attachAgent     string
	attachWorkspace string
	attachSession   string
)

var attachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Attach the OpenCode TUI to an Acolyte agent session",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAttach(cmd.Context(), attachAgent, attachWorkspace, attachSession)
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
	attachCmd.Flags().StringVar(&attachAgent, "agent-name", "", "target agent name")
	attachCmd.Flags().StringVar(&attachWorkspace, "workspace-folder", "", "target agent workspace")
	attachCmd.Flags().StringVar(&attachSession, "session-id", "", "OpenCode session id (default: latest)")
}

func runAttach(ctx context.Context, agentName, workspace, sessionID string) error {
	agent, err := resolveTargetAgent(agentName, workspace)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		if st, err := store.OpenReadOnly(agent.Workspace); err == nil {
			if session, ok, err := st.Orch().LatestSession(); err == nil && ok {
				sessionID = session.OpenCodeSessionID
			}
			st.Close()
		}
	}

	serviceURL, err := openCodeServiceURL(ctx)
	if err != nil {
		return err
	}
	password, err := openCodeServicePassword()
	if err != nil {
		return err
	}

	args := []string{"attach", serviceURL, "--dir", agent.Workspace}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	env := attachEnv(os.Environ(), password)
	if err := runAttachCommand(ctx, args, env); err != nil {
		return fmt.Errorf("opencode attach: %w", err)
	}
	return nil
}

func attachEnv(base []string, password string) []string {
	out := make([]string, 0, len(base)+2)
	for _, entry := range base {
		if strings.HasPrefix(entry, "OPENCODE_SERVER_USERNAME=") ||
			strings.HasPrefix(entry, "OPENCODE_SERVER_PASSWORD=") {
			continue
		}
		out = append(out, entry)
	}
	out = append(out,
		"OPENCODE_SERVER_USERNAME=opencode",
		"OPENCODE_SERVER_PASSWORD="+password,
	)
	return out
}
