package routine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func persistSession(env Env, sessionID string) error {
	if env.Store == nil || env.Thread == "" || sessionID == "" {
		return nil
	}
	_, err := env.Store.Orch().UpsertSession(env.Thread, sessionID, env.Clock())
	return err
}

// memoryFile captures the content a dream run may legitimately rewrite.
type memoryFile struct {
	hash string
	data []byte
	mode os.FileMode
}

// snapshotMemory records the persona and skill files before/after a dream run.
func snapshotMemory(workspace string) (map[string]memoryFile, error) {
	snapshot := map[string]memoryFile{}
	for _, root := range []string{".acolyte", ".agents/skills"} {
		base := filepath.Join(workspace, root)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.IsDir() {
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			snapshot[filepath.ToSlash(relative)] = memoryFile{
				hash: hex.EncodeToString(sum[:]),
				data: raw,
				mode: info.Mode().Perm(),
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return snapshot, nil
}

func changedPaths(before, after map[string]memoryFile) []string {
	var paths []string
	for path, file := range after {
		if previous, ok := before[path]; !ok || previous.hash != file.hash {
			paths = append(paths, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

// enforceMemoryPolicy keeps dream writes inside the persona/skill allowlist and
// restores anything outside it. Session text is untrusted, so prompt guidance is
// backed by a deterministic file-level check.
func enforceMemoryPolicy(workspace string, before, after map[string]memoryFile) (changed, reverted []string, err error) {
	for _, path := range changedPaths(before, after) {
		reason := memoryPolicyViolation(path, after[path])
		if reason == "" {
			changed = append(changed, path)
			continue
		}
		target := filepath.Join(workspace, filepath.FromSlash(path))
		if previous, ok := before[path]; ok {
			if err := os.WriteFile(target, previous.data, previous.mode); err != nil {
				return nil, nil, fmt.Errorf("routine: restore %s: %w", path, err)
			}
		} else if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("routine: remove %s: %w", path, err)
		}
		reverted = append(reverted, path+" ("+reason+")")
	}
	return changed, reverted, nil
}

func memoryPolicyViolation(path string, file memoryFile) string {
	if !allowedMemoryPath(path) {
		return "path outside memory allowlist"
	}
	if bytes.IndexByte(file.data, 0) >= 0 {
		return "binary content"
	}
	if len(file.data) > memoryFileLimit(path) {
		return "file exceeds size limit"
	}
	return ""
}

func allowedMemoryPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == ".acolyte" && parts[1] != "HEARTBEAT.md" {
		return strings.HasSuffix(parts[1], ".md")
	}
	if len(parts) == 4 && parts[0] == ".agents" && parts[1] == "skills" && parts[2] != "" {
		return parts[3] == "SKILL.md"
	}
	return false
}

func memoryFileLimit(path string) int {
	if strings.HasPrefix(path, ".acolyte/") {
		return 64 * 1024
	}
	return 32 * 1024
}

func skillsCreated(before map[string]memoryFile, changed []string) int {
	created := 0
	for _, path := range changed {
		if _, ok := before[path]; ok {
			continue
		}
		if strings.HasPrefix(path, ".agents/skills/") && strings.HasSuffix(path, "/SKILL.md") {
			created++
		}
	}
	return created
}

// escapeUntrusted keeps text from closing or forging the untrusted-data fences
// embedded in routine prompts.
func escapeUntrusted(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}

func truncateNote(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func changeNote(result string, changed []string, dryRun bool) string {
	note := truncateNote(result, 4000)
	if note == "" {
		note = "(no model reply)"
	}
	if dryRun {
		note = "DRY RUN\n" + note
	}
	if len(changed) > 0 {
		note += "\nchanged files:\n- " + strings.Join(changed, "\n- ")
	} else {
		note += "\nchanged files: none"
	}
	return note
}

func requireOpenCode(env Env) error {
	if env.OpenCode == nil {
		return fmt.Errorf("routine: OpenCode client is not configured")
	}
	return nil
}
