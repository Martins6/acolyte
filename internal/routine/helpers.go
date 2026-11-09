package routine

import (
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

// snapshotMemory hashes the persona and skill files before/after a dream run.
func snapshotMemory(workspace string) (map[string]string, error) {
	snapshot := map[string]string{}
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
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			snapshot[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return snapshot, nil
}

func changedPaths(before, after map[string]string) []string {
	var paths []string
	for path, hash := range after {
		if before[path] != hash {
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

func skillsCreated(before, after map[string]string) int {
	created := 0
	for path := range after {
		if before[path] != "" {
			continue
		}
		if strings.HasPrefix(path, ".agents/skills/") && strings.HasSuffix(path, "SKILL.md") {
			created++
		}
	}
	return created
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
