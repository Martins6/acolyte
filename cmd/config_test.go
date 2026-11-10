package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store"
)

// setupConfigCLI registers "valet" with a custom --config.
func setupConfigCLI(t *testing.T, raw string) string {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	ws := filepath.Join(t.TempDir(), "agent")
	cfg, err := agent.ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{Name: "valet", Workspace: ws, Config: cfg}); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return ws
}

func withFakeEditor(t *testing.T, fn func(path string) error) {
	t.Helper()
	previous := openEditor
	openEditor = fn
	t.Cleanup(func() { openEditor = previous })
}

func TestRunConfigGetDefaults(t *testing.T) {
	setupCronCLI(t)

	var out bytes.Buffer
	if err := runConfigGet(&out, "valet", false); err != nil {
		t.Fatalf("runConfigGet() error: %v", err)
	}
	var cfg agent.Config
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if cfg.Description != "" || cfg.Engine.Agent != "build" || cfg.Engine.Model != "" {
		t.Errorf("defaults = %+v", cfg)
	}
	if cfg.Schedule.Timezone != "" || cfg.Schedule.HeartbeatMinutes != 0 {
		t.Errorf("schedule = %+v, want empty", cfg.Schedule)
	}
	if cfg.MemoryLoad != nil || cfg.MemoryLearn != nil || cfg.MemoryExport != nil || cfg.MemoryAccess != nil {
		t.Errorf("memory sections = %+v, want nil", cfg)
	}
	if len(cfg.Communication.Channels) != 1 || cfg.Communication.Channels[0] != "telegram" {
		t.Errorf("channels = %v", cfg.Communication.Channels)
	}
	if cfg.Communication.Telegram.AllowedUsers == nil || len(cfg.Communication.Telegram.AllowedUsers) != 0 {
		t.Errorf("allowed_users = %v, want empty list", cfg.Communication.Telegram.AllowedUsers)
	}
	if !strings.Contains(out.String(), "\n") {
		t.Error("default get output should be indented, not one line")
	}
}

func TestRunConfigGetFull(t *testing.T) {
	ws := setupConfigCLI(t, `{
		"description": "Personal assistant",
		"engine": { "model": "minimax-coding-plan/MiniMax-M3" },
		"memory-learn": { "args": { "days": 4 } },
		"communication": { "telegram": { "allowed_users": ["@you"] } },
		"schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 30 }
	}`)

	var out bytes.Buffer
	if err := runConfigGet(&out, ws, true); err != nil {
		t.Fatalf("runConfigGet() error: %v", err)
	}
	if strings.Contains(strings.TrimSpace(out.String()), "\n") {
		t.Errorf("--json output should be one line:\n%s", out.String())
	}
	var cfg agent.Config
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if cfg.Description != "Personal assistant" {
		t.Errorf("description = %q", cfg.Description)
	}
	if cfg.Engine.Model != "minimax-coding-plan/MiniMax-M3" || cfg.Engine.Agent != "build" {
		t.Errorf("engine = %+v", cfg.Engine)
	}
	if cfg.Schedule != (agent.ScheduleConfig{Timezone: "America/Sao_Paulo", HeartbeatMinutes: 30}) {
		t.Errorf("schedule = %+v", cfg.Schedule)
	}
	if cfg.MemoryLearn == nil {
		t.Fatal("memory-learn is nil, want simple-dreaming")
	}
	if days, err := cfg.Days(); err != nil || days != 4 {
		t.Errorf("days = %d, %v, want 4", days, err)
	}
	if len(cfg.Communication.Telegram.AllowedUsers) != 1 || cfg.Communication.Telegram.AllowedUsers[0] != "@you" {
		t.Errorf("allowed_users = %v", cfg.Communication.Telegram.AllowedUsers)
	}
}

