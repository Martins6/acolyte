package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

// SupportedSetKeys lists every key `acolyte config <agent> set` accepts, in
// help order.
func SupportedSetKeys() []string {
	return []string{
		"description",
		"engine.model",
		"engine.agent",
		"communication.channels",
		"communication.telegram.allowed_users",
		"schedule.timezone",
		"schedule.heartbeat_minutes",
		"memory-learn",
		"memory-load",
		"memory-access",
		"memory-export",
	}
}

// LoadConfig assembles the effective runtime config of a registered agent:
// identity from the registry, engine from opencode.jsonc, and memory,
// communication, and schedule from the per-agent store (settings + the
// dream/heartbeat jobs, which are the source of truth when present).
func LoadConfig(st *store.Store, a registry.Agent) (Config, error) {
	model, agentID, _, err := readEngine(a.Workspace)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Description: strings.TrimSpace(a.Description),
		Engine:      EngineConfig{Model: strings.TrimSpace(model), Agent: strings.TrimSpace(agentID)},
	}

	orchStore := st.Orch()
	for key, dst := range map[string]**MemoryConfig{
		"memory.load":   &cfg.MemoryLoad,
		"memory.access": &cfg.MemoryAccess,
		"memory.learn":  &cfg.MemoryLearn,
		"memory.export": &cfg.MemoryExport,
	} {
		memory, err := readMemorySetting(orchStore, key)
		if err != nil {
			return Config{}, err
		}
		*dst = memory
	}

	commStore := st.CommTG()
	channels, err := readSettingList(commStore.GetSetting, "channels", nil)
	if err != nil {
		return Config{}, err
	}
	cfg.Communication.Channels = channels
	allowedUsers, err := readSettingList(commStore.GetSetting, "telegram.allowed_users", []string{})
	if err != nil {
		return Config{}, err
	}
	cfg.Communication.Telegram.AllowedUsers = allowedUsers

	dream, dreamExists, err := existingJob(orchStore, "dream")
	if err != nil {
		return Config{}, err
	}
	if dreamExists {
		cfg.MemoryLearn = &MemoryConfig{
			Mode: "simple-dreaming",
			Args: map[string]any{"days": dreamDays(dream)},
		}
	}

	heartbeat, heartbeatExists, err := existingJob(orchStore, "heartbeat")
	if err != nil {
		return Config{}, err
	}

	timezone := ""
	if raw, ok, err := orchStore.GetSetting("schedule.timezone"); err != nil {
		return Config{}, err
	} else if ok {
		timezone = strings.TrimSpace(raw)
	}
	if timezone == "" && heartbeatExists {
		timezone = strings.TrimSpace(heartbeat.Timezone)
	}
	if timezone == "" && dreamExists {
		timezone = strings.TrimSpace(dream.Timezone)
	}
	cfg.Schedule.Timezone = timezone
	if heartbeatExists {
		cfg.Schedule.HeartbeatMinutes = heartbeatMinutes(heartbeat)
	}

	if cfg.Communication.Telegram.AllowedUsers == nil {
		cfg.Communication.Telegram.AllowedUsers = []string{}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SetOptions is one `acolyte config <agent> set <key> <value>` invocation.
type SetOptions struct {
	Key    string
	Value  string
	DryRun bool
}

// SettingChange is one pending store-setting write.
type SettingChange struct {
	Module string `json:"module"` // "orch" or "comm_tg"
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Delete bool   `json:"delete"`
}

// JobChange is one pending cron job write.
type JobChange struct {
	Action string       `json:"action"` // "upsert" or "delete"
	Title  string       `json:"title"`
	Job    orch.CronJob `json:"job,omitempty"`
}

// SetResult reports the effective before/after value of the key plus every
// pending mutation. With DryRun set nothing is written.
type SetResult struct {
	Key          string          `json:"key"`
	Before       any             `json:"before"`
	After        any             `json:"after"`
	Changed      bool            `json:"changed"`
	DryRun       bool            `json:"dry_run"`
	OpencodeDiff string          `json:"opencode_diff,omitempty"`
	Settings     []SettingChange `json:"settings,omitempty"`
	Jobs         []JobChange     `json:"jobs,omitempty"`
	Description  *string         `json:"description,omitempty"`

	opencodeBefore string
	opencodeAfter  string
}

// SetConfig validates and applies one runtime config key. Every change is
// validated before anything is written; DryRun stops after the plan so the
// caller can print a diff and before/after values.
func SetConfig(st *store.Store, a registry.Agent, opts SetOptions) (*SetResult, error) {
	key := strings.TrimSpace(opts.Key)
	if key == "" {
		return nil, fmt.Errorf("config: key is required (supported: %s)", strings.Join(SupportedSetKeys(), ", "))
	}
	current, err := LoadConfig(st, a)
	if err != nil {
		return nil, err
	}
	res, err := planSet(st, a, key, opts.Value, current)
	if err != nil {
		return nil, err
	}
	res.DryRun = opts.DryRun
	if opts.DryRun {
		return res, nil
	}
	if err := res.apply(st, a); err != nil {
		return nil, err
	}
	return res, nil
}

func planSet(st *store.Store, a registry.Agent, key, value string, current Config) (*SetResult, error) {
	value = strings.TrimSpace(value)
	proposed := current
	res := &SetResult{Key: key}

	switch key {
	case "description":
		after := strings.TrimSpace(value)
		res.Before, res.After = current.Description, after
		if after != current.Description {
			res.Description = &after
		}

	case "engine.model":
		model := strings.TrimSpace(value)
		if model == "" {
			return nil, fmt.Errorf("config: engine.model must not be empty")
		}
		if !strings.Contains(model, "/") {
			return nil, fmt.Errorf("config: engine.model must look like provider/model (got %q)", model)
		}
		res.Before, res.After = current.Engine.Model, model
		if model != current.Engine.Model {
			if err := planOpencodeString(res, a.Workspace, "model", model); err != nil {
				return nil, err
			}
		}

	case "engine.agent":
		agentID := strings.TrimSpace(value)
		if err := validateEngineAgent(a.Workspace, agentID); err != nil {
			return nil, err
		}
		res.Before, res.After = current.Engine.Agent, agentID
		if agentID != current.Engine.Agent {
			if err := planOpencodeString(res, a.Workspace, "default_agent", agentID); err != nil {
				return nil, err
			}
		}
		proposed.Engine.Agent = agentID

	case "communication.channels":
		channels, err := parseStringArray(value)
		if err != nil {
			return nil, fmt.Errorf("config: communication.channels: %w", err)
		}
		res.Before, res.After = current.Communication.Channels, channels
		proposed.Communication.Channels = channels
		if err := planCommSetting(res, st.CommTG().GetSetting, "channels", mustJSON(channels)); err != nil {
			return nil, err
		}

	case "communication.telegram.allowed_users":
		users, err := parseUserList(value)
		if err != nil {
			return nil, fmt.Errorf("config: communication.telegram.allowed_users: %w", err)
		}
		res.Before, res.After = current.Communication.Telegram.AllowedUsers, users
		proposed.Communication.Telegram.AllowedUsers = users
		if err := planCommSetting(res, st.CommTG().GetSetting, "telegram.allowed_users", mustJSON(users)); err != nil {
			return nil, err
		}

	case "schedule.timezone":
		timezone := strings.TrimSpace(value)
		if timezone != "" {
			if _, err := time.LoadLocation(timezone); err != nil {
				return nil, fmt.Errorf("config: invalid schedule.timezone %q: %w", timezone, err)
			}
		}
		res.Before, res.After = current.Schedule.Timezone, timezone
		proposed.Schedule.Timezone = timezone
		orchStore := st.Orch()
		stored, ok, err := orchStore.GetSetting("schedule.timezone")
		if err != nil {
			return nil, err
		}
		if timezone == "" {
			if ok {
				res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: "schedule.timezone", Delete: true})
			}
		} else if !ok || strings.TrimSpace(stored) != timezone {
			res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: "schedule.timezone", Value: timezone})
		}
		// A timezone change re-times the existing routine jobs and their
		// next run, matching the parser/nextRun pattern used by `new`.
		for _, title := range []string{"dream", "heartbeat"} {
			job, exists, err := existingJob(orchStore, title)
			if err != nil {
				return nil, err
			}
			if !exists || job.Timezone == timezone {
				continue
			}
			next, err := nextRun(job.Spec, timezone)
			if err != nil {
				return nil, err
			}
			job.Timezone = timezone
			job.NextRunAt = next
			res.Jobs = append(res.Jobs, JobChange{Action: "upsert", Title: title, Job: job})
		}

	case "schedule.heartbeat_minutes":
		minutes, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("config: schedule.heartbeat_minutes must be an integer (got %q)", value)
		}
		if minutes < 0 {
			return nil, fmt.Errorf("config: schedule.heartbeat_minutes must be >= 0")
		}
		res.Before, res.After = current.Schedule.HeartbeatMinutes, minutes
		proposed.Schedule.HeartbeatMinutes = minutes
		if err := planHeartbeat(res, st.Orch(), a.Name, minutes, proposed.Schedule.Timezone); err != nil {
			return nil, err
		}

	case "memory-learn":
		desired, err := parseMemoryValue(value)
		if err != nil {
			return nil, fmt.Errorf("config: memory-learn: %w", err)
		}
		if err := validateMemoryLearn(desired); err != nil {
			return nil, err
		}
		res.Before, res.After = current.MemoryLearn, desired
		proposed.MemoryLearn = desired
		if err := planDream(res, st.Orch(), a.Name, desired, proposed.Schedule.Timezone); err != nil {
			return nil, err
		}

	case "memory-load", "memory-access", "memory-export":
		validate, err := memoryValidator(key)
		if err != nil {
			return nil, err
		}
		desired, err := parseMemoryValue(value)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", key, err)
		}
		if err := validate(desired); err != nil {
			return nil, err
		}
		switch key {
		case "memory-load":
			res.Before, res.After, proposed.MemoryLoad = current.MemoryLoad, desired, desired
		case "memory-access":
			res.Before, res.After, proposed.MemoryAccess = current.MemoryAccess, desired, desired
		case "memory-export":
			res.Before, res.After, proposed.MemoryExport = current.MemoryExport, desired, desired
		}
		settingKey := "memory." + strings.TrimPrefix(key, "memory-")
		stored, ok, err := st.Orch().GetSetting(settingKey)
		if err != nil {
			return nil, err
		}
		if desired == nil {
			if ok {
				res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: settingKey, Delete: true})
			}
		} else if !ok || stored != mustJSON(desired) {
			res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: settingKey, Value: mustJSON(desired)})
		}

	default:
		return nil, fmt.Errorf("config: unsupported key %q (supported: %s)",
			key, strings.Join(SupportedSetKeys(), ", "))
	}

	// Validate the whole proposed config last so cross-key rules (timezone
	// required while heartbeat/dream run, v0 modes) gate every plan.
	if err := proposed.Validate(); err != nil {
		return nil, err
	}
	res.Changed = res.Description != nil ||
		len(res.Settings) > 0 ||
		len(res.Jobs) > 0 ||
		res.opencodeAfter != ""
	return res, nil
}

