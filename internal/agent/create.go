package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/lock"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
	"github.com/martins6/acolyte/internal/workspace"
	"github.com/robfig/cron/v3"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CreateOptions is one `acolyte new` invocation.
type CreateOptions struct {
	Name      string
	Workspace string
	Config    Config
}

// JobResult reports one baked cron job.
type JobResult struct {
	Title    string
	Spec     string
	Inserted bool
}

// CreateResult is the outcome of Create.
type CreateResult struct {
	Agent     registry.Agent
	Adopted   bool
	StorePath string
	Jobs      []JobResult
}

// Create scaffolds or adopts an agent workspace, bakes its capabilities, and
// registers it. The registry write happens last so a failed creation leaves a
// valid, re-adoptable folder.
func Create(ctx context.Context, opts CreateOptions) (*CreateResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(opts.Name)
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("agent: invalid name %q (use letters, digits, dot, dash, or underscore; max 64)", opts.Name)
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}

	ws, err := paths.Canonical(opts.Workspace)
	if err != nil {
		return nil, fmt.Errorf("agent: resolve workspace: %w", err)
	}
	home, err := paths.Home()
	if err != nil {
		return nil, err
	}
	home, err = paths.Canonical(home)
	if err != nil {
		return nil, fmt.Errorf("agent: resolve ACOLYTE_HOME: %w", err)
	}
	if ws == home {
		return nil, fmt.Errorf("agent: workspace must not be ACOLYTE_HOME (%s); choose a separate agent folder", home)
	}

	held, err := lock.Acquire(ws)
	if err != nil {
		return nil, err
	}
	defer held.Release()

	reg, err := registry.OpenDefault()
	if err != nil {
		return nil, err
	}
	defer reg.Close()

	existing, adopted, err := lookup(reg, name, ws)
	if err != nil {
		return nil, err
	}

	if err := workspace.ScaffoldAgent(ws, workspace.AgentOptions{
		Name:        name,
		Description: opts.Config.Description,
		Agent:       opts.Config.Engine.Agent,
		Model:       opts.Config.Engine.Model,
	}); err != nil {
		return nil, err
	}

	st, err := store.Open(ws)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	if err := workspace.ValidateAgent(ws); err != nil {
		return nil, err
	}

	jobs, err := applyCapabilities(st, opts.Config, name)
	if err != nil {
		return nil, err
	}

	if err := saveIdentity(reg, existing, adopted, name, ws, opts.Config); err != nil {
		return nil, err
	}

	registered, err := reg.GetByName(name)
	if err != nil {
		return nil, err
	}

	return &CreateResult{
		Agent:     registered,
		Adopted:   adopted,
		StorePath: st.Path(),
		Jobs:      jobs,
	}, nil
}

func lookup(reg *registry.Registry, name, workspace string) (registry.Agent, bool, error) {
	byName, err := reg.GetByName(name)
	if err == nil {
		if byName.Workspace != workspace {
			return registry.Agent{}, false, fmt.Errorf("agent: name %q is already registered at %s", name, byName.Workspace)
		}
		return byName, true, nil
	}
	if !errors.Is(err, registry.ErrNotFound) {
		return registry.Agent{}, false, err
	}

	byWorkspace, err := reg.GetByWorkspace(workspace)
	if err == nil {
		if byWorkspace.Name != name {
			return registry.Agent{}, false, fmt.Errorf("agent: workspace %s is already registered as %q", workspace, byWorkspace.Name)
		}
		return byWorkspace, true, nil
	}
	if !errors.Is(err, registry.ErrNotFound) {
		return registry.Agent{}, false, err
	}
	return registry.Agent{}, false, nil
}

func saveIdentity(reg *registry.Registry, existing registry.Agent, adopted bool, name, workspace string, cfg Config) error {
	if adopted {
		if cfg.Description != "" {
			existing.Description = cfg.Description
		}
		existing.Engine = "opencode"
		return reg.Update(existing)
	}
	return reg.Register(registry.Agent{
		Name:        name,
		Workspace:   workspace,
		Description: cfg.Description,
		Engine:      "opencode",
		Enabled:     true,
	})
}

