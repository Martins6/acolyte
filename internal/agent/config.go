// Package agent parses the `acolyte new --config` capability selector and
// creates/adopts a registered agent workspace.
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// Config is the full `acolyte new --config` object. Every key is optional.
type Config struct {
	Description   string              `json:"description"`
	Engine        EngineConfig        `json:"engine"`
	MemoryLoad    *MemoryConfig       `json:"memory-load"`
	MemoryAccess  *MemoryConfig       `json:"memory-access"`
	MemoryLearn   *MemoryConfig       `json:"memory-learn"`
	MemoryExport  *MemoryConfig       `json:"memory-export"`
	Communication CommunicationConfig `json:"communication"`
	Schedule      ScheduleConfig      `json:"schedule"`
}

// EngineConfig wires the OpenCode engine.
type EngineConfig struct {
	Model string `json:"model"`
	Agent string `json:"agent"`
}

// MemoryConfig selects one memory mode. A nil pointer means the native
// baseline (no extra module).
type MemoryConfig struct {
	Mode string         `json:"mode"`
	Args map[string]any `json:"args"`
}

// CommunicationConfig enables channels and stores their non-secret settings.
type CommunicationConfig struct {
	Channels []string       `json:"channels"`
	Telegram TelegramConfig `json:"telegram"`
}

// TelegramConfig holds non-secret Telegram settings.
type TelegramConfig struct {
	AllowedUsers []string `json:"allowed_users"`
}

// ScheduleConfig controls the baked cron jobs.
type ScheduleConfig struct {
	Timezone         string `json:"timezone"`
	HeartbeatMinutes int    `json:"heartbeat_minutes"`
}

// ParseConfig decodes and validates a --config JSON object. Unknown keys are
// rejected so typos do not silently bake the wrong capabilities.
func ParseConfig(raw string) (Config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	if !strings.HasPrefix(raw, "{") {
		return Config{}, fmt.Errorf("config: top-level value must be a JSON object")
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("config: parse: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("config: unexpected trailing data")
		}
		return Config{}, fmt.Errorf("config: parse trailing data: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate normalizes defaults and rejects unsupported v0 modes/arguments.
func (c *Config) Validate() error {
	c.Description = strings.TrimSpace(c.Description)
	c.Engine.Model = strings.TrimSpace(c.Engine.Model)
	c.Engine.Agent = strings.TrimSpace(c.Engine.Agent)
	if c.Engine.Agent == "" {
		c.Engine.Agent = "build"
	}

	if err := validateMemoryLoad(c.MemoryLoad); err != nil {
		return err
	}
	if err := validateMemoryAccess(c.MemoryAccess); err != nil {
		return err
	}
	if err := validateMemoryLearn(c.MemoryLearn); err != nil {
		return err
	}
	if err := validateMemoryExport(c.MemoryExport); err != nil {
		return err
	}

	if c.Communication.Channels == nil {
		c.Communication.Channels = []string{"telegram"}
	}
	for _, channel := range c.Communication.Channels {
		if channel != "telegram" {
			return fmt.Errorf("config: communication channel %q is not available in v0 (supported: telegram)", channel)
		}
	}
	for i, user := range c.Communication.Telegram.AllowedUsers {
		c.Communication.Telegram.AllowedUsers[i] = strings.TrimSpace(user)
	}

	if c.Schedule.HeartbeatMinutes < 0 {
		return fmt.Errorf("config: schedule.heartbeat_minutes must be >= 0")
	}
	c.Schedule.Timezone = strings.TrimSpace(c.Schedule.Timezone)
	if c.Schedule.Timezone != "" {
		if _, err := time.LoadLocation(c.Schedule.Timezone); err != nil {
			return fmt.Errorf("config: invalid schedule.timezone %q: %w", c.Schedule.Timezone, err)
		}
	}
	if c.Schedule.Timezone == "" && (c.Schedule.HeartbeatMinutes > 0 || c.MemoryLearn != nil) {
		return fmt.Errorf("config: schedule.timezone is required when scheduling is enabled")
	}
	return nil
}

func validateMemoryLoad(memory *MemoryConfig) error {
	if memory == nil {
		return nil
	}
	if memory.Mode == "" {
		memory.Mode = "default"
	}
	if memory.Mode != "default" {
		return fmt.Errorf("config: memory-load mode %q is not available in v0 (supported: default)", memory.Mode)
	}
	if len(memory.Args) > 0 {
		return fmt.Errorf("config: memory-load does not take args")
	}
	return nil
}

func validateMemoryAccess(memory *MemoryConfig) error {
	if memory == nil {
		return nil
	}
	if memory.Mode == "" {
		memory.Mode = "native"
	}
	if memory.Mode != "native" {
		return fmt.Errorf("config: memory-access mode %q is not available in v0 (supported: native/null)", memory.Mode)
	}
	if len(memory.Args) > 0 {
		return fmt.Errorf("config: memory-access does not take args")
	}
	return nil
}

func validateMemoryLearn(memory *MemoryConfig) error {
	if memory == nil {
		return nil
	}
	if memory.Mode == "" {
		memory.Mode = "simple-dreaming"
	}
	if memory.Mode != "simple-dreaming" {
		return fmt.Errorf("config: memory-learn mode %q is not available in v0 (supported: simple-dreaming)", memory.Mode)
	}
	if _, err := daysArg(memory); err != nil {
		return err
	}
	return nil
}

func validateMemoryExport(memory *MemoryConfig) error {
	if memory == nil {
		return nil
	}
	if memory.Mode == "" {
		memory.Mode = "opencode-export"
	}
	if memory.Mode != "opencode-export" {
		return fmt.Errorf("config: memory-export mode %q is not available in v0 (supported: opencode-export)", memory.Mode)
	}
	if len(memory.Args) > 0 {
		return fmt.Errorf("config: memory-export does not take args")
	}
	return nil
}

// Days returns the validated lookback/interval for simple-dreaming (default 3).
func (c Config) Days() (int, error) {
	if c.MemoryLearn == nil {
		return 0, nil
	}
	return daysArg(c.MemoryLearn)
}

// maxDreamDays keeps @every <days*24>h inside time.Duration and is a sane
// upper bound for a lookback/interval window (100 years).
const maxDreamDays = 36500

func daysArg(memory *MemoryConfig) (int, error) {
	if memory == nil || len(memory.Args) == 0 {
		return 3, nil
	}
	for key := range memory.Args {
		if key != "days" {
			return 0, fmt.Errorf("config: memory-learn: unsupported arg %q (supported: days)", key)
		}
	}
	raw := memory.Args["days"]
	value, err := jsonInt(raw)
	if err != nil {
		return 0, fmt.Errorf("config: memory-learn.days: %w", err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("config: memory-learn.days must be > 0")
	}
	if value > int64(maxDreamDays) {
		return 0, fmt.Errorf("config: memory-learn.days must be <= %d", maxDreamDays)
	}
	return int(value), nil
}

func jsonInt(raw any) (int64, error) {
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("must be an integer")
		}
		return parsed, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return 0, fmt.Errorf("must be an integer")
		}
		if value > math.MaxInt64 || value < math.MinInt64 {
			return 0, fmt.Errorf("must be an integer")
		}
		return int64(value), nil
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}