// planOpencodeString stages a targeted opencode.jsonc edit plus its diff.
func planOpencodeString(res *SetResult, workspace, key, value string) error {
	raw, err := readOpencodeConfig(workspace)
	if err != nil {
		return err
	}
	var doc opencodeEngineDoc
	if err := parseJSONC(raw, &doc); err != nil {
		return fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
	}
	updated, err := setOpencodeString(raw, key, value)
	if err != nil {
		return err
	}
	if updated == raw {
		return nil
	}
	if err := validateOpencodeDocument(workspace, updated); err != nil {
		return err
	}
	res.opencodeBefore, res.opencodeAfter = raw, updated
	res.OpencodeDiff = unifiedDiff(opencodeConfigName, opencodeConfigName, raw, updated)
	return nil
}

func planCommSetting(res *SetResult, get func(string) (string, bool, error), key, desired string) error {
	stored, ok, err := get(key)
	if err != nil {
		return err
	}
	if !ok || stored != desired {
		res.Settings = append(res.Settings, SettingChange{Module: "comm_tg", Key: key, Value: desired})
	}
	return nil
}

func planHeartbeat(res *SetResult, orchStore *orch.Store, agentName string, minutes int, timezone string) error {
	heartbeat, exists, err := existingJob(orchStore, "heartbeat")
	if err != nil {
		return err
	}
	if minutes == 0 {
		if exists {
			res.Jobs = append(res.Jobs, JobChange{Action: "delete", Title: "heartbeat"})
		}
		return nil
	}
	spec := fmt.Sprintf("@every %dm", minutes)
	next, err := nextRun(spec, timezone)
	if err != nil {
		return err
	}
	desired := orch.CronJob{
		Title:       "heartbeat",
		Owner:       "heartbeat",
		Kind:        "routine",
		Spec:        spec,
		Routine:     "heartbeat",
		RoutineArgs: mustJSON(map[string]int{"interval": minutes}),
		AgentTarget: agentName,
		Timezone:    timezone,
		Enabled:     true,
		NextRunAt:   next,
	}
	if exists && sameCapabilityJob(heartbeat, desired) {
		return nil // keep the existing schedule (and its next_run_at)
	}
	res.Jobs = append(res.Jobs, JobChange{Action: "upsert", Title: "heartbeat", Job: desired})
	return nil
}

