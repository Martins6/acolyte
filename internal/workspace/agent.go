package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentOptions carries the values baked into a new agent workspace.
type AgentOptions struct {
	Name        string
	Description string
	Agent       string
	Model       string
}

// ScaffoldAgent creates the SPEC §3 workspace layout. It only creates missing
// artifacts and never overwrites a file the user or the agent has edited.
// The per-agent database is created by internal/store, not here.
func ScaffoldAgent(workspace string, opts AgentOptions) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("workspace: empty agent workspace")
	}
	if err := ensureNoLegacyConfig(workspace); err != nil {
		return err
	}

	dirs := []string{
		"downloads/images",
		"downloads/audio",
		"downloads/documents",
		"downloads/videos",
		".acolyte",
		".agents/skills",
		".opencode/agents",
		".logs",
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0755); err != nil {
			return fmt.Errorf("workspace: create %s: %w", dir, err)
		}
	}

	opencode, err := openCodeConfig(opts)
	if err != nil {
		return err
	}

	files := map[string]string{
		"AGENTS.md":             agentAgentsContent(opts),
		"opencode.jsonc":        opencode,
		".acolyte/CONFIG.md":    agentConfigContent,
		".acolyte/SOUL.md":      agentSoulContent,
		".acolyte/TOOLS.md":     agentToolsContent,
		".acolyte/USER.md":      agentUserContent,
		".acolyte/HEARTBEAT.md": agentHeartbeatContent,
		".gitignore":            agentGitignore,
	}
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if err := writeIfMissing(path, content); err != nil {
			return err
		}
	}
	return nil
}

func ensureNoLegacyConfig(workspace string) error {
	legacy := filepath.Join(workspace, "opencode.json")
	if _, err := os.Stat(legacy); err == nil {
		return fmt.Errorf("workspace: legacy opencode.json found; use opencode.jsonc and remove the legacy file before adopting this folder")
	}
	return nil
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("workspace: create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("workspace: write %s: %w", path, err)
	}
	return nil
}

type openCodeConfigFile struct {
	Schema       string                   `json:"$schema,omitempty"`
	Model        string                   `json:"model,omitempty"`
	Instructions []string                 `json:"instructions,omitempty"`
	DefaultAgent string                   `json:"default_agent,omitempty"`
	Agent        map[string]openCodeAgent `json:"agent,omitempty"`
}

type openCodeAgent struct {
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

func openCodeConfig(opts AgentOptions) (string, error) {
	agentID := strings.TrimSpace(opts.Agent)
	if agentID == "" {
		agentID = "build"
	}

	config := openCodeConfigFile{
		Schema:       "https://opencode.ai/config.json",
		Model:        strings.TrimSpace(opts.Model),
		Instructions: []string{".acolyte/*.md"},
		DefaultAgent: agentID,
	}
	if agentID != "build" {
		name := strings.TrimSpace(opts.Name)
		if name == "" {
			name = "acolyte"
		}
		description := strings.TrimSpace(opts.Description)
		if description == "" {
			description = "Acolyte agent"
		}
		config.Agent = map[string]openCodeAgent{
			agentID: {
				Description: description,
				Prompt:      fmt.Sprintf("You are %s, an Acolyte agent powered by OpenCode. Follow the persona in .acolyte/.", name),
			},
		}
	}

	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("workspace: render opencode.jsonc: %w", err)
	}
	return string(raw) + "\n", nil
}

// ValidateAgent checks the required SPEC §3 artifacts. It does not require
// optional persona files or any skill to be present.
func ValidateAgent(workspace string) error {
	info, err := os.Stat(workspace)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workspace: agent workspace %s does not exist", workspace)
		}
		return fmt.Errorf("workspace: stat %s: %w", workspace, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace: %s is not a directory", workspace)
	}

	var missing []string
	for _, name := range []string{
		"AGENTS.md",
		"opencode.jsonc",
		".acolyte/CONFIG.md",
		".acolyte/SOUL.md",
		".acolyte.sqlite.db",
	} {
		path := filepath.Join(workspace, name)
		fileInfo, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, name)
				continue
			}
			return fmt.Errorf("workspace: stat %s: %w", name, err)
		}
		if fileInfo.IsDir() {
			missing = append(missing, name+" (expected a file)")
		}
	}
	if info, err := os.Stat(filepath.Join(workspace, ".agents", "skills")); err != nil || !info.IsDir() {
		missing = append(missing, ".agents/skills/")
	}
	if err := ensureNoLegacyConfig(workspace); err != nil {
		return err
	}

	if len(missing) > 0 {
		return fmt.Errorf("workspace: invalid agent workspace %s: missing %s", workspace, strings.Join(missing, ", "))
	}
	return nil
}
