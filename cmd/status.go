package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
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
	fmt.Fprintln(table, "NAME\tWORKSPACE\tJOBS\tNEXT JOB\tLAST ACTIVE\tCHANNELS\tLAST MESSAGE\tSTATUS\tDESCRIPTION")
	for _, agent := range agents {
		stats, err := readStats(agent.Workspace)
		jobs := "—"
		nextJob := "—"
		lastActive := "—"
		channels := "—"
		lastMessage := "—"
		status := "ok"
		if err != nil {
			status = "unavailable"
		} else {
			jobs = fmt.Sprintf("%d", stats.JobCount)
			nextJob = formatTime(stats.NextJobAt)
			lastActive = formatTime(stats.LastActiveAt)
			channels = formatList(stats.Channels, "—")
			if stats.SettingsErr != nil {
				channels = "unavailable"
			}
			if stats.LastMessageErr != nil {
				lastMessage = "unavailable"
			} else {
				lastMessage = formatTime(stats.LastMessageAt)
			}
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			agent.Name,
			agent.Workspace,
			jobs,
			nextJob,
			lastActive,
			channels,
			lastMessage,
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

	st, err := store.OpenReadOnly(agent.Workspace)
	if err != nil {
		if errors.Is(err, store.ErrNoStore) {
			fmt.Fprintln(w, "Last active: —")
			printCommStatus(w, commStatus{})
			fmt.Fprintln(w, "Jobs:        —")
		} else {
			fmt.Fprintf(w, "Last active: unavailable (%v)\n", err)
			printCommStatus(w, commStatus{SettingsErr: err, LastMessageErr: err})
			fmt.Fprintln(w, "Jobs:        unavailable")
		}
		return nil
	}
	defer st.Close()

	stats, statsErr := st.Orch().Stats()
	if statsErr != nil {
		fmt.Fprintf(w, "Last active: unavailable (%v)\n", statsErr)
	} else {
		fmt.Fprintf(w, "Last active: %s\n", formatTime(stats.LastActiveAt))
	}
	printCommStatus(w, readCommStatus(st.CommTG()))
	if statsErr != nil {
		fmt.Fprintln(w, "Jobs:        unavailable")
		return nil
	}

	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		fmt.Fprintln(w, "Jobs:        unavailable")
		return nil
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

// agentStatus is the per-agent view used by the list: orchestrator activity
// plus the communication settings users ask about most often.
type agentStatus struct {
	orch.Stats
	commStatus
}

// commStatus is the communication portion of the per-agent status. A nil
// error with a nil LastMessageAt means "no messages yet"; a non-nil error
// means the tables could not be read.
type commStatus struct {
	Channels       []string
	AllowedUsers   []string
	LastMessageAt  *time.Time
	SettingsErr    error
	LastMessageErr error
}

func readStats(workspace string) (agentStatus, error) {
	st, err := store.OpenReadOnly(workspace)
	if err != nil {
		return agentStatus{}, err
	}
	defer st.Close()
	stats, err := st.Orch().Stats()
	if err != nil {
		return agentStatus{}, err
	}
	return agentStatus{Stats: stats, commStatus: readCommStatus(st.CommTG())}, nil
}

func readCommStatus(comm *commtg.Store) commStatus {
	var status commStatus
	status.Channels, status.AllowedUsers, status.SettingsErr = readCommSettings(comm)
	if last, ok, err := comm.LastMessageAt(); err != nil {
		status.LastMessageErr = err
	} else if ok {
		status.LastMessageAt = &last
	}
	return status
}

func readCommSettings(comm *commtg.Store) (channels, allowed []string, err error) {
	channels, err = readSettingList(comm.GetSetting, "channels", []string{"telegram"})
	if err != nil {
		return nil, nil, err
	}
	allowed, err = readSettingList(comm.GetSetting, "telegram.allowed_users", nil)
	if err != nil {
		return nil, nil, err
	}
	return channels, allowed, nil
}

// readSettingList parses a JSON string-array setting. Absent, blank, or
// malformed values fall back to the defaults so stale settings never make
// status fail. An explicit empty array is preserved: `"channels": []` means
// the agent has no channels, not that the default applies.
func readSettingList(get func(string) (string, bool, error), key string, fallback []string) ([]string, error) {
	raw, ok, err := get(key)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return append([]string(nil), fallback...), nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return append([]string(nil), fallback...), nil
	}
	return values, nil
}

func printCommStatus(w io.Writer, status commStatus) {
	channels := "unavailable"
	allowed := "unavailable"
	if status.SettingsErr == nil {
		channels = formatList(status.Channels, "—")
		allowed = formatList(status.AllowedUsers, "—")
	}
	lastMessage := "unavailable"
	if status.LastMessageErr == nil {
		lastMessage = formatTime(status.LastMessageAt)
	}
	fmt.Fprintf(w, "Channels:    %s\n", channels)
	fmt.Fprintf(w, "Allowed:     %s\n", allowed)
	fmt.Fprintf(w, "Last message: %s\n", lastMessage)
}

func formatList(values []string, empty string) string {
	if len(values) == 0 {
		return empty
	}
	return strings.Join(values, ", ")
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