func planDream(res *SetResult, orchStore *orch.Store, agentName string, desired *MemoryConfig, timezone string) error {
	dream, exists, err := existingJob(orchStore, "dream")
	if err != nil {
		return err
	}
	storedSetting, settingExists, err := orchStore.GetSetting("memory.learn")
	if err != nil {
		return err
	}

	if desired == nil {
		if exists {
			res.Jobs = append(res.Jobs, JobChange{Action: "delete", Title: "dream"})
		}
		if settingExists {
			res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: "memory.learn", Delete: true})
		}
		return nil
	}

	days, err := daysArg(desired)
	if err != nil {
		return err
	}
	spec := fmt.Sprintf("@every %dh", days*24)
	next, err := nextRun(spec, timezone)
	if err != nil {
		return err
	}
	job := orch.CronJob{
		Title:       "dream",
		Owner:       "memory-learn",
		Kind:        "routine",
		Spec:        spec,
		Routine:     "dream",
		RoutineArgs: mustJSON(map[string]int{"days": days}),
		AgentTarget: agentName,
		Timezone:    timezone,
		Enabled:     true,
		NextRunAt:   next,
	}
	if !exists || !sameCapabilityJob(dream, job) {
		res.Jobs = append(res.Jobs, JobChange{Action: "upsert", Title: "dream", Job: job})
	}
	if desiredSetting := mustJSON(desired); !settingExists || storedSetting != desiredSetting {
		res.Settings = append(res.Settings, SettingChange{Module: "orch", Key: "memory.learn", Value: desiredSetting})
	}
	return nil
}

