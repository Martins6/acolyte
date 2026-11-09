package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/config"
	cronsvc "github.com/martins6/acolyte/internal/cron"
	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
	cronlib "github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
)

var (
	cronTitle         string
	cronDatetime      string
	cronPrompt        string
	cronShell         string
	cronRoutine       string
	cronRoutineArgs   string
	cronAgentName     string
	cronWorkspace     string
	cronSessionID     string
	cronTimezone      string
	cronOnSuccess     string
	cronOnFailure     string
	cronViewAgent     string
	cronViewWorkspace string
	cronRmAgent       string
	cronRmWorkspace   string

	cronNow = time.Now
)

type cronCreateOptions struct {
	Title       string
	Datetime    string
	Prompt      string
	Shell       string
	Routine     string
	RoutineArgs string
	AgentName   string
	Workspace   string
	SessionID   string
	Timezone    string
	OnSuccess   string
	OnFailure   string
}

type cronJobMeta struct {
	OnSuccess string `json:"on_success,omitempty"`
	OnFailure string `json:"on_failure,omitempty"`
}

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Manage scheduled jobs for registered agents",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cronTitle) == "" && strings.TrimSpace(cronDatetime) == "" {
			return cmd.Help()
		}
		return runCronCreate(cmd.OutOrStdout(), cronCreateOptions{
			Title:       cronTitle,
			Datetime:    cronDatetime,
			Prompt:      cronPrompt,
			Shell:       cronShell,
			Routine:     cronRoutine,
			RoutineArgs: cronRoutineArgs,
			AgentName:   cronAgentName,
			Workspace:   cronWorkspace,
			SessionID:   cronSessionID,
			Timezone:    cronTimezone,
			OnSuccess:   cronOnSuccess,
			OnFailure:   cronOnFailure,
		})
	},
}

var cronListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cron jobs across all registered agents",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronList(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var cronViewCmd = &cobra.Command{
	Use:   "view <title>",
	Short: "Show one cron job as JSON",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronView(cmd.OutOrStdout(), args[0], cronViewAgent, cronViewWorkspace)
	},
}

var cronRmCmd = &cobra.Command{
	Use:   "rm <title>",
	Short: "Remove a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronRemove(cmd.OutOrStdout(), args[0], cronRmAgent, cronRmWorkspace)
	},
}

var cronRoutineListCmd = &cobra.Command{
	Use:   "routine-list",
	Short: "List registered routines and their arguments as JSON",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronRoutineList(cmd.OutOrStdout())
	},
}

var cronTickCmd = &cobra.Command{
	Use:   "tick",
	Short: "Run one due-job pass (for testing and debugging)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronTick(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(cronCmd)
	cronCmd.AddCommand(cronListCmd, cronViewCmd, cronRmCmd, cronRoutineListCmd, cronTickCmd)

	cronCmd.Flags().StringVar(&cronTitle, "title", "", "unique job title")
	cronCmd.Flags().StringVar(&cronDatetime, "datetime", "", "cron expression (@every 1h, 0 9 * * *)")
	cronCmd.Flags().StringVar(&cronPrompt, "prompt", "", "prompt text for a prompt job")
	cronCmd.Flags().StringVar(&cronShell, "shell", "", "shell script path for a shell job")
	cronCmd.Flags().StringVar(&cronRoutine, "routine", "", "registered routine name")
	cronCmd.Flags().StringVar(&cronRoutineArgs, "routine-args", "", "routine arguments as JSON")
	cronCmd.Flags().StringVar(&cronAgentName, "agent-name", "", "target agent name")
	cronCmd.Flags().StringVar(&cronWorkspace, "workspace-folder", "", "target agent workspace")
	cronCmd.Flags().StringVar(&cronSessionID, "session-id", "", "optional OpenCode session id")
	cronCmd.Flags().StringVar(&cronTimezone, "timezone", "", "IANA timezone (default: agent schedule.timezone)")
	cronCmd.Flags().StringVar(&cronOnSuccess, "on-success", "", "inbox | notify-user | log")
	cronCmd.Flags().StringVar(&cronOnFailure, "on-failure", "", "inbox | notify-user | log")

	cronViewCmd.Flags().StringVar(&cronViewAgent, "agent-name", "", "target agent name")
	cronViewCmd.Flags().StringVar(&cronViewWorkspace, "workspace-folder", "", "target agent workspace")
	cronRmCmd.Flags().StringVar(&cronRmAgent, "agent-name", "", "target agent name")
	cronRmCmd.Flags().StringVar(&cronRmWorkspace, "workspace-folder", "", "target agent workspace")
}

