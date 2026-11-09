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
	return persistSessionFor(env, env.Thread, sessionID)
}

func persistSessionFor(env Env, thread, sessionID string) error {
	if env.Store == nil || thread == "" || sessionID == "" {
		return nil
	}
	_, err := env.Store.Orch().UpsertSession(thread, sessionID, env.Clock())
	return err
}

// memoryFile captures the content a dream run may legitimately rewrite.
type memoryFile struct {
	hash       string
	data       []byte
	mode       os.FileMode
	symlink    bool
	linkTarget string
}

// snapshotMemory records the persona and skill files before/after a dream run.
// It never follows symlinks; callers decide whether links are allowed.
func snapshotMemory(workspace string) (map[string]memoryFile, error) {
	if err := checkMemoryRoots(workspace); err != nil {
		return nil, err
	}
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
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				snapshot[relative] = memoryFile{
					hash:       "symlink:" + target,
					symlink:    true,
					linkTarget: target,
				}
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			snapshot[relative] = memoryFile{
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

// checkMemoryRoots refuses symlinked roots so a walk cannot escape the workspace.
func checkMemoryRoots(workspace string) error {
	for _, relative := range []string{".acolyte", ".agents", ".agents/skills"} {
		path := filepath.Join(workspace, relative)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("routine: inspect memory root %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("routine: memory root %s is a symlink", relative)
		}
	}
	return nil
}

// assertNoMemorySymlinks fails closed before a model turn can write through a
// pre-existing link to a path outside the workspace.
func assertNoMemorySymlinks(snapshot map[string]memoryFile) error {
	var links []string
	for path, file := range snapshot {
		if file.symlink {
			links = append(links, path)
		}
	}
	if len(links) == 0 {
		return nil
	}
	sort.Strings(links)
	return fmt.Errorf("routine: refusing dream writes through symlinks: %s", strings.Join(links, ", "))
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

// revertMemoryChanges restores every change made by the analysis turn. That
// turn reads untrusted conversations and must never commit memory itself.
func revertMemoryChanges(workspace string, before, after map[string]memoryFile) ([]string, error) {
	var reverted []string
	for _, path := range changedPaths(before, after) {
		previous, existed := before[path]
		if err := restoreMemoryFile(workspace, path, previous, existed, after[path]); err != nil {
			return nil, err
		}
		reverted = append(reverted, path+" (analysis turn must not write)")
	}
	return reverted, nil
}

// enforceMemoryPolicy keeps writer-phase dream writes inside the persona/skill
// allowlist and restores anything outside it. Session text is untrusted, so
// prompt guidance is backed by a deterministic file-level check.
func enforceMemoryPolicy(workspace string, before, after map[string]memoryFile) (changed, reverted []string, err error) {
	for _, path := range changedPaths(before, after) {
		if reason := memoryPolicyViolation(path, after[path]); reason == "" {
			changed = append(changed, path)
			continue
		} else {
			previous, existed := before[path]
			if err := restoreMemoryFile(workspace, path, previous, existed, after[path]); err != nil {
				return nil, nil, err
			}
			reverted = append(reverted, path+" ("+reason+")")
		}
	}
	return changed, reverted, nil
}

func restoreMemoryFile(workspace, path string, previous memoryFile, existed bool, current memoryFile) error {
	target := filepath.Join(workspace, filepath.FromSlash(path))
	if current.symlink {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("routine: remove symlink %s: %w", path, err)
		}
	}
	if existed {
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("routine: recreate %s: %w", path, err)
		}
		if err := os.WriteFile(target, previous.data, previous.mode); err != nil {
			return fmt.Errorf("routine: restore %s: %w", path, err)
		}
		return nil
	}
	if !current.symlink {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("routine: remove %s: %w", path, err)
		}
	}
	return nil
}

func memoryPolicyViolation(path string, file memoryFile) string {
	if file.symlink {
		return "symlink in memory path"
	}
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

func truncateNote(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func requireOpenCode(env Env) error {
	if env.OpenCode == nil {
		return fmt.Errorf("routine: OpenCode client is not configured")
	}
	return nil
}

// escapeUntrusted keeps text from closing or forging the untrusted-data fences
// embedded in routine prompts.
func escapeUntrusted(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}
