// Package secrets loads Acolyte's layered configuration.
//
// Precedence (highest first): CLI overrides, existing process environment,
// <workspace>/.env, $ACOLYTE_HOME/.env. Every resolved value carries the
// source it came from, so `acolyte config <agent> env` can label it. Workspace
// values are returned in a scoped map and are never applied with a
// process-global os.Setenv, so one daemon can serve many agents without
// leaking secrets between them.
package secrets

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	godotenv "github.com/subosito/gotenv"
)

// Source identifies the layer a value was resolved from.
type Source string

const (
	// SourceCLI is an explicit --env KEY=VALUE override on this invocation.
	SourceCLI Source = "cli"
	// SourceProcess is the environment the process inherited.
	SourceProcess Source = "process"
	// SourceWorkspace is <workspace>/.env.
	SourceWorkspace Source = ".env-workspace"
	// SourceGlobal is $ACOLYTE_HOME/.env.
	SourceGlobal Source = ".env-global"
)

// Label renders the provenance label for key, for example
// "CONFIG-TELEGRAM_BOT_TOKEN:.env-workspace".
func (src Source) Label(key string) string {
	return "CONFIG-" + key + ":" + string(src)
}

// Secrets is the merged, scoped view of CLI overrides, the global .env, and
// the workspace .env.
type Secrets struct {
	values    map[string]string
	global    map[string]string
	workspace map[string]string
	overrides map[string]string
}

// Option configures Load.
type Option func(*loadOptions)

type loadOptions struct {
	overrides map[string]string
}

// WithOverrides layers CLI-provided values above the process environment and
// both .env files. It lets a single invocation override secrets without
// writing them to disk.
func WithOverrides(values map[string]string) Option {
	return func(opts *loadOptions) {
		opts.overrides = values
	}
}

// Load reads the global .env and, when workspace is non-empty, the workspace
// .env. Missing files are not an error. A malformed file returns an error.
func Load(workspace string, options ...Option) (*Secrets, error) {
	var opts loadOptions
	for _, option := range options {
		option(&opts)
	}

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
		overrides: clone(opts.overrides),
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

// Lookup resolves a key with CLI overrides first, then the process
// environment, then workspace, then global.
func (s *Secrets) Lookup(key string) (string, bool) {
	value, _, ok := s.Resolve(key)
	return value, ok
}

// Resolve returns the effective value for key and the layer it came from.
func (s *Secrets) Resolve(key string) (string, Source, bool) {
	if s != nil {
		if value, ok := s.overrides[key]; ok {
			return value, SourceCLI, true
		}
	}
	if value, ok := os.LookupEnv(key); ok {
		return value, SourceProcess, true
	}
	if s == nil {
		return "", "", false
	}
	if value, ok := s.workspace[key]; ok {
		return value, SourceWorkspace, true
	}
	if value, ok := s.global[key]; ok {
		return value, SourceGlobal, true
	}
	return "", "", false
}

// Keys returns every key declared in the global .env, the workspace .env, or
// the CLI overrides, sorted. Keys that exist only in the process environment
// are not listed; use Resolve for a specific key.
func (s *Secrets) Keys() []string {
	if s == nil {
		return nil
	}
	set := make(map[string]bool, len(s.global)+len(s.workspace)+len(s.overrides))
	for key := range s.global {
		set[key] = true
	}
	for key := range s.workspace {
		set[key] = true
	}
	for key := range s.overrides {
		set[key] = true
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Environ returns the current process environment with the layered values
// applied. Precedence is preserved: a CLI override wins over the process
// environment, which wins over both .env files.
func (s *Secrets) Environ() []string {
	if s == nil {
		return os.Environ()
	}
	current := os.Environ()
	env := make([]string, 0, len(current)+len(s.values)+len(s.overrides))
	seen := make(map[string]bool, len(current))
	for _, entry := range current {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			env = append(env, entry)
			continue
		}
		seen[key] = true
		value, hasValue := s.values[key]
		if override, ok := s.overrides[key]; ok {
			value, hasValue = override, true
		}
		if hasValue {
			env = append(env, key+"="+value)
			continue
		}
		env = append(env, entry)
	}

	set := make(map[string]bool, len(s.values)+len(s.overrides))
	for key := range s.values {
		set[key] = true
	}
	for key := range s.overrides {
		set[key] = true
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if override, ok := s.overrides[key]; ok {
			env = append(env, key+"="+override)
			continue
		}
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

// Overrides returns the CLI-provided values.
func (s *Secrets) Overrides() map[string]string {
	if s == nil {
		return nil
	}
	return clone(s.overrides)
}

func clone(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
