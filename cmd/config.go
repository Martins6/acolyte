package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/lock"
	"github.com/martins6/acolyte/internal/store"
	"github.com/spf13/cobra"
)

var (
	configJSON   bool
	configDryRun bool
)

// openEditor runs $EDITOR (fallback vi) on path. It is a seam so tests can
// swap in a fake editor.
var openEditor = func(path string) error {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	command := exec.Command(parts[0], append(parts[1:], path)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// configFlags is the parsed flag state of one `acolyte config` invocation.
type configFlags struct {
	JSON   bool
	DryRun bool
}

var configCmd = &cobra.Command{
	Use:   "config <agent> <get|set|edit> [value]",
	Short: "Inspect or update an agent's runtime capabilities",
	Long: `Inspect or update the runtime capabilities of a registered agent.

<agent> is an agent name or a workspace path.

  acolyte config <agent> get                       print the effective config as JSON
  acolyte config <agent> set <key> <value>         update one durable key
  acolyte config <agent> edit                      open opencode.jsonc in $EDITOR

Supported set keys:
  description                          registry description
  engine.model                         opencode.jsonc "model" (provider/model)
  engine.agent                         opencode.jsonc "default_agent" (build, plan, or a defined agent)
  communication.channels               JSON array (v0: telegram only; [] disables channels)
  communication.telegram.allowed_users JSON array or comma-separated list
  schedule.timezone                    IANA timezone (required while heartbeat/dream run)
  schedule.heartbeat_minutes           minutes; 0 removes the heartbeat job, >0 upserts it
  memory-learn                         {"mode":"simple-dreaming","args":{"days":N}} or null
  memory-load / memory-access / memory-export
                                       JSON object or null (store setting only)

Flags:
  --json      machine-readable JSON output
  --dry-run   set only: print the plan (diff + before/after) without writing`,
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return runConfigCommand(cmd.OutOrStdout(), args, configFlags{JSON: configJSON, DryRun: configDryRun})
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.Flags().BoolVar(&configJSON, "json", false, "machine-readable JSON output")
	configCmd.Flags().BoolVar(&configDryRun, "dry-run", false, "set: print the plan without writing")
}

func runConfigCommand(w io.Writer, args []string, flags configFlags) error {
	ref := strings.TrimSpace(args[0])
	if ref == "" {
		return fmt.Errorf("config: agent name or workspace is required")
	}
	if len(args) < 2 {
		return fmt.Errorf("config: missing subcommand for %q (use get, set, or edit)", ref)
	}
	sub := args[1]
	if flags.DryRun && sub != "set" {
		return fmt.Errorf("--dry-run is only valid with 'config set'")
	}
	switch sub {
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: acolyte config <agent> get [--json]")
		}
		return runConfigGet(w, ref, flags.JSON)
	case "set":
		if len(args) != 4 {
			return fmt.Errorf("usage: acolyte config <agent> set <key> <value> [--dry-run] [--json]")
		}
		return runConfigSet(w, ref, args[2], args[3], flags.DryRun, flags.JSON)
	case "edit":
		if len(args) != 2 {
			return fmt.Errorf("usage: acolyte config <agent> edit [--json]")
		}
		return runConfigEdit(w, ref, flags.JSON)
	default:
		return fmt.Errorf("config: unknown subcommand %q (use get, set, or edit)", sub)
	}
}

func runConfigGet(w io.Writer, ref string, jsonOut bool) error {
	target, err := resolveTargetAgent(ref, "")
	if err != nil {
		return err
	}
	st, err := openAgentStore(target.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()

	cfg, err := agent.LoadConfig(st, target)
	if err != nil {
		return err
	}
	if jsonOut {
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(raw))
		return nil
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(raw))
	return nil
}

func runConfigSet(w io.Writer, ref, key, value string, dryRun, jsonOut bool) error {
	target, err := resolveTargetAgent(ref, "")
	if err != nil {
		return err
	}
	held, err := lock.Acquire(target.Workspace)
	if err != nil {
		return err
	}
	defer held.Release()

	st, err := store.Open(target.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()

	res, err := agent.SetConfig(st, target, agent.SetOptions{Key: key, Value: value, DryRun: dryRun})
	if err != nil {
		return err
	}
	return printConfigSet(w, res, jsonOut)
}

func runConfigEdit(w io.Writer, ref string, jsonOut bool) error {
	target, err := resolveTargetAgent(ref, "")
	if err != nil {
		return err
	}
	held, err := lock.Acquire(target.Workspace)
	if err != nil {
		return err
	}
	defer held.Release()

	changed, err := agent.EditOpencode(target.Workspace, openEditor)
	if err != nil {
		return err
	}
	path := filepath.Join(target.Workspace, "opencode.jsonc")
	if jsonOut {
		raw, err := json.Marshal(map[string]any{
			"agent":     target.Name,
			"workspace": target.Workspace,
			"changed":   changed,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(raw))
		return nil
	}
	if changed {
		fmt.Fprintf(w, "Updated %s.\n", path)
	} else {
		fmt.Fprintf(w, "No changes to %s.\n", path)
	}
	return nil
}

func printConfigSet(w io.Writer, res *agent.SetResult, jsonOut bool) error {
	if jsonOut {
		raw, err := json.Marshal(res)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(raw))
		return nil
	}

	if !res.Changed {
		verb := "No changes:"
		if res.DryRun {
			verb = "Dry run, no changes:"
		}
		fmt.Fprintf(w, "%s %s is already %s.\n", verb, res.Key, formatConfigValue(res.After))
		return nil
	}

	verb := "Set"
	if res.DryRun {
		verb = "Would set"
	}
	fmt.Fprintf(w, "%s %s: %s -> %s\n", verb, res.Key,
		formatConfigValue(res.Before), formatConfigValue(res.After))
	if res.OpencodeDiff != "" {
		fmt.Fprint(w, res.OpencodeDiff)
	}
	for _, change := range res.Settings {
		if change.Delete {
			fmt.Fprintf(w, "  delete setting %s.%s\n", change.Module, change.Key)
		} else {
			fmt.Fprintf(w, "  set setting    %s.%s = %s\n", change.Module, change.Key, change.Value)
		}
	}
	for _, change := range res.Jobs {
		if change.Action == "delete" {
			fmt.Fprintf(w, "  delete job     %s\n", change.Title)
		} else {
			fmt.Fprintf(w, "  upsert job     %s [%s]\n", change.Job.Title, change.Job.Spec)
		}
	}
	if res.Description != nil {
		fmt.Fprintf(w, "  set registry   description = %s\n", strconv.Quote(*res.Description))
	}
	if res.DryRun {
		fmt.Fprintln(w, "Dry run: nothing was written.")
	}
	return nil
}

func formatConfigValue(value any) string {
	if text, ok := value.(string); ok {
		return strconv.Quote(text)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

// openAgentStore prefers store.Open (which applies migrations) and falls
// back to a read-only open so `get` still works on a read-only filesystem.
func openAgentStore(workspace string) (*store.Store, error) {
	st, err := store.Open(workspace)
	if err == nil {
		return st, nil
	}
	readOnly, readOnlyErr := store.OpenReadOnly(workspace)
	if readOnlyErr != nil {
		return nil, fmt.Errorf("open agent store: %w", err)
	}
	return readOnly, nil
}
