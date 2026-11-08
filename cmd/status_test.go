package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
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
	if !strings.Contains(text, "valet") || !strings.Contains(text, ws) {
		t.Errorf("agent row missing: %q", text)
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

	var out bytes.Buffer
	if err := runStatus(&out, "valet"); err != nil {
		t.Fatalf("runStatus() error: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Name:        valet",
		"Workspace:   " + ws,
		"Description: Personal assistant",
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