func runCronCreate(w io.Writer, opts cronCreateOptions) error {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	datetime := strings.TrimSpace(opts.Datetime)
	if datetime == "" {
		return fmt.Errorf("--datetime is required")
	}

	actions := 0
	for _, value := range []string{opts.Prompt, opts.Shell, opts.Routine} {
		if strings.TrimSpace(value) != "" {
			actions++
		}
	}
	if actions != 1 {
		return fmt.Errorf("use exactly one of --prompt, --shell, or --routine")
	}

	agent, err := resolveTargetAgent(opts.AgentName, opts.Workspace)
	if err != nil {
		return err
	}

	st, err := store.Open(agent.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()

	timezone := strings.TrimSpace(opts.Timezone)
	if timezone == "" {
		if setting, ok, err := st.Orch().GetSetting("schedule.timezone"); err != nil {
			return err
		} else if ok {
			timezone = setting
		}
	}
	location := time.Local
	if timezone != "" {
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return fmt.Errorf("invalid timezone %q: %w", timezone, err)
		}
	}
	schedule, err := cronlib.ParseStandard(datetime)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", datetime, err)
	}
	next := schedule.Next(cronNow().In(location)).UTC()

	job := orch.CronJob{
		Title:       title,
		Owner:       "user",
		Spec:        datetime,
		AgentTarget: agent.Name,
		SessionID:   strings.TrimSpace(opts.SessionID),
		Timezone:    timezone,
		Enabled:     true,
		NextRunAt:   &next,
	}
	jobMeta := cronJobMeta{OnSuccess: strings.TrimSpace(opts.OnSuccess), OnFailure: strings.TrimSpace(opts.OnFailure)}

	switch {
	case strings.TrimSpace(opts.Prompt) != "":
		job.Kind = "prompt"
		job.Prompt = strings.TrimSpace(opts.Prompt)
		if jobMeta.OnSuccess == "" {
			jobMeta.OnSuccess = "notify-user"
		}
		if jobMeta.OnFailure == "" {
			jobMeta.OnFailure = "notify-user"
		}
	case strings.TrimSpace(opts.Shell) != "":
		job.Kind = "shell"
		script := strings.TrimSpace(opts.Shell)
		if !filepath.IsAbs(script) {
			script = filepath.Join(agent.Workspace, script)
		}
		script, err = filepath.Abs(script)
		if err != nil {
			return fmt.Errorf("resolve shell path: %w", err)
		}
		info, err := os.Stat(script)
		if err != nil {
			return fmt.Errorf("shell script %s: %w", script, err)
		}
		if info.IsDir() {
			return fmt.Errorf("shell path %s is a directory", script)
		}
		job.ShellPath = script
		if jobMeta.OnSuccess == "" {
			jobMeta.OnSuccess = "inbox"
		}
		if jobMeta.OnFailure == "" {
			jobMeta.OnFailure = "notify-user"
		}
	case strings.TrimSpace(opts.Routine) != "":
		job.Kind = "routine"
		name := strings.TrimSpace(opts.Routine)
		routineValue, ok := routine.Default().Get(name)
		if !ok {
			return fmt.Errorf("routine %q is not registered; see 'acolyte cron routine-list'", name)
		}
		args := map[string]any{}
		if strings.TrimSpace(opts.RoutineArgs) != "" {
			if err := json.Unmarshal([]byte(opts.RoutineArgs), &args); err != nil {
				return fmt.Errorf("parse --routine-args: %w", err)
			}
		}
		normalized, err := routine.ValidateArgs(routineValue.Args(), args)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		job.Routine = name
		job.RoutineArgs = string(raw)
		if jobMeta.OnSuccess == "" {
			jobMeta.OnSuccess = "notify-user"
		}
		if jobMeta.OnFailure == "" {
			jobMeta.OnFailure = "notify-user"
		}
	}

	if err := validateRouting(jobMeta); err != nil {
		return err
	}
	rawMeta, err := json.Marshal(jobMeta)
	if err != nil {
		return err
	}
	job.Meta = string(rawMeta)

	if err := st.Orch().AddCronJob(job); err != nil {
		if errors.Is(err, orch.ErrJobExists) {
			return fmt.Errorf("cron job %q already exists for agent %q", title, agent.Name)
		}
		return err
	}

	fmt.Fprintf(w, "Created cron job %q for agent %q.\n", title, agent.Name)
	fmt.Fprintf(w, "  when: %s (next %s)\n", datetime, next.Local().Format(time.RFC3339))
	fmt.Fprintf(w, "  kind: %s\n", job.Kind)
	return nil
}

func validateRouting(meta cronJobMeta) error {
	for _, value := range []string{meta.OnSuccess, meta.OnFailure} {
		switch value {
		case "", "inbox", "notify-user", "log":
		default:
			return fmt.Errorf("invalid routing action %q (use inbox, notify-user, or log)", value)
		}
	}
	return nil
}

