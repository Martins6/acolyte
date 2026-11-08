package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scaffoldTest(t *testing.T, opts AgentOptions) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "agent")
	if err := ScaffoldAgent(ws, opts); err != nil {
		t.Fatalf("ScaffoldAgent() error: %v", err)
	}
	return ws
}

func TestScaffoldAgent(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{
		Name:        "valet",
		Description: "Personal assistant",
		Agent:       "build",
		Model:       "minimax-coding-plan/MiniMax-M3",
	})

	for _, name := range []string{
		"AGENTS.md",
		"opencode.jsonc",
		".acolyte/CONFIG.md",
		".acolyte/SOUL.md",
		".acolyte/TOOLS.md",
		".acolyte/USER.md",
		".acolyte/HEARTBEAT.md",
		".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(ws, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	for _, dir := range []string{
		".agents/skills", ".opencode/agents", ".logs",
		"downloads/images", "downloads/audio", "downloads/documents", "downloads/videos",
	} {
		info, err := os.Stat(filepath.Join(ws, dir))
		if err != nil || !info.IsDir() {
			t.Errorf("missing directory %s: %v", dir, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(ws, "opencode.jsonc"))
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	var config struct {
		Model        string         `json:"model"`
		Instructions []string       `json:"instructions"`
		DefaultAgent string         `json:"default_agent"`
		Agent        map[string]any `json:"agent"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("opencode.jsonc is not valid JSON: %v", err)
	}
	if config.Model != "minimax-coding-plan/MiniMax-M3" {
		t.Errorf("model = %q", config.Model)
	}
	if len(config.Instructions) != 1 || config.Instructions[0] != ".acolyte/*.md" {
		t.Errorf("instructions = %v", config.Instructions)
	}
	if config.DefaultAgent != "build" {
		t.Errorf("default_agent = %q, want build", config.DefaultAgent)
	}
	if len(config.Agent) != 0 {
		t.Errorf("agent block = %v, want empty for build", config.Agent)
	}
}

func TestScaffoldCustomAgent(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{
		Name:        "valet",
		Description: "Personal assistant",
		Agent:       "acolyte",
	})

	raw, err := os.ReadFile(filepath.Join(ws, "opencode.jsonc"))
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	var config struct {
		DefaultAgent string `json:"default_agent"`
		Agent        map[string]struct {
			Description string `json:"description"`
			Prompt      string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("opencode.jsonc is not valid JSON: %v", err)
	}
	if config.DefaultAgent != "acolyte" {
		t.Errorf("default_agent = %q, want acolyte", config.DefaultAgent)
	}
	agent, ok := config.Agent["acolyte"]
	if !ok {
		t.Fatal("agent.acolyte block is missing")
	}
	if agent.Description != "Personal assistant" || !strings.Contains(agent.Prompt, "valet") {
		t.Errorf("agent block = %+v", agent)
	}
}

func TestScaffoldIdempotentAndNonDestructive(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	soulPath := filepath.Join(ws, ".acolyte/SOUL.md")
	custom := "# Custom soul\n"
	if err := os.WriteFile(soulPath, []byte(custom), 0644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	if err := ScaffoldAgent(ws, AgentOptions{Name: "valet"}); err != nil {
		t.Fatalf("ScaffoldAgent(second) error: %v", err)
	}
	raw, err := os.ReadFile(soulPath)
	if err != nil {
		t.Fatalf("read soul: %v", err)
	}
	if string(raw) != custom {
		t.Errorf("soul content = %q, want preserved custom content", raw)
	}
}

func TestScaffoldMergesExistingGitignore(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".gitignore"), []byte("node_modules\n"), 0644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	if err := ScaffoldAgent(ws, AgentOptions{Name: "valet"}); err != nil {
		t.Fatalf("ScaffoldAgent() error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "node_modules") {
		t.Error("existing gitignore content was removed")
	}
	for _, pattern := range requiredGitignore {
		if !strings.Contains(text, pattern) {
			t.Errorf("gitignore missing %q", pattern)
		}
	}

	// Idempotent: a second scaffold must not duplicate the patterns.
	if err := ScaffoldAgent(ws, AgentOptions{Name: "valet"}); err != nil {
		t.Fatalf("ScaffoldAgent(second) error: %v", err)
	}
	raw, err = os.ReadFile(filepath.Join(ws, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if count := strings.Count(string(raw), ".acolyte.sqlite.db\n"); count != 1 {
		t.Errorf("db pattern appears %d times, want 1", count)
	}
}

func TestScaffoldGitignoreFilePatternsNotSatisfiedByDirectories(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".gitignore"), []byte(".env/\n.acolyte.sqlite.db/\n.logs\n"), 0644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	if err := ScaffoldAgent(ws, AgentOptions{Name: "valet"}); err != nil {
		t.Fatalf("ScaffoldAgent() error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	text := string(raw)
	if !hasExactLine(text, ".env") {
		t.Error("directory-only .env/ did not receive a file .env rule")
	}
	if !hasExactLine(text, ".acolyte.sqlite.db") {
		t.Error("directory-only .acolyte.sqlite.db/ did not receive a file rule")
	}
	if countExactLine(text, ".logs") != 1 {
		t.Errorf(".logs line count = %d, want 1", countExactLine(text, ".logs"))
	}
}

func hasExactLine(text, want string) bool {
	return countExactLine(text, want) > 0
}

func countExactLine(text, want string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			count++
		}
	}
	return count
}

func TestScaffoldRejectsSymlinkedGitignore(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(target, []byte("node_modules\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(ws, ".gitignore")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ScaffoldAgent(ws, AgentOptions{Name: "valet"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ScaffoldAgent() error = %v, want symlink error", err)
	}
}

func TestScaffoldRejectsLegacyConfig(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "opencode.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	if err := ScaffoldAgent(ws, AgentOptions{Name: "valet"}); err == nil {
		t.Fatal("ScaffoldAgent() error = nil, want legacy config error")
	}
	if _, err := os.Stat(filepath.Join(ws, "opencode.jsonc")); !os.IsNotExist(err) {
		t.Error("opencode.jsonc was created despite the legacy config")
	}
}

func TestScaffoldRejectsSymlinkedDirectory(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, ".acolyte")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ScaffoldAgent(ws, AgentOptions{Name: "valet"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ScaffoldAgent() error = %v, want symlink error", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("scaffold wrote %d entries outside the workspace", len(entries))
	}
}

func TestScaffoldRejectsSymlinkedFile(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(filepath.Join(ws, ".acolyte"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(ws, ".acolyte", "SOUL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ScaffoldAgent(ws, AgentOptions{Name: "valet"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ScaffoldAgent() error = %v, want symlink error", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(raw) != "original" {
		t.Errorf("target = %q, want original", raw)
	}
}

func TestValidateAgentRejectsSymlinkedFile(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	if err := os.WriteFile(filepath.Join(ws, ".acolyte.sqlite.db"), nil, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	target := filepath.Join(t.TempDir(), "agents.md")
	if err := os.WriteFile(target, []byte("# target\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Remove(filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Fatalf("remove AGENTS.md: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ValidateAgent(ws)
	if err == nil || !strings.Contains(err.Error(), "symlink not allowed") {
		t.Fatalf("ValidateAgent() error = %v, want symlink error", err)
	}
}

func TestValidateRejectsSymlinkedPersonaDir(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	if err := os.WriteFile(filepath.Join(ws, ".acolyte.sqlite.db"), nil, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "CONFIG.md"), []byte("# config\n"), 0644); err != nil {
		t.Fatalf("write outside config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "SOUL.md"), []byte("# soul\n"), 0644); err != nil {
		t.Fatalf("write outside soul: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(ws, ".acolyte")); err != nil {
		t.Fatalf("remove .acolyte: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".acolyte")); err != nil {
		t.Fatalf("symlink .acolyte: %v", err)
	}

	err := ValidateAgent(ws)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ValidateAgent() error = %v, want symlink error", err)
	}
}

func TestValidateRejectsSymlinkedSkillsParent(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	if err := os.WriteFile(filepath.Join(ws, ".acolyte.sqlite.db"), nil, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "skills"), 0755); err != nil {
		t.Fatalf("mkdir outside skills: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(ws, ".agents")); err != nil {
		t.Fatalf("remove .agents: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".agents")); err != nil {
		t.Fatalf("symlink .agents: %v", err)
	}

	err := ValidateAgent(ws)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ValidateAgent() error = %v, want symlink error", err)
	}
}

func TestValidateAgent(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	if err := os.WriteFile(filepath.Join(ws, ".acolyte.sqlite.db"), nil, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	if err := ValidateAgent(ws); err != nil {
		t.Fatalf("ValidateAgent() error: %v", err)
	}
}

func TestValidateAgentMissingArtifacts(t *testing.T) {
	ws := t.TempDir()
	err := ValidateAgent(ws)
	if err == nil {
		t.Fatal("ValidateAgent(empty) error = nil, want error")
	}
	for _, want := range []string{"AGENTS.md", "opencode.jsonc", ".acolyte/CONFIG.md", ".acolyte/SOUL.md", ".acolyte.sqlite.db", ".agents/skills/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateAgentRejectsLegacyConfig(t *testing.T) {
	ws := scaffoldTest(t, AgentOptions{Name: "valet"})
	if err := os.WriteFile(filepath.Join(ws, ".acolyte.sqlite.db"), nil, 0600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "opencode.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	if err := ValidateAgent(ws); err == nil || !strings.Contains(err.Error(), "legacy opencode.json") {
		t.Fatalf("ValidateAgent() error = %v, want legacy config error", err)
	}
}
