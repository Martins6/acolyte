package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/traceimport"
	"github.com/spf13/cobra"
)

var (
	memoryImportAgent     string
	memoryImportWorkspace string
	memoryImportMode      string
	memoryImportDryRun    bool
	memoryImportForce     bool
	memoryImportJSON      bool
)

type memoryImportOptions struct {
	ZipPath    string
	AgentName  string
	Workspace  string
	Mode       string
	DryRun     bool
	Force      bool
	JSON       bool
	OpenImport func(context.Context) (traceimport.OpenCode, error)
	OpenStore  func(string) (*store.Store, error)
}

var memoryImportCmd = &cobra.Command{
	Use:   "import <zip>",
	Short: "Import a memory trace zip into an agent",
	Long: `Import a portable memory trace bundle (written by acolyte memory export)
into an agent workspace.

Modes:
  trace     (default) safely extract the bundle into
            <workspace>/downloads/memory-traces/<trace-id>/ and upsert the
            managed block in .acolyte/MEMORY-TRACES.md, which opencode.jsonc
            auto-loads so the agent sees the index.
  sessions  replay each sessions/<id>.json through the OpenCode session
            import API and map the imported sessions to threads
            memory:<trace-id>:<source-session-id>.
  all       run both.

Extraction rejects absolute paths, ".." traversal, symlink entries, and
entries escaping the trace root; total size is capped at 512 MiB (64 MiB
per file). An existing trace folder requires --force. --dry-run validates
the zip and prints the planned actions without writing or importing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMemoryImport(cmd.Context(), cmd.OutOrStdout(), memoryImportOptions{
			ZipPath:    args[0],
			AgentName:  memoryImportAgent,
			Workspace:  memoryImportWorkspace,
			Mode:       memoryImportMode,
			DryRun:     memoryImportDryRun,
			Force:      memoryImportForce,
			JSON:       memoryImportJSON,
			OpenImport: openCodeImportEnsure,
			OpenStore:  openImportStore,
		})
	},
}

// openImportStore opens the writable per-agent store used for session
// mappings. It is a seam so command tests can inject fakes.
var openImportStore = func(workspace string) (*store.Store, error) {
	return store.Open(workspace)
}

func init() {
	memoryCmd.AddCommand(memoryImportCmd)

	memoryImportCmd.Flags().StringVar(&memoryImportAgent, "agent-name", "", "target agent name")
	memoryImportCmd.Flags().StringVar(&memoryImportWorkspace, "workspace-folder", "", "target agent workspace")
	memoryImportCmd.Flags().StringVar(&memoryImportMode, "mode", "trace", "import mode: trace, sessions, or all")
	memoryImportCmd.Flags().BoolVar(&memoryImportDryRun, "dry-run", false, "validate and print planned actions without writing")
	memoryImportCmd.Flags().BoolVar(&memoryImportForce, "force", false, "replace an existing trace folder and re-import mapped sessions")
	memoryImportCmd.Flags().BoolVar(&memoryImportJSON, "json", false, "print one JSON summary object")
}

func runMemoryImport(ctx context.Context, w io.Writer, opts memoryImportOptions) error {
	opts.ZipPath = strings.TrimSpace(opts.ZipPath)
	if opts.ZipPath == "" {
		return fmt.Errorf("zip path is required")
	}
	mode, err := traceimport.ParseMode(opts.Mode)
	if err != nil {
		return err
	}
	if _, err := os.Stat(opts.ZipPath); err != nil {
		return fmt.Errorf("read trace zip: %w", err)
	}

	agent, err := resolveTargetAgent(opts.AgentName, opts.Workspace)
	if err != nil {
		return err
	}

	req := traceimport.Request{
		Agent:   agent,
		ZipPath: opts.ZipPath,
		Mode:    mode,
		DryRun:  opts.DryRun,
		Force:   opts.Force,
	}

	needsSessions := mode == traceimport.ModeSessions || mode == traceimport.ModeAll
	if needsSessions {
		if opts.DryRun {
			// Dry-run only reads existing thread mappings; a missing store
			// means nothing is mapped yet.
			st, err := openImportStoreRO(agent.Workspace)
			if err != nil && !errors.Is(err, store.ErrNoStore) {
				return fmt.Errorf("open agent store: %w", err)
			}
			if st != nil {
				defer st.Close()
			}
			req.Store = st
		} else {
			open := opts.OpenStore
			if open == nil {
				open = openImportStore
			}
			st, err := open(agent.Workspace)
			if err != nil {
				return fmt.Errorf("open agent store: %w", err)
			}
			defer st.Close()
			req.Store = st

			ensure := opts.OpenImport
			if ensure == nil {
				ensure = openCodeImportEnsure
			}
			client, err := ensure(ctx)
			if err != nil {
				return fmt.Errorf("OpenCode service unavailable (required for --mode %s): %w", mode, err)
			}
			req.OpenCode = client
		}
	}

	result, err := traceimport.Import(ctx, req)
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
	printMemoryImportSummary(w, agent.Name, result)
	return nil
}

// openImportStoreRO opens the store read-only for dry-run mapping checks.
var openImportStoreRO = func(workspace string) (*store.Store, error) {
	return store.OpenReadOnly(workspace)
}

func printMemoryImportSummary(w io.Writer, agentName string, result *traceimport.Result) {
	if result.DryRun {
		fmt.Fprintf(w, "Dry run for agent %q (mode %s, trace %s): no changes written\n",
			agentName, result.Mode, result.TraceID)
		for _, action := range result.Actions {
			fmt.Fprintf(w, "  %s\n", action)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", warning)
		}
		return
	}

	fmt.Fprintf(w, "Imported trace %s into agent %q (mode %s)\n", result.TraceID, agentName, result.Mode)
	if result.SourceAgent != "" {
		fmt.Fprintf(w, "  source:    %s", result.SourceAgent)
		if !result.ExportedAt.IsZero() {
			fmt.Fprintf(w, ", exported %s", result.ExportedAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
		fmt.Fprintln(w)
	}
	if result.TraceDir != "" {
		fmt.Fprintf(w, "  folder:    %s\n", result.TraceDir)
		fmt.Fprintf(w, "  files:     %d\n", result.Files)
	}
	if result.IndexPath != "" {
		status := "unchanged"
		if result.IndexUpdated {
			status = "updated"
		}
		fmt.Fprintf(w, "  index:     %s (%s)\n", status, result.IndexPath)
	}
	if len(result.Sessions) > 0 || len(result.Skipped) > 0 {
		fmt.Fprintf(w, "  sessions:  %d imported, %d skipped\n", len(result.Sessions), len(result.Skipped))
		for _, session := range result.Sessions {
			fmt.Fprintf(w, "    %s -> %s (%s)\n", session.SourceSessionID, session.SessionID, session.Thread)
		}
		for _, skipped := range result.Skipped {
			fmt.Fprintf(w, "    %s skipped (already mapped)\n", skipped)
		}
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
}
