package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/traceexport"
	"github.com/spf13/cobra"
)

var (
	memoryExportAgent     string
	memoryExportWorkspace string
	memoryExportOut       string
	memoryExportSession   string
	memoryExportMaxPages  int
	memoryExportNoOpen    bool
	memoryExportForce     bool
	memoryExportJSON      bool
)

type memoryExportOptions struct {
	AgentName  string
	Workspace  string
	OutPath    string
	SessionID  string
	MaxPages   int
	NoOpenCode bool
	Force      bool
	JSON       bool
}

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Memory operations",
}

var memoryExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a complete memory trace as a zip",
	Long: `Export one agent's memory trace into a single zip.

The bundle contains one Markdown timeline per OpenCode session (messages,
reasoning, tool calls, and communication routing events), the persona and
skills files, orchestrator logs/cron/session mappings, communication state,
and memory-learn audit rows. OpenCode-native session JSON is included when the
service supports the experimental export endpoint.

The Markdown is self-contained: another agent can read the unzipped folder as
external memory without Acolyte.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMemoryExport(cmd.Context(), cmd.OutOrStdout(), memoryExportOptions{
			AgentName:  memoryExportAgent,
			Workspace:  memoryExportWorkspace,
			OutPath:    memoryExportOut,
			SessionID:  memoryExportSession,
			MaxPages:   memoryExportMaxPages,
			NoOpenCode: memoryExportNoOpen,
			Force:      memoryExportForce,
			JSON:       memoryExportJSON,
		})
	},
}

func init() {
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryExportCmd)

	memoryExportCmd.Flags().StringVar(&memoryExportAgent, "agent-name", "", "target agent name")
	memoryExportCmd.Flags().StringVar(&memoryExportWorkspace, "workspace-folder", "", "target agent workspace")
	memoryExportCmd.Flags().StringVarP(&memoryExportOut, "out", "o", "", "path to write the memory trace zip (required)")
	memoryExportCmd.Flags().StringVar(&memoryExportSession, "session-id", "", "export only one OpenCode session id")
	memoryExportCmd.Flags().IntVar(&memoryExportMaxPages, "max-pages", 1000, "maximum OpenCode message pages per session")
	memoryExportCmd.Flags().BoolVar(&memoryExportNoOpen, "no-opencode", false, "skip OpenCode session transcripts (store/persona only)")
	memoryExportCmd.Flags().BoolVar(&memoryExportForce, "force", false, "overwrite an existing zip")
	memoryExportCmd.Flags().BoolVar(&memoryExportJSON, "json", false, "print one JSON summary object")
	_ = memoryExportCmd.MarkFlagRequired("out")
}

func runMemoryExport(ctx context.Context, w io.Writer, opts memoryExportOptions) error {
	opts.OutPath = strings.TrimSpace(opts.OutPath)
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	if opts.OutPath == "" {
		return fmt.Errorf("--out is required")
	}
	if opts.MaxPages <= 0 {
		return fmt.Errorf("--max-pages must be greater than zero")
	}

	agent, err := resolveTargetAgent(opts.AgentName, opts.Workspace)
	if err != nil {
		return err
	}
	st, err := store.Open(agent.Workspace)
	if err != nil {
		// Fall back to a read-only open so an export still works on a
		// read-only filesystem. store.Open is preferred because it applies
		// migrations, which guarantees the newer tables exist.
		st, err = store.OpenReadOnly(agent.Workspace)
		if err != nil {
			return fmt.Errorf("open agent store: %w", err)
		}
	}
	defer st.Close()

	var client traceexport.OpenCode
	if !opts.NoOpenCode {
		ensure, err := openCodeEnsure(ctx)
		if err != nil {
			return fmt.Errorf("open OpenCode service: %w", err)
		}
		client = ensure
	}

	result, err := traceexport.Export(ctx, traceexport.Request{
		Agent:          agent,
		Store:          st,
		OpenCode:       client,
		OutPath:        opts.OutPath,
		SessionID:      opts.SessionID,
		MaxPages:       opts.MaxPages,
		Force:          opts.Force,
		AcolyteVersion: versionValue,
	})
	if err != nil {
		return err
	}

	if opts.JSON {
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(raw))
		return nil
	}

	fmt.Fprintf(w, "Exported agent %q to %s\n", agent.Name, result.Path)
	fmt.Fprintf(w, "  sessions:      %d\n", result.Sessions)
	fmt.Fprintf(w, "  messages:      %d\n", result.Messages)
	fmt.Fprintf(w, "  log entries:   %d\n", result.Logs)
	fmt.Fprintf(w, "  persona files: %d\n", result.PersonaFiles)
	for _, warning := range result.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	return nil
}