// sameCapabilityJob reports whether an existing job already runs the desired
// capability. Matching jobs keep their next_run_at so re-setting a key never
// resets a schedule.
func sameCapabilityJob(existing, desired orch.CronJob) bool {
	return existing.Owner == desired.Owner &&
		existing.Kind == desired.Kind &&
		existing.Spec == desired.Spec &&
		existing.Routine == desired.Routine &&
		existing.RoutineArgs == desired.RoutineArgs &&
		existing.AgentTarget == desired.AgentTarget &&
		existing.Timezone == desired.Timezone &&
		existing.Enabled == desired.Enabled
}

func (r *SetResult) apply(st *store.Store, a registry.Agent) error {
	if r.opencodeAfter != "" && r.opencodeAfter != r.opencodeBefore {
		if err := writeOpencodeConfig(a.Workspace, r.opencodeAfter); err != nil {
			return err
		}
	}
	for _, change := range r.Settings {
		if err := applySetting(st, change); err != nil {
			return err
		}
	}
	orchStore := st.Orch()
	for _, change := range r.Jobs {
		switch change.Action {
		case "upsert":
			if err := orchStore.UpsertCronJob(change.Job); err != nil {
				return err
			}
		case "delete":
			if err := orchStore.DeleteCronJob(change.Title); err != nil && !errors.Is(err, orch.ErrJobNotFound) {
				return err
			}
		default:
			return fmt.Errorf("config: unknown job action %q", change.Action)
		}
	}
	if r.Description != nil {
		reg, err := registry.OpenDefault()
		if err != nil {
			return err
		}
		defer reg.Close()
		updated := a
		updated.Description = *r.Description
		if err := reg.Update(updated); err != nil {
			return err
		}
	}
	return nil
}

