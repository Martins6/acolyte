package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status [name|workspace]",
	Short: "Show registered agents and their scheduled work",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := ""
		if len(args) == 1 {
			ref = args[0]
		}
		return runStatus(cmd.OutOrStdout(), ref)
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(w io.Writer, ref string) error {
	reg, err := openExistingRegistry()
	if err != nil {
		return err
	}
	if reg == nil {
		if ref != "" {
			return fmt.Errorf("no agent matches %q", ref)
		}
		fmt.Fprintln(w, "No agents registered.")
		return nil
	}
	defer reg.Close()

	if ref != "" {
		agent, err := reg.Resolve(ref)
		if err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				return fmt.Errorf("no agent matches %q", ref)
			}
			return err
		}
		return printAgent(w, agent)
	}

	agents, err := reg.List()
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		fmt.Fprintln(w, "No agents registered.")
		return nil
	}

	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tWORKSPACE\tJOBS\tNEXT JOB\tLAST ACTIVE\tSTATUS\tDESCRIPTION")
	for _, agent := range agents {
		stats, err := readStats(agent.Workspace)
		jobs := "—"
		status := "ok"
		if err != nil {
			status = "unavailable"
		} else {
			jobs = fmt.Sprintf("%d", stats.JobCount)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			agent.Name,
			agent.Workspace,
			jobs,
			formatTime(stats.NextJobAt),
			formatTime(stats.LastActiveAt),
			status,
			agent.Description,
		)
	}
	return table.Flush()
}

func printAgent(w io.Writer, agent registry.Agent) error {
	fmt.Fprintf(w, "Name:        %s\n", agent.Name)
	fmt.Fprintf(w, "Workspace:   %s\n", agent.Workspace)
	fmt.Fprintf(w, "Description: %s\n", agent.Description)
	fmt.Fprintf(w, "Engine:      %s\n", agent.Engine)
	fmt.Fprintf(w, "Enabled:     %t\n", agent.Enabled)
	fmt.Fprintf(w, "Registered:  %s\n", formatTimeValue(agent.RegisteredAt))
	fmt.Fprintf(w, "Last seen:   %s\n", formatTimeValue(agent.LastSeenAt))

	stats, statsErr := readStats(agent.Workspace)
	if statsErr != nil {
		if errors.Is(statsErr, store.ErrNoStore) {
			fmt.Fprintln(w, "Last active: —")
		} else {
			fmt.Fprintf(w, "Last active: unavailable (%v)\n", statsErr)
		}
	} else {
		fmt.Fprintf(w, "Last active: %s\n", formatTime(stats.LastActiveAt))
	}

	st, err := store.OpenReadOnly(agent.Workspace)
	if err != nil {
		if errors.Is(err, store.ErrNoStore) {
			fmt.Fprintln(w, "Jobs:        —")
			return nil
		}
		return err
	}
	defer st.Close()

	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(w, "Jobs:        none")
		return nil
	}

	fmt.Fprintf(w, "Jobs:        %d\n", len(jobs))
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "  TITLE\tKIND\tSPEC\tOWNER\tNEXT RUN")
	for _, job := range jobs {
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\t%s\n",
			job.Title, job.Kind, job.Spec, job.Owner, formatTime(job.NextRunAt))
	}
	return table.Flush()
}

func openExistingRegistry() (*registry.Registry, error) {
	path, err := paths.RegistryDB()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("status: stat registry: %w", err)
	}
	return registry.Open(path)
}

func readStats(workspace string) (orch.Stats, error) {
	st, err := store.OpenReadOnly(workspace)
	if err != nil {
		return orch.Stats{}, err
	}
	defer st.Close()
	return st.Orch().Stats()
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04")
}

func formatTimeValue(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04")
}
