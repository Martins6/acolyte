package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
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
	filesystem, err := openDirFS(workspace, true)
	if err != nil {
		return err
	}
	defer filesystem.Close()

	if err := filesystem.ensureNoLegacyConfig(); err != nil {
		return err
	}

	dirs := []string{
		"media/inbound/images",
		"media/inbound/audio",
		"media/inbound/documents",
		"media/inbound/videos",
		"media/outbound/images",
		".acolyte",
		".agents/skills",
		".opencode/agents",
		".logs",
	}
	for _, dir := range dirs {
		created, err := filesystem.ensureDir(dir)
		if err != nil {
			return err
		}
		created.Close()
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
		if err := filesystem.createFileIfMissing(name, content); err != nil {
			return err
		}
	}
	return filesystem.ensureGitignore()
}

var requiredGitignore = []string{
	".env",
	".acolyte.sqlite.db",
	".acolyte.sqlite.db-shm",
	".acolyte.sqlite.db-wal",
	".logs/",
	"downloads/",
	"media/",
}

// ensureGitignore merges the agent-state patterns into an existing .gitignore
// instead of replacing the user's file.
func (d *dirFS) ensureGitignore() error {
	stat, err := d.statNoFollow(".gitignore")
	if errors.Is(err, unix.ENOENT) {
		return d.createFileIfMissing(".gitignore", agentGitignore)
	}
	if err != nil {
		return fmt.Errorf("workspace: stat .gitignore: %w", err)
	}
	if isSymlink(stat) {
		return fmt.Errorf("workspace: refusing symlinked .gitignore")
	}
	if !isRegular(stat) {
		return fmt.Errorf("workspace: .gitignore is not a regular file")
	}

	raw, err := d.readFile(".gitignore")
	if err != nil {
		return fmt.Errorf("workspace: read .gitignore: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	var missing []string
	for _, pattern := range requiredGitignore {
		if !ignoreRulePresent(lines, pattern) {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	var appendText strings.Builder
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		appendText.WriteString("\n")
	}
	appendText.WriteString("\n# Acolyte agent state\n")
	for _, pattern := range missing {
		appendText.WriteString(pattern + "\n")
	}
	return d.appendFile(".gitignore", appendText.String())
}

// ignoreRulePresent matches a required rule without conflating a file with a
// directory-only rule of the same name (for example .env vs .env/).
func ignoreRulePresent(lines []string, required string) bool {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == required {
			return true
		}
		if strings.HasSuffix(required, "/") && line == strings.TrimSuffix(required, "/") {
			return true
		}
	}
	return false
}

func (d *dirFS) ensureNoLegacyConfig() error {
	_, err := d.statNoFollow("opencode.json")
	if err == nil {
		return fmt.Errorf("workspace: legacy opencode.json found; use opencode.jsonc and remove the legacy file before adopting this folder")
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return fmt.Errorf("workspace: stat opencode.json: %w", err)
}

func safeRel(rel string, allowDot bool) (string, error) {
	rel = filepath.Clean(rel)
	if rel == "." {
		if allowDot {
			return rel, nil
		}
		return "", fmt.Errorf("workspace: empty relative path")
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace: path %q escapes the workspace", rel)
	}
	return rel, nil
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

	filesystem, err := openDirFS(workspace, false)
	if err != nil {
		return err
	}
	defer filesystem.Close()

	var missing []string
	for _, name := range []string{
		"AGENTS.md",
		"opencode.jsonc",
		".acolyte/CONFIG.md",
		".acolyte/SOUL.md",
		".acolyte.sqlite.db",
	} {
		stat, err := filesystem.statNoFollow(name)
		if errors.Is(err, unix.ENOENT) {
			missing = append(missing, name)
			continue
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			missing = append(missing, name+" (symlink not allowed)")
			continue
		}
		if err != nil {
			return fmt.Errorf("workspace: stat %s: %w", name, err)
		}
		if isSymlink(stat) {
			missing = append(missing, name+" (symlink not allowed)")
			continue
		}
		if !isRegular(stat) {
			missing = append(missing, name+" (expected a file)")
		}
	}

	stat, err := filesystem.statNoFollow(".agents/skills")
	switch {
	case errors.Is(err, unix.ENOENT):
		missing = append(missing, ".agents/skills/")
	case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
		missing = append(missing, ".agents/skills/ (symlink not allowed)")
	case err != nil:
		return fmt.Errorf("workspace: stat .agents/skills: %w", err)
	case isSymlink(stat):
		missing = append(missing, ".agents/skills/ (symlink not allowed)")
	case !isDir(stat):
		missing = append(missing, ".agents/skills/")
	}

	if err := filesystem.ensureNoLegacyConfig(); err != nil {
		return err
	}

	if len(missing) > 0 {
		return fmt.Errorf("workspace: invalid agent workspace %s: missing %s", workspace, strings.Join(missing, ", "))
	}
	return nil
}
