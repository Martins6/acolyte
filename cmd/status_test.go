package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
)

func setupAgentHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	return home
}

func createTestAgent(t *testing.T, name, workspace, raw string) {
	t.Helper()
	cfg, err := agent.ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{
		Name:      name,
		Workspace: workspace,
		Config:    cfg,
	}); err != nil {
		t.Fatalf("Create(%s) error: %v", name, err)
	}
}

func TestRunStatusEmpty(t *testing.T) {
	setupAgentHome(t)
	var out bytes.Buffer
	if err := runStatus(&out, ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	if !strings.Contains(out.String(), "No agents registered.") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunStatusList(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{"schedule":{"timezone":"UTC","heartbeat_minutes":60}}`)

	var out bytes.Buffer
	if err := runStatus(&out, ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "NAME") || !strings.Contains(text, "WORKSPACE") {
		t.Errorf("table header missing: %q", text)
	}
	if !strings.Contains(text, "CHANNELS") || !strings.Contains(text, "LAST MESSAGE") {
		t.Errorf("table header missing communication columns: %q", text)
	}
	if !strings.Contains(text, "valet") || !strings.Contains(text, ws) {
		t.Errorf("agent row missing: %q", text)
	}
	if !strings.Contains(text, "telegram") {
		t.Errorf("agent row missing default channel: %q", text)
	}
}

func TestRunStatusListUnavailable(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{}`)
	if err := os.Remove(filepath.Join(ws, ".acolyte.sqlite.db")); err != nil {
		t.Fatalf("remove store: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus(&out, ""); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	if !strings.Contains(out.String(), "unavailable") {
		t.Errorf("output does not report unavailable stats:\n%s", out.String())
	}
}

func TestRunStatusDetailUnavailable(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{}`)

	dbPath := filepath.Join(ws, ".acolyte.sqlite.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		_ = os.Remove(path)
	}
	if err := os.Mkdir(dbPath, 0755); err != nil {
		t.Fatalf("replace store with directory: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus(&out, "valet"); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Last active: unavailable") {
		t.Errorf("output missing unavailable activity:\n%s", text)
	}
	if !strings.Contains(text, "Jobs:        unavailable") {
		t.Errorf("output missing unavailable jobs:\n%s", text)
	}
}

func TestRunStatusDetailStatsFailure(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{}`)

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.DB().Exec(`DROP TABLE orch_cron_jobs`); err != nil {
		t.Fatalf("drop cron table: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus(&out, "valet"); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Last active: unavailable") {
		t.Errorf("output missing unavailable activity:\n%s", text)
	}
	if !strings.Contains(text, "Jobs:        unavailable") {
		t.Errorf("output missing unavailable jobs:\n%s", text)
	}
}

func TestRunStatusDetail(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{
		"description": "Personal assistant",
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 30 }
	}`)

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Deleting the stored channels setting exercises the status default.
	if err := st.CommTG().DeleteSetting("channels"); err != nil {
		t.Fatalf("delete channels setting: %v", err)
	}
	if err := st.CommTG().SetSetting("telegram.allowed_users", `["@alice", "123"]`); err != nil {
		t.Fatalf("set allowed users: %v", err)
	}
	sentAt := time.Date(2026, 11, 14, 10, 30, 0, 0, time.Local)
	if err := st.CommTG().UpsertChat(10, "alice", true, true, sentAt); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	item, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_status", ChatID: 10, Text: "hello", CreatedAt: sentAt,
	})
	if err != nil {
		t.Fatalf("EnqueueOutbound() error: %v", err)
	}
	if err := st.CommTG().MarkOutboundSent(item.ID, 1, 42, sentAt); err != nil {
		t.Fatalf("MarkOutboundSent() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus(&out, "valet"); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	wantWorkspace, err := paths.Canonical(ws)
	if err != nil {
		t.Fatalf("Canonical(workspace) error: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Name:        valet",
		"Workspace:   " + wantWorkspace,
		"Description: Personal assistant",
		"Channels:    telegram",
		"Allowed:     @alice, 123",
		"Last message: 2026-11-14 10:30",
		"Jobs:        2",
		"dream",
		"heartbeat",
		"memory-learn",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestReadSettingList(t *testing.T) {
	settings := map[string]string{
		"bad":   "not-json",
		"empty": `[]`,
		"valid": `["telegram"]`,
		"blank": "   ",
	}
	get := func(key string) (string, bool, error) {
		value, ok := settings[key]
		return value, ok, nil
	}
	tests := []struct {
		key      string
		fallback []string
		want     []string
	}{
		{key: "missing", fallback: []string{"telegram"}, want: []string{"telegram"}},
		{key: "blank", fallback: []string{"telegram"}, want: []string{"telegram"}},
		{key: "bad", fallback: []string{"telegram"}, want: []string{"telegram"}},
		{key: "empty", fallback: []string{"telegram"}, want: nil},
		{key: "valid", fallback: nil, want: []string{"telegram"}},
	}
	for _, test := range tests {
		got, err := readSettingList(get, test.key, test.fallback)
		if err != nil {
			t.Fatalf("readSettingList(%q) error: %v", test.key, err)
		}
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Errorf("readSettingList(%q) = %v, want %v", test.key, got, test.want)
		}
	}
}

func TestRunStatusByWorkspace(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{}`)

	var out bytes.Buffer
	if err := runStatus(&out, ws); err != nil {
		t.Fatalf("runStatus(workspace) error: %v", err)
	}
	if !strings.Contains(out.String(), "Name:        valet") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunStatusMissing(t *testing.T) {
	setupAgentHome(t)
	var out bytes.Buffer
	if err := runStatus(&out, "missing"); err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("runStatus(missing) error = %v", err)
	}
}

func TestRunDelete(t *testing.T) {
	setupAgentHome(t)
	ws := filepath.Join(t.TempDir(), "valet")
	createTestAgent(t, "valet", ws, `{}`)

	var out bytes.Buffer
	if err := runDelete(&out, "valet"); err != nil {
		t.Fatalf("runDelete() error: %v", err)
	}
	if !strings.Contains(out.String(), `Deleted agent "valet"`) {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(ws); err != nil {
		t.Errorf("workspace was removed: %v", err)
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	agents, err := reg.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("agents = %+v, want empty", agents)
	}

	if err := runDelete(&out, "valet"); err == nil {
		t.Fatal("runDelete(second) error = nil, want error")
	}
}

func TestRunDeleteMissing(t *testing.T) {
	setupAgentHome(t)
	var out bytes.Buffer
	if err := runDelete(&out, "missing"); err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("runDelete(missing) error = %v", err)
	}
}
