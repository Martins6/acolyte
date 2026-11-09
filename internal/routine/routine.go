// Package routine defines named, headless behaviors that cron jobs can run.
package routine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

// ArgSpec describes one routine argument.
type ArgSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// CatalogEntry is the public routine-list projection.
type CatalogEntry struct {
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`
	Description string    `json:"description"`
	Args        []ArgSpec `json:"args"`
}

// Env is the execution context handed to a routine.
type Env struct {
	Agent      registry.Agent
	Workspace  string
	SessionID  string
	Store      *store.Store
	OpenCode   *opencode.Client
	NotifyUser func(ctx context.Context, text string) error
	Inbox      func(ctx context.Context, text string, meta map[string]any) (string, string, error)
	Log        func(level, message string, meta map[string]any)
}

// Routine is one named headless behavior.
type Routine interface {
	Name() string
	Owner() string
	Description() string
	Args() []ArgSpec
	Run(ctx context.Context, env Env, args map[string]any) (string, error)
}

// Registry stores routines by name.
type Registry struct {
	mu       sync.RWMutex
	routines map[string]Routine
}

// NewRegistry returns an empty routine registry.
func NewRegistry() *Registry {
	return &Registry{routines: map[string]Routine{}}
}

// Register adds a routine. Names are case-sensitive and unique.
func (r *Registry) Register(candidate Routine) error {
	if candidate == nil {
		return fmt.Errorf("routine: nil routine")
	}
	name := strings.TrimSpace(candidate.Name())
	if name == "" {
		return fmt.Errorf("routine: routine name is required")
	}
	if strings.TrimSpace(candidate.Owner()) == "" {
		return fmt.Errorf("routine: %s owner is required", name)
	}
	seenArgs := make(map[string]bool)
	for _, arg := range candidate.Args() {
		argName := strings.TrimSpace(arg.Name)
		if argName == "" {
			return fmt.Errorf("routine: %s has an unnamed argument", name)
		}
		if seenArgs[argName] {
			return fmt.Errorf("routine: %s has duplicate argument %q", name, argName)
		}
		seenArgs[argName] = true
		switch arg.Type {
		case "int", "string", "bool":
		default:
			return fmt.Errorf("routine: %s arg %q has unsupported type %q", name, argName, arg.Type)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.routines[name]; exists {
		return fmt.Errorf("routine: %s already registered", name)
	}
	r.routines[name] = candidate
	return nil
}

// Get returns a routine by name.
func (r *Registry) Get(name string) (Routine, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.routines[strings.TrimSpace(name)]
	return value, ok
}

// List returns routines sorted by name.
func (r *Registry) List() []Routine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.routines))
	for name := range r.routines {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Routine, 0, len(names))
	for _, name := range names {
		out = append(out, r.routines[name])
	}
	return out
}

// Catalog returns the routine-list JSON projection.
func (r *Registry) Catalog() []CatalogEntry {
	list := r.List()
	out := make([]CatalogEntry, 0, len(list))
	for _, value := range list {
		out = append(out, CatalogEntry{
			Name:        value.Name(),
			Owner:       value.Owner(),
			Description: value.Description(),
			Args:        append([]ArgSpec(nil), value.Args()...),
		})
	}
	return out
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default returns the process-wide registry with the v0 builtins registered.
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultReg = NewRegistry()
		_ = defaultReg.Register(dreamRoutine{})
		_ = defaultReg.Register(heartbeatRoutine{})
	})
	return defaultReg
}