func runCronList(w, errW io.Writer) error {
	reg, err := registry.OpenDefault()
	if err != nil {
		return err
	}
	defer reg.Close()

	agents, err := reg.List()
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "| TITLE | DATETIME | AGENT | OWNER | KIND | PROMPT | SHELL | SESSION |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- |")
	rows := 0
	for _, agent := range agents {
		st, err := store.OpenReadOnly(agent.Workspace)
		if err != nil {
			fmt.Fprintf(errW, "warning: %s: %v\n", agent.Name, err)
			continue
		}
		jobs, err := st.Orch().ListCronJobs()
		st.Close()
		if err != nil {
			fmt.Fprintf(errW, "warning: %s: %v\n", agent.Name, err)
			continue
		}
		for _, job := range jobs {
			promptCell := ""
			switch job.Kind {
			case "routine":
				promptCell = "routine:" + job.Routine
			case "prompt":
				promptCell = "prompt:" + job.Prompt
			}
			session := job.SessionID
			if session == "" {
				session = "—"
			}
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				escapeCell(job.Title), escapeCell(job.Spec), escapeCell(agent.Name), escapeCell(job.Owner),
				escapeCell(job.Kind), escapeCell(promptCell), escapeCell(job.ShellPath), escapeCell(session),
			)
			rows++
		}
	}
	if rows == 0 {
		fmt.Fprintln(w, "\nNo cron jobs.")
	}
	return nil
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ReplaceAll(value, "\r", " ")
}

type cronMatch struct {
	Agent registry.Agent
	Job   orch.CronJob
}

func findCronJob(title, agentName, workspace string) (cronMatch, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return cronMatch{}, fmt.Errorf("title is required")
	}
	if strings.TrimSpace(agentName) != "" || strings.TrimSpace(workspace) != "" {
		agent, err := resolveTargetAgent(agentName, workspace)
		if err != nil {
			return cronMatch{}, err
		}
		st, err := store.OpenReadOnly(agent.Workspace)
		if err != nil {
			return cronMatch{}, err
		}
		defer st.Close()
		job, err := st.Orch().GetCronJobByTitle(title)
		if errors.Is(err, orch.ErrJobNotFound) {
			return cronMatch{}, fmt.Errorf("cron job %q not found for agent %q", title, agent.Name)
		}
		if err != nil {
			return cronMatch{}, err
		}
		return cronMatch{Agent: agent, Job: job}, nil
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		return cronMatch{}, err
	}
	defer reg.Close()
	agents, err := reg.List()
	if err != nil {
		return cronMatch{}, err
	}
	var matches []cronMatch
	for _, agent := range agents {
		st, err := store.OpenReadOnly(agent.Workspace)
		if err != nil {
			continue
		}
		job, err := st.Orch().GetCronJobByTitle(title)
		st.Close()
		if err == nil {
			matches = append(matches, cronMatch{Agent: agent, Job: job})
		}
	}
	switch len(matches) {
	case 0:
		return cronMatch{}, fmt.Errorf("cron job %q not found", title)
	case 1:
		return matches[0], nil
	default:
		var names []string
		for _, match := range matches {
			names = append(names, match.Agent.Name)
		}
		return cronMatch{}, fmt.Errorf("cron job %q is ambiguous; use --agent-name (matches: %s)", title, strings.Join(names, ", "))
	}
}

func runCronView(w io.Writer, title, agentName, workspace string) error {
	match, err := findCronJob(title, agentName, workspace)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(match.Job, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(raw))
	return nil
}

func runCronRemove(w io.Writer, title, agentName, workspace string) error {
	match, err := findCronJob(title, agentName, workspace)
	if err != nil {
		return err
	}
	st, err := store.Open(match.Agent.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Orch().DeleteCronJob(title); err != nil {
		return err
	}
	fmt.Fprintf(w, "Deleted cron job %q for agent %q.\n", title, match.Agent.Name)
	return nil
}

func runCronRoutineList(w io.Writer) error {
	raw, err := json.MarshalIndent(routine.Default().Catalog(), "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(raw))
	return nil
}

func runCronTick(ctx context.Context, w io.Writer) error {
	client, err := opencode.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("opencode service unavailable: %w", err)
	}
	if _, err := config.Load(""); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := database.Init(legacyWorkspace()); err != nil {
		return fmt.Errorf("initialize legacy notifier database: %w", err)
	}
	reg, err := registry.OpenDefault()
	if err != nil {
		return err
	}
	defer reg.Close()

	service, err := cronsvc.Start(ctx, cronsvc.Options{
		Registry:        reg,
		Routines:        routine.Default(),
		Sink:            cronsvc.LegacySink{UserIDFunc: config.GetAllowedUserChatID, Turn: client},
		OpenCode:        client,
		Interval:        time.Hour,
		RefreshInterval: time.Hour,
	})
	if err != nil {
		return err
	}
	defer service.Stop()
	service.Tick(ctx)
	service.Wait()
	fmt.Fprintln(w, "Cron tick complete.")
	return nil
}
