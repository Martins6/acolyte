// Package secrets loads Acolyte's layered .env files.
//
// Precedence (highest first): existing process environment, <workspace>/.env,
// $ACOLYTE_HOME/.env. Workspace values are returned in a scoped map and are
// never applied with a process-global os.Setenv, so one daemon can serve many
// agents without leaking secrets between them.
package secrets

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	godotenv "github.com/subosito/gotenv"
)

// Secrets is the merged, scoped view of the global and workspace .env files.
type Secrets struct {
	values    map[string]string
	global    map[string]string
	workspace map[string]string
}

// Load reads the global .env and, when workspace is non-empty, the workspace
// .env. Missing files are not an error. A malformed file returns an error.
func Load(workspace string) (*Secrets, error) {
	globalPath, err := paths.GlobalEnv()
	if err != nil {
		return nil, err
	}
	global, err := readFile(globalPath)
	if err != nil {
		return nil, err
	}

	workspaceValues := map[string]string{}
	if workspace != "" {
		workspaceValues, err = readFile(paths.WorkspaceEnv(workspace))
		if err != nil {
			return nil, err
		}
	}

	merged := make(map[string]string, len(global)+len(workspaceValues))
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range workspaceValues {
		merged[k] = v
	}

	return &Secrets{
		values:    merged,
		global:    global,
		workspace: workspaceValues,
	}, nil
}

func readFile(path string) (map[string]string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("secrets: stat %s: %w", path, err)
	}

	values, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read %s: %w", path, err)
	}
	return values, nil
}

// Lookup resolves a key with process environment first, then workspace, then
// global.
func (s *Secrets) Lookup(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	if s == nil {
		return "", false
	}
	value, ok := s.values[key]
	return value, ok
}

// Environ returns the current process environment with merged file values for
// keys that are not already set. Precedence is preserved: an exported process
// variable always wins over both .env files.
func (s *Secrets) Environ() []string {
	env := os.Environ()
	present := make(map[string]bool, len(env))
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok {
			present[key] = true
		}
	}
	if s == nil {
		return env
	}

	keys := make([]string, 0, len(s.values))
	for key := range s.values {
		if !present[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+s.values[key])
	}
	return env
}

// Global returns the values read from $ACOLYTE_HOME/.env.
func (s *Secrets) Global() map[string]string {
	if s == nil {
		return nil
	}
	return clone(s.global)
}

// Workspace returns the values read from <workspace>/.env.
func (s *Secrets) Workspace() map[string]string {
	if s == nil {
		return nil
	}
	return clone(s.workspace)
}

func clone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
