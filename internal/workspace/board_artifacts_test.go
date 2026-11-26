package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArtifact(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestPlanBoardArtifacts(t *testing.T) {
	ws := t.TempDir()
	writeArtifact(t, filepath.Join(ws, "AGENTS.md"), "# Agent\n\n## Capabilities\n- existing\n")
	writeArtifact(t, filepath.Join(ws, ".acolyte", "CONFIG.md"), "# CONFIG\n- existing\n")
	writeArtifact(t, filepath.Join(ws, ".acolyte", "HEARTBEAT.md"), "# Heartbeat\n- existing\n")

	changes, err := PlanBoardArtifacts(ws, true, false)
	if err != nil {
		t.Fatalf("PlanBoardArtifacts() error: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want 3", changes)
	}
	if err := ApplyArtifactChanges(ws, changes); err != nil {
		t.Fatalf("ApplyArtifactChanges() error: %v", err)
	}

	skill, err := os.ReadFile(filepath.Join(ws, BoardSkillPath))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !strings.Contains(string(skill), "name: board") {
		t.Errorf("skill = %q", skill)
	}
	agents, _ := os.ReadFile(filepath.Join(ws, "AGENTS.md"))
	if !strings.Contains(string(agents), "acolyte:board") || !strings.Contains(string(agents), "existing") {
		t.Errorf("AGENTS.md = %q", agents)
	}
	config, _ := os.ReadFile(filepath.Join(ws, ".acolyte", "CONFIG.md"))
	if !strings.Contains(string(config), "acolyte:board") || !strings.Contains(string(config), "existing") {
		t.Errorf("CONFIG.md = %q", config)
	}
	heartbeat, _ := os.ReadFile(filepath.Join(ws, ".acolyte", "HEARTBEAT.md"))
	if strings.Contains(string(heartbeat), "acolyte:board") {
		t.Errorf("heartbeat marker added without opt-in: %q", heartbeat)
	}

	changes, err = PlanBoardArtifacts(ws, true, false)
	if err != nil {
		t.Fatalf("PlanBoardArtifacts() error: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("second plan changes = %+v, want none", changes)
	}

	changes, err = PlanBoardArtifacts(ws, true, true)
	if err != nil {
		t.Fatalf("PlanBoardArtifacts(heartbeat) error: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != ".acolyte/HEARTBEAT.md" {
		t.Fatalf("heartbeat changes = %+v", changes)
	}
	if err := ApplyArtifactChanges(ws, changes); err != nil {
		t.Fatalf("ApplyArtifactChanges(heartbeat) error: %v", err)
	}
	heartbeat, _ = os.ReadFile(filepath.Join(ws, ".acolyte", "HEARTBEAT.md"))
	if !strings.Contains(string(heartbeat), "acolyte:board") || !strings.Contains(string(heartbeat), "notify-user") {
		t.Errorf("HEARTBEAT.md = %q", heartbeat)
	}

	changes, err = PlanBoardArtifacts(ws, false, false)
	if err != nil {
		t.Fatalf("PlanBoardArtifacts(disable) error: %v", err)
	}
	if len(changes) != 4 {
		t.Fatalf("disable changes = %+v, want 4", changes)
	}
	if err := ApplyArtifactChanges(ws, changes); err != nil {
		t.Fatalf("ApplyArtifactChanges(disable) error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, BoardSkillPath)); !os.IsNotExist(err) {
		t.Errorf("skill still exists: %v", err)
	}
	for _, relative := range []string{"AGENTS.md", ".acolyte/CONFIG.md", ".acolyte/HEARTBEAT.md"} {
		raw, err := os.ReadFile(filepath.Join(ws, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		if strings.Contains(string(raw), "acolyte:board") {
			t.Errorf("%s still has board marker: %q", relative, raw)
		}
		if !strings.Contains(string(raw), "existing") {
			t.Errorf("%s lost user content: %q", relative, raw)
		}
	}
}

func TestApplyArtifactChangesRejectsSymlinkParent(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, ".acolyte")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := ApplyArtifactChanges(ws, []ArtifactChange{{
		Path: ".acolyte/CONFIG.md", After: "external", Action: "create",
	}})
	if err == nil {
		t.Fatal("ApplyArtifactChanges() succeeded through a symlinked parent")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "CONFIG.md")); !os.IsNotExist(statErr) {
		t.Errorf("external file created: %v", statErr)
	}
}

func TestPlanBoardArtifactsRejectsSymlink(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.md")
	writeArtifact(t, target, "outside")
	if err := os.Symlink(target, filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PlanBoardArtifacts(ws, true, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("PlanBoardArtifacts() error = %v, want symlink error", err)
	}
}

func TestPlanBoardArtifactsRejectsMalformedMarkers(t *testing.T) {
	ws := t.TempDir()
	writeArtifact(t, filepath.Join(ws, "AGENTS.md"), "# Agent\n"+boardMarkerStart+"\n")
	if _, err := PlanBoardArtifacts(ws, false, false); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("PlanBoardArtifacts() error = %v, want malformed marker error", err)
	}
}

func TestPlanBoardArtifactsRefusesUserSkill(t *testing.T) {
	ws := t.TempDir()
	skillPath := filepath.Join(ws, BoardSkillPath)
	writeArtifact(t, skillPath, "# my own board skill\n")
	if _, err := PlanBoardArtifacts(ws, true, false); err == nil || !strings.Contains(err.Error(), "not Acolyte-managed") {
		t.Fatalf("PlanBoardArtifacts(enable) error = %v, want managed-file error", err)
	}
	if _, err := PlanBoardArtifacts(ws, false, false); err == nil || !strings.Contains(err.Error(), "not Acolyte-managed") {
		t.Fatalf("PlanBoardArtifacts(disable) error = %v, want managed-file error", err)
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read user skill: %v", err)
	}
	if string(raw) != "# my own board skill\n" {
		t.Errorf("user skill changed: %q", raw)
	}
}

func TestEnsureBoardTools(t *testing.T) {
	ws := t.TempDir()
	if err := EnsureBoardTools(ws); err != nil {
		t.Fatalf("EnsureBoardTools() error: %v", err)
	}
	for _, relative := range []string{BoardReadToolPath, BoardReplyToolPath} {
		raw, err := os.ReadFile(filepath.Join(ws, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		if !strings.Contains(string(raw), "acolyte") {
			t.Errorf("%s = %q", relative, raw)
		}
	}
	// Existing files are preserved.
	path := filepath.Join(ws, BoardReadToolPath)
	writeArtifact(t, path, "user-owned")
	if err := EnsureBoardTools(ws); err == nil || !strings.Contains(err.Error(), "not the Acolyte board tool") {
		t.Fatalf("EnsureBoardTools(user file) error = %v, want conflict", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "user-owned" {
		t.Errorf("EnsureBoardTools overwrote user file: %q", raw)
	}
}
