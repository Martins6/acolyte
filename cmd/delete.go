package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/registry"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <name|workspace>",
	Short: "Remove an agent from the registry (folder untouched)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDelete(cmd.OutOrStdout(), args[0])
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}

func runDelete(w io.Writer, ref string) error {
	reg, err := openExistingRegistry()
	if err != nil {
		return err
	}
	if reg == nil {
		return fmt.Errorf("no agent matches %q", ref)
	}
	defer reg.Close()

	agent, err := reg.Delete(ref)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("no agent matches %q", ref)
		}
		return err
	}

	fmt.Fprintf(w, "Deleted agent %q.\n", agent.Name)
	fmt.Fprintf(w, "  workspace: %s\n", agent.Workspace)
	fmt.Fprintln(w, "  folder left untouched.")
	return nil
}