func TestRunConfigSetDryRunLeavesStateUnchanged(t *testing.T) {
	ws := setupCronCLI(t)
	path := filepath.Join(ws, "opencode.jsonc")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}

	var out bytes.Buffer
	if err := runConfigSet(&out, "valet", "engine.model", "acme/model-1", true, false); err != nil {
		t.Fatalf("runConfigSet(dry-run) error: %v", err)
	}
	if !strings.Contains(out.String(), "Would set") || !strings.Contains(out.String(), "--- a/opencode.jsonc") {
		t.Errorf("dry-run output = %q", out.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	if !bytes.Equal(original, after) {
		t.Errorf("dry-run rewrote opencode.jsonc:\n%s", after)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if jobs, err := st.Orch().ListCronJobs(); err != nil || len(jobs) != 0 {
		t.Errorf("jobs = %v, %v, want none", jobs, err)
	}
}

func TestRunConfigSetAppliesChanges(t *testing.T) {
	ws := setupConfigCLI(t, `{"schedule": {"timezone": "UTC"}}`)

	var out bytes.Buffer
	if err := runConfigSet(&out, "valet", "schedule.heartbeat_minutes", "45", false, false); err != nil {
		t.Fatalf("runConfigSet(heartbeat) error: %v", err)
	}
	if !strings.Contains(out.String(), "upsert job") {
		t.Errorf("output = %q", out.String())
	}
	if err := runConfigSet(&out, "valet", "description", "Night watch", false, false); err != nil {
		t.Fatalf("runConfigSet(description) error: %v", err)
	}
	if err := runConfigSet(&out, "valet", "communication.telegram.allowed_users", "@a,@b", false, false); err != nil {
		t.Fatalf("runConfigSet(users) error: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("heartbeat")
	st.Close()
	if err != nil || job.Spec != "@every 45m" {
		t.Errorf("heartbeat = %+v, %v", job, err)
	}

	var get bytes.Buffer
	if err := runConfigGet(&get, "valet", true); err != nil {
		t.Fatalf("runConfigGet() error: %v", err)
	}
	var cfg agent.Config
	if err := json.Unmarshal(get.Bytes(), &cfg); err != nil {
		t.Fatalf("get output: %v", err)
	}
	if cfg.Description != "Night watch" {
		t.Errorf("description = %q", cfg.Description)
	}
	if len(cfg.Communication.Telegram.AllowedUsers) != 2 {
		t.Errorf("allowed_users = %v", cfg.Communication.Telegram.AllowedUsers)
	}
}

func TestRunConfigSetJSONOutput(t *testing.T) {
	setupCronCLI(t)

	var out bytes.Buffer
	if err := runConfigSet(&out, "valet", "engine.model", "acme/model-1", true, true); err != nil {
		t.Fatalf("runConfigSet() error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if payload["key"] != "engine.model" || payload["dry_run"] != true || payload["changed"] != true {
		t.Errorf("payload = %v", payload)
	}
	if diff, ok := payload["opencode_diff"].(string); !ok || !strings.Contains(diff, `"model"`) {
		t.Errorf("opencode_diff = %v", payload["opencode_diff"])
	}
}

func TestRunConfigEditFakeEditor(t *testing.T) {
	ws := setupCronCLI(t)
	path := filepath.Join(ws, "opencode.jsonc")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}

	// No change: valid JSONC, editor made no edit.
	withFakeEditor(t, func(string) error { return nil })
	var out bytes.Buffer
	if err := runConfigEdit(&out, "valet", false); err != nil {
		t.Fatalf("edit (no change) error: %v", err)
	}
	if !strings.Contains(out.String(), "No changes") {
		t.Errorf("output = %q", out.String())
	}
	if same, _ := os.ReadFile(path); !bytes.Equal(same, original) {
		t.Error("no-change edit rewrote the file")
	}

	// A valid edit is accepted.
	withFakeEditor(t, func(target string) error {
		return os.WriteFile(target, append([]byte("// edited\n"), original...), 0644)
	})
	if err := runConfigEdit(&out, "valet", false); err != nil {
		t.Fatalf("edit (valid) error: %v", err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	if !strings.Contains(string(edited), "// edited") {
		t.Errorf("edit not applied:\n%s", edited)
	}

	// An invalid edit is rejected and rolled back.
	withFakeEditor(t, func(target string) error {
		return os.WriteFile(target, []byte("not jsonc at all"), 0644)
	})
	err = runConfigEdit(&out, "valet", false)
	if err == nil || !strings.Contains(err.Error(), "not valid JSONC") {
		t.Fatalf("edit (invalid) error = %v, want JSONC error", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	if !bytes.Equal(restored, edited) {
		t.Errorf("invalid edit did not restore the previous file:\n%s", restored)
	}
}

func TestRunConfigCommandDispatch(t *testing.T) {
	setupCronCLI(t)

	cases := []struct {
		args    []string
		flags   configFlags
		wantErr string
	}{
		{args: []string{"valet"}, wantErr: "missing subcommand"},
		{args: []string{"valet", "bogus"}, wantErr: "unknown subcommand"},
		{args: []string{"valet", "get", "extra"}, wantErr: "usage:"},
		{args: []string{"valet", "set", "engine.model"}, wantErr: "usage:"},
		{args: []string{"valet", "edit", "extra"}, wantErr: "usage:"},
		{args: []string{"valet", "get"}, flags: configFlags{DryRun: true}, wantErr: "--dry-run is only valid"},
		{args: []string{"", "get"}, wantErr: "required"},
	}
	for _, tc := range cases {
		err := runConfigCommand(&bytes.Buffer{}, tc.args, tc.flags)
		if err == nil {
			t.Errorf("runConfigCommand(%v) error = nil, want error", tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("runConfigCommand(%v) error = %v, want containing %q", tc.args, err, tc.wantErr)
		}
	}

	// A real get through the dispatcher.
	var out bytes.Buffer
	if err := runConfigCommand(&out, []string{"valet", "get"}, configFlags{JSON: true}); err != nil {
		t.Fatalf("dispatch get error: %v", err)
	}
	if !strings.Contains(out.String(), `"engine"`) {
		t.Errorf("get output = %q", out.String())
	}
}

func TestRunConfigSetUnknownKey(t *testing.T) {
	setupCronCLI(t)
	err := runConfigSet(&bytes.Buffer{}, "valet", "nope", "1", false, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported key") {
		t.Fatalf("error = %v, want unsupported-key error", err)
	}
}