func applyCapabilities(st *store.Store, cfg Config, name string) ([]JobResult, error) {
	dream, dreamExists, err := existingJob(st.Orch(), "dream")
	if err != nil {
		return nil, err
	}
	heartbeat, heartbeatExists, err := existingJob(st.Orch(), "heartbeat")
	if err != nil {
		return nil, err
	}

	// Derive the shared timezone from preserved jobs, not from the new
	// config, so an existing job and its setting cannot contradict each other.
	timezones := map[string]bool{}
	if setting, ok, err := st.Orch().GetSetting("schedule.timezone"); err != nil {
		return nil, err
	} else if ok && strings.TrimSpace(setting) != "" {
		timezones[setting] = true
	}
	if dreamExists && dream.Timezone != "" {
		timezones[dream.Timezone] = true
	}
	if heartbeatExists && heartbeat.Timezone != "" {
		timezones[heartbeat.Timezone] = true
	}
	if len(timezones) > 1 {
		return nil, fmt.Errorf("agent: existing jobs/settings use conflicting timezones")
	}
	effectiveTimezone := cfg.Schedule.Timezone
	for timezone := range timezones {
		effectiveTimezone = timezone
	}

	// Build and validate every schedule before persisting capability settings,
	// so a bad spec can never leave half-applied state.
	var jobs []orch.CronJob
	if cfg.MemoryLearn != nil {
		days, err := cfg.Days()
		if err != nil {
			return nil, err
		}
		spec := fmt.Sprintf("@every %dh", days*24)
		next, err := nextRun(spec, effectiveTimezone)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, orch.CronJob{
			Title:       "dream",
			Owner:       "memory-learn",
			Kind:        "routine",
			Spec:        spec,
			Routine:     "dream",
			RoutineArgs: mustJSON(map[string]int{"days": days}),
			AgentTarget: name,
			Timezone:    effectiveTimezone,
			Enabled:     true,
			NextRunAt:   next,
		})
	}

	if cfg.Schedule.HeartbeatMinutes > 0 {
		spec := fmt.Sprintf("@every %dm", cfg.Schedule.HeartbeatMinutes)
		next, err := nextRun(spec, effectiveTimezone)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, orch.CronJob{
			Title:       "heartbeat",
			Owner:       "heartbeat",
			Kind:        "routine",
			Spec:        spec,
			Routine:     "heartbeat",
			RoutineArgs: mustJSON(map[string]int{"interval": cfg.Schedule.HeartbeatMinutes}),
			AgentTarget: name,
			Timezone:    effectiveTimezone,
			Enabled:     true,
			NextRunAt:   next,
		})
	}

	if effectiveTimezone != "" {
		if err := setSettingIfMissing(st.Orch(), "schedule.timezone", effectiveTimezone); err != nil {
			return nil, err
		}
	}

	for key, memory := range map[string]*MemoryConfig{
		"memory.load":   cfg.MemoryLoad,
		"memory.access": cfg.MemoryAccess,
		"memory.learn":  cfg.MemoryLearn,
		"memory.export": cfg.MemoryExport,
	} {
		if memory == nil {
			continue
		}
		// The dream job is the source of truth for simple-dreaming; do not
		// seed a setting that contradicts a preserved job.
		if key == "memory.learn" && dreamExists {
			continue
		}
		if err := setSettingIfMissing(st.Orch(), key, mustJSON(memory)); err != nil {
			return nil, err
		}
	}

	if hasTelegram(cfg) && len(cfg.Communication.Telegram.AllowedUsers) > 0 {
		if _, exists, err := st.CommTG().GetSetting("telegram.allowed_users"); err != nil {
			return nil, err
		} else if !exists {
			if err := st.CommTG().SetSetting(
				"telegram.allowed_users",
				mustJSON(cfg.Communication.Telegram.AllowedUsers),
			); err != nil {
				return nil, err
			}
		}
	}

	var results []JobResult
	for _, job := range jobs {
		inserted, err := st.Orch().AddCronJobIfMissing(job)
		if err != nil {
			return nil, err
		}
		results = append(results, JobResult{Title: job.Title, Spec: job.Spec, Inserted: inserted})
	}
	return results, nil
}

func existingJob(store *orch.Store, title string) (orch.CronJob, bool, error) {
	job, err := store.GetCronJobByTitle(title)
	if errors.Is(err, orch.ErrJobNotFound) {
		return orch.CronJob{}, false, nil
	}
	if err != nil {
		return orch.CronJob{}, false, err
	}
	return job, true, nil
}

func setSettingIfMissing(store *orch.Store, key, value string) error {
	if _, exists, err := store.GetSetting(key); err != nil {
		return err
	} else if exists {
		return nil
	}
	return store.SetSetting(key, value)
}

func hasTelegram(cfg Config) bool {
	for _, channel := range cfg.Communication.Channels {
		if channel == "telegram" {
			return true
		}
	}
	return false
}

func nextRun(spec, timezone string) (*time.Time, error) {
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("agent: parse cron spec %q: %w", spec, err)
	}
	location := time.Local
	if timezone != "" {
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return nil, fmt.Errorf("agent: load timezone %q: %w", timezone, err)
		}
	}
	next := schedule.Next(time.Now().In(location)).UTC()
	return &next, nil
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
