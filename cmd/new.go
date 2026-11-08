package cmd

import (
	"fmt"
	"strings"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/spf13/cobra"
)

var (
	newName            string
	newWorkspaceFolder string
	newConfigJSON      string
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Scaffold, validate, and register an agent",
	Long: `Create a new agent workspace or adopt an existing one.

The workspace is an OpenCode project plus an Acolyte overlay. The chosen
capabilities are baked as concrete artifacts (opencode.jsonc, persona files,
cron jobs) and the agent is registered in $ACOLYTE_HOME/registry.sqlite.db.

Example:
  acolyte new --name valet --workspace-folder ~/agents/valet --config '{
    "description": "Personal assistant",
    "engine": { "model": "minimax-coding-plan/MiniMax-M3", "agent": "build" },
    "memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
    "communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
    "schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 60 }
  }'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(newName) == "" {
			return fmt.Errorf("--name is required")
		}
		if strings.TrimSpace(newWorkspaceFolder) == "" {
			return fmt.Errorf("--workspace-folder is required")
		}

		config, err := agent.ParseConfig(newConfigJSON)
		if err != nil {
			return err
		}

		result, err := agent.Create(cmd.Context(), agent.CreateOptions{
			Name:      newName,
			Workspace: newWorkspaceFolder,
			Config:    config,
		})
		if err != nil {
			return err
		}

		state := "registered"
		if result.Adopted {
			state = "adopted"
		}
		fmt.Printf("Agent %q %s.\n", result.Agent.Name, state)
		fmt.Printf("  workspace: %s\n", result.Agent.Workspace)
		fmt.Printf("  engine:    %s (%s)\n", result.Agent.Engine, config.Engine.Agent)
		fmt.Printf("  store:     %s\n", result.StorePath)
		if len(result.Jobs) == 0 {
			fmt.Println("  jobs:      none")
		}
		for _, job := range result.Jobs {
			jobState := "created"
			if !job.Inserted {
				jobState = "existing (capability updates deferred)"
			}
			fmt.Printf("  job:       %s %s [%s]\n", job.Title, job.Spec, jobState)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(newCmd)
	newCmd.Flags().StringVar(&newName, "name", "", "unique agent name (required)")
	newCmd.Flags().StringVar(&newWorkspaceFolder, "workspace-folder", "", "agent workspace folder (required)")
	newCmd.Flags().StringVar(&newConfigJSON, "config", "{}", "capability selector JSON")
	_ = newCmd.MarkFlagRequired("name")
	_ = newCmd.MarkFlagRequired("workspace-folder")
}
