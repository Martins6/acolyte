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
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return fmt.Errorf("workspace: create %s: %w", workspace, err)
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
		if err := ensureDirNoSymlink(workspace, dir); err != nil {
			return err
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
		if err := writeIfMissing(workspace, name, content); err != nil {
			return err
		}
	}
	if err := ensureGitignore(workspace); err != nil {
		return err
	}
	return nil
}

var requiredGitignore = []string{
	".env",
	".acolyte.sqlite.db",
	".acolyte.sqlite.db-shm",
	".acolyte.sqlite.db-wal",
	".logs/",
	"downloads/",
}

// ensureGitignore merges the agent-state patterns into an existing .gitignore
// instead of replacing the user's file.
func ensureGitignore(workspace string) error {
	path := filepath.Join(workspace, ".gitignore")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return writeIfMissing(workspace, ".gitignore", agentGitignore)
	}
	if err != nil {
		return fmt.Errorf("workspace: stat .gitignore: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace: refusing symlinked .gitignore")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("workspace: .gitignore is not a regular file")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workspace: read .gitignore: %w", err)
	}
	present := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		present[strings.TrimSuffix(line, "/")] = true
	}

	var missing []string
	for _, pattern := range requiredGitignore {
		if !present[strings.TrimSuffix(pattern, "/")] {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("workspace: open .gitignore: %w", err)
	}
	defer file.Close()

	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		if _, err := file.WriteString("\n"); err != nil {
			return fmt.Errorf("workspace: append .gitignore: %w", err)
		}
	}
	if _, err := file.WriteString("\n# Acolyte agent state\n"); err != nil {
		return fmt.Errorf("workspace: append .gitignore: %w", err)
	}
	for _, pattern := range missing {
		if _, err := file.WriteString(pattern + "\n"); err != nil {
			return fmt.Errorf("workspace: append .gitignore: %w", err)
		}
	}
	return nil
}

func ensureNoLegacyConfig(workspace string) error {
	legacy := filepath.Join(workspace, "opencode.json")
	if _, err := os.Lstat(legacy); err == nil {
		return fmt.Errorf("workspace: legacy opencode.json found; use opencode.jsonc and remove the legacy file before adopting this folder")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: stat %s: %w", legacy, err)
	}
	return nil
}

// ensureDirNoSymlink creates one directory path under workspace without
// following a symlinked component. It refuses paths that escape the workspace.
func ensureDirNoSymlink(workspace, rel string) error {
	rel, err := safeRel(rel, true)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	current := workspace
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)

		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("workspace: refusing symlinked path component %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("workspace: %s is not a directory", current)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("workspace: stat %s: %w", current, err)
		}

		if err := os.Mkdir(current, 0755); err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("workspace: create %s: %w", current, err)
			}
			// A concurrent creator won the race; re-check without following it.
			info, statErr := os.Lstat(current)
			if statErr != nil {
				return fmt.Errorf("workspace: stat %s: %w", current, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("workspace: refusing symlinked path component %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("workspace: %s is not a directory", current)
			}
		}
	}
	return nil
}

func writeIfMissing(workspace, rel, content string) error {
	rel, err := safeRel(rel, false)
	if err != nil {
		return err
	}
	path := filepath.Join(workspace, rel)
	if err := ensureDirNoSymlink(workspace, filepath.Dir(rel)); err != nil {
		return err
	}

	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace: refusing to overwrite symlink %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("workspace: stat %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("workspace: create %s: %w", path, err)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return fmt.Errorf("workspace: write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", path, err)
	}
	return nil
}

// checkNoSymlinkComponents verifies that no component under the workspace is a
// symlink, without creating anything.
func checkNoSymlinkComponents(workspace, rel string) error {
	rel, err := safeRel(rel, true)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	current := workspace
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace: symlinked path component %s", current)
		}
	}
	return nil
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

	var missing []string
	for _, name := range []string{
		"AGENTS.md",
		"opencode.jsonc",
		".acolyte/CONFIG.md",
		".acolyte/SOUL.md",
		".acolyte.sqlite.db",
	} {
		path := filepath.Join(workspace, name)
		if err := checkNoSymlinkComponents(workspace, name); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, name)
				continue
			}
			if strings.Contains(err.Error(), "symlinked path component") {
				missing = append(missing, name+" (symlink not allowed)")
				continue
			}
			return err
		}
		fileInfo, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, name)
				continue
			}
			return fmt.Errorf("workspace: stat %s: %w", name, err)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			missing = append(missing, name+" (symlink not allowed)")
			continue
		}
		if fileInfo.IsDir() {
			missing = append(missing, name+" (expected a file)")
		}
	}
	if err := checkNoSymlinkComponents(workspace, ".agents/skills"); err != nil {
		if os.IsNotExist(err) {
			missing = append(missing, ".agents/skills/")
		} else if strings.Contains(err.Error(), "symlinked path component") {
			missing = append(missing, ".agents/skills/ (symlink not allowed)")
		} else {
			return err
		}
	} else if info, err := os.Lstat(filepath.Join(workspace, ".agents", "skills")); err != nil {
		missing = append(missing, ".agents/skills/")
	} else if info.Mode()&os.ModeSymlink != 0 {
		missing = append(missing, ".agents/skills/ (symlink not allowed)")
	} else if !info.IsDir() {
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
