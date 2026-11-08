package agent

import (
	"strings"
	"testing"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig("{}")
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if cfg.Engine.Agent != "build" {
		t.Errorf("engine.agent = %q, want build", cfg.Engine.Agent)
	}
	if len(cfg.Communication.Channels) != 1 || cfg.Communication.Channels[0] != "telegram" {
		t.Errorf("channels = %v, want [telegram]", cfg.Communication.Channels)
	}
	if cfg.MemoryLearn != nil || cfg.MemoryExport != nil || cfg.MemoryLoad != nil || cfg.MemoryAccess != nil {
		t.Error("memory sections should default to nil")
	}
	if cfg.Schedule.HeartbeatMinutes != 0 {
		t.Errorf("heartbeat = %d, want 0", cfg.Schedule.HeartbeatMinutes)
	}
}

func TestParseConfigFull(t *testing.T) {
	raw := `{
		"description": "Personal assistant",
		"engine": { "model": "minimax-coding-plan/MiniMax-M3", "agent": "build" },
		"memory-load": { "mode": "default" },
		"memory-access": { "mode": "native" },
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 5 } },
		"memory-export": { "mode": "opencode-export" },
		"communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
		"schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 60 }
	}`
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if cfg.Description != "Personal assistant" {
		t.Errorf("description = %q", cfg.Description)
	}
	if cfg.Engine.Model != "minimax-coding-plan/MiniMax-M3" {
		t.Errorf("model = %q", cfg.Engine.Model)
	}
	days, err := cfg.Days()
	if err != nil {
		t.Fatalf("Days() error: %v", err)
	}
	if days != 5 {
		t.Errorf("days = %d, want 5", days)
	}
	if cfg.Communication.Telegram.AllowedUsers[0] != "@you" {
		t.Errorf("allowed users = %v", cfg.Communication.Telegram.AllowedUsers)
	}
	if cfg.Schedule.HeartbeatMinutes != 60 {
		t.Errorf("heartbeat = %d, want 60", cfg.Schedule.HeartbeatMinutes)
	}
}

func TestParseConfigUnknownField(t *testing.T) {
	if _, err := ParseConfig(`{"typo": true}`); err == nil {
		t.Fatal("ParseConfig(unknown field) error = nil, want error")
	}
}

func TestParseConfigUnknownMode(t *testing.T) {
	raw := `{"memory-learn":{"mode":"post-session","args":{"days":3}},"schedule":{"timezone":"UTC"}}`
	if _, err := ParseConfig(raw); err == nil || !strings.Contains(err.Error(), "not available in v0") {
		t.Fatalf("ParseConfig(unknown mode) error = %v", err)
	}
}

func TestParseConfigTimezoneRequired(t *testing.T) {
	raw := `{"memory-learn":{"mode":"simple-dreaming"}}`
	if _, err := ParseConfig(raw); err == nil || !strings.Contains(err.Error(), "schedule.timezone is required") {
		t.Fatalf("ParseConfig(missing timezone) error = %v", err)
	}
}

func TestParseConfigInvalidTimezone(t *testing.T) {
	raw := `{"schedule":{"timezone":"Not/A/Zone"}}`
	if _, err := ParseConfig(raw); err == nil {
		t.Fatal("ParseConfig(invalid timezone) error = nil, want error")
	}
}

func TestParseConfigDaysDefault(t *testing.T) {
	raw := `{"memory-learn":{"mode":"simple-dreaming"},"schedule":{"timezone":"UTC"}}`
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	days, err := cfg.Days()
	if err != nil {
		t.Fatalf("Days() error: %v", err)
	}
	if days != 3 {
		t.Errorf("days = %d, want 3", days)
	}
}

func TestParseConfigDaysInvalid(t *testing.T) {
	for _, raw := range []string{
		`{"memory-learn":{"args":{"days":0}},"schedule":{"timezone":"UTC"}}`,
		`{"memory-learn":{"args":{"days":"3"}},"schedule":{"timezone":"UTC"}}`,
		`{"memory-learn":{"args":{"weeks":3}},"schedule":{"timezone":"UTC"}}`,
	} {
		if _, err := ParseConfig(raw); err == nil {
			t.Errorf("ParseConfig(%s) error = nil, want error", raw)
		}
	}
}

func TestParseConfigDaysTooLarge(t *testing.T) {
	raw := `{"memory-learn":{"args":{"days":4611686018427387905}},"schedule":{"timezone":"UTC"}}`
	if _, err := ParseConfig(raw); err == nil || !strings.Contains(err.Error(), "must be <=") {
		t.Fatalf("ParseConfig(too many days) error = %v, want bound error", err)
	}
}

func TestParseConfigDays32BitOverflow(t *testing.T) {
	raw := `{"memory-learn":{"args":{"days":4294967297}},"schedule":{"timezone":"UTC"}}`
	if _, err := ParseConfig(raw); err == nil {
		t.Fatal("ParseConfig(days beyond int32) error = nil, want bound error")
	}
}

func TestParseConfigDaysMaxAccepted(t *testing.T) {
	raw := `{"memory-learn":{"args":{"days":36500}},"schedule":{"timezone":"UTC"}}`
	if _, err := ParseConfig(raw); err != nil {
		t.Fatalf("ParseConfig(max days) error: %v", err)
	}
}

func TestParseConfigNullMemory(t *testing.T) {
	cfg, err := ParseConfig(`{"memory-learn":null}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if cfg.MemoryLearn != nil {
		t.Errorf("memory-learn = %+v, want nil", cfg.MemoryLearn)
	}
}

func TestParseConfigExplicitEmptyChannels(t *testing.T) {
	cfg, err := ParseConfig(`{"communication":{"channels":[]}}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if len(cfg.Communication.Channels) != 0 {
		t.Errorf("channels = %v, want empty", cfg.Communication.Channels)
	}
}

func TestParseConfigTrailingData(t *testing.T) {
	if _, err := ParseConfig(`{}{}`); err == nil {
		t.Fatal("ParseConfig(trailing data) error = nil, want error")
	}
}

func TestParseConfigNegativeHeartbeat(t *testing.T) {
	if _, err := ParseConfig(`{"schedule":{"heartbeat_minutes":-1}}`); err == nil {
		t.Fatal("ParseConfig(negative heartbeat) error = nil, want error")
	}
}