func applySetting(st *store.Store, change SettingChange) error {
	switch change.Module {
	case "orch":
		if change.Delete {
			return st.Orch().DeleteSetting(change.Key)
		}
		return st.Orch().SetSetting(change.Key, change.Value)
	case "comm_tg":
		if change.Delete {
			return st.CommTG().DeleteSetting(change.Key)
		}
		return st.CommTG().SetSetting(change.Key, change.Value)
	default:
		return fmt.Errorf("config: unknown setting module %q", change.Module)
	}
}

func memoryValidator(key string) (func(*MemoryConfig) error, error) {
	switch key {
	case "memory-load":
		return validateMemoryLoad, nil
	case "memory-access":
		return validateMemoryAccess, nil
	case "memory-export":
		return validateMemoryExport, nil
	default:
		return nil, fmt.Errorf("config: unsupported key %q", key)
	}
}

// parseMemoryValue decodes a memory set value: a JSON object or null (null
// clears the capability).
func parseMemoryValue(value string) (*MemoryConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("value must be a JSON object or null")
	}
	if value == "null" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "{") {
		return nil, fmt.Errorf("value must be a JSON object or null")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var memory MemoryConfig
	if err := decoder.Decode(&memory); err != nil {
		return nil, fmt.Errorf("parse value: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing data")
		}
		return nil, fmt.Errorf("parse trailing data: %w", err)
	}
	return &memory, nil
}

func parseStringArray(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") {
		return nil, fmt.Errorf("value must be a JSON array of strings")
	}
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil, fmt.Errorf("value must be a JSON array of strings: %w", err)
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

// parseUserList accepts a JSON array or a comma-separated list and
// normalizes it: trimmed, empty entries dropped, duplicates removed.
func parseUserList(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	var entries []string
	switch {
	case strings.HasPrefix(value, "["):
		if err := json.Unmarshal([]byte(value), &entries); err != nil {
			return nil, fmt.Errorf("value must be a JSON array or comma-separated list: %w", err)
		}
	case value == "":
	default:
		entries = strings.Split(value, ",")
	}
	seen := map[string]bool{}
	users := []string{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		users = append(users, entry)
	}
	return users, nil
}

func readMemorySetting(orchStore *orch.Store, key string) (*MemoryConfig, error) {
	raw, ok, err := orchStore.GetSetting(key)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var memory MemoryConfig
	if err := json.Unmarshal([]byte(raw), &memory); err != nil {
		return nil, fmt.Errorf("config: parse %s setting: %w", key, err)
	}
	return &memory, nil
}

func readSettingList(get func(string) (string, bool, error), key string, fallback []string) ([]string, error) {
	raw, ok, err := get(key)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("config: parse %s setting: %w", key, err)
	}
	return values, nil
}

var (
	heartbeatSpecPattern = regexp.MustCompile(`^@every (\d+)m$`)
	dreamSpecPattern     = regexp.MustCompile(`^@every (\d+)h$`)
)

// heartbeatMinutes reads the interval from a heartbeat job's args, falling
// back to its @every spec.
func heartbeatMinutes(job orch.CronJob) int {
	if value, ok := routineIntArg(job, "interval"); ok && value > 0 {
		return value
	}
	if match := heartbeatSpecPattern.FindStringSubmatch(job.Spec); match != nil {
		if value, err := strconv.Atoi(match[1]); err == nil && value > 0 {
			return value
		}
	}
	return 0
}

// dreamDays reads the interval from a dream job's args, falling back to its
// @every spec (days*24 hours).
func dreamDays(job orch.CronJob) int {
	if value, ok := routineIntArg(job, "days"); ok && value > 0 {
		return value
	}
	if match := dreamSpecPattern.FindStringSubmatch(job.Spec); match != nil {
		if hours, err := strconv.Atoi(match[1]); err == nil && hours > 0 && hours%24 == 0 {
			return hours / 24
		}
	}
	return 3
}

func routineIntArg(job orch.CronJob, name string) (int, bool) {
	var args map[string]int
	if err := json.Unmarshal([]byte(job.RoutineArgs), &args); err != nil {
		return 0, false
	}
	value, ok := args[name]
	return value, ok
}
