// Package registry stores the global identity map between agent names and
// workspace directories ($ACOLYTE_HOME/registry.sqlite.db).
package registry

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/mattn/go-sqlite3"
)

var (
	ErrNameTaken      = errors.New("registry: agent name already taken")
	ErrWorkspaceTaken = errors.New("registry: workspace already registered")
	ErrNotFound       = errors.New("registry: agent not found")
	ErrAmbiguous      = errors.New("registry: reference is ambiguous")
)

// Agent is one registered Acolyte agent.
type Agent struct {
	Name         string    `json:"name"`
	Workspace    string    `json:"workspace"`
	Description  string    `json:"description"`
	Engine       string    `json:"engine"`
	Enabled      bool      `json:"enabled"`
	RegisteredAt time.Time `json:"registered_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// Registry is the global agent registry.
type Registry struct {
	db   *sql.DB
	path string
}

// OpenDefault opens $ACOLYTE_HOME/registry.sqlite.db.
func OpenDefault() (*Registry, error) {
	path, err := paths.RegistryDB()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// Open opens (and creates/migrates) the registry at path.
func Open(path string) (*Registry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("registry: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("registry: create directory: %w", err)
	}
	if err := ensureFile(path); err != nil {
		return nil, err
	}

	// busy_timeout is connection-local; set it in the DSN so every pooled
	// connection gets it, not just the first one.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=5000"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("registry: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("registry: ping %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("registry: set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("registry: set WAL mode: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("registry: create schema: %w", err)
	}

	return &Registry{db: db, path: path}, nil
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("registry: stat %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("registry: create %s: %w", path, err)
	}
	return file.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS agents (
  name          TEXT PRIMARY KEY,
  workspace     TEXT NOT NULL UNIQUE,
  description   TEXT NOT NULL DEFAULT '',
  engine        TEXT NOT NULL DEFAULT 'opencode',
  enabled       INTEGER NOT NULL DEFAULT 1,
  registered_at DATETIME NOT NULL,
  last_seen_at  DATETIME NOT NULL
);`

// Path returns the registry database path.
func (r *Registry) Path() string { return r.path }

// Close closes the database.
func (r *Registry) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// Register inserts a new agent. It returns ErrNameTaken or ErrWorkspaceTaken
// when the identity is already in use.
func (r *Registry) Register(agent Agent) error {
	normalized, err := normalize(agent)
	if err != nil {
		return err
	}
	agent = normalized
	if agent.Name == "" {
		return fmt.Errorf("registry: agent name is required")
	}
	if agent.Workspace == "" {
		return fmt.Errorf("registry: agent workspace is required")
	}
	if agent.Engine == "" {
		agent.Engine = "opencode"
	}
	// Registration always enables the agent; Update is the way to disable it.
	agent.Enabled = true
	now := time.Now().UTC()
	if agent.RegisteredAt.IsZero() {
		agent.RegisteredAt = now
	}
	agent.LastSeenAt = now

	// Re-registering the exact same identity adopts it: refresh metadata
	// instead of failing on the unique constraint.
	if err := r.adoptExisting(agent); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	_, err = r.db.Exec(
		`INSERT INTO agents (name, workspace, description, engine, enabled, registered_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		agent.Name, agent.Workspace, agent.Description, agent.Engine, boolInt(agent.Enabled),
		agent.RegisteredAt, agent.LastSeenAt,
	)
	if err == nil {
		return nil
	}
	if isUnique(err) {
		// Another process may have inserted the same identity between the
		// pre-check and the insert; adopt it if it is an exact match.
		if adoptErr := r.adoptExisting(agent); adoptErr == nil {
			return nil
		} else if !errors.Is(adoptErr, ErrNotFound) {
			return adoptErr
		}
		return r.uniqueConflict(agent.Name, agent.Workspace)
	}
	return fmt.Errorf("registry: register %q: %w", agent.Name, err)
}

func (r *Registry) adoptExisting(agent Agent) error {
	existing, err := r.GetByName(agent.Name)
	if err == nil {
		if existing.Workspace != agent.Workspace {
			return ErrNameTaken
		}
		return r.Update(agent)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	existing, err = r.GetByWorkspace(agent.Workspace)
	if err == nil {
		if existing.Name != agent.Name {
			return ErrWorkspaceTaken
		}
		return r.Update(agent)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return ErrNotFound
}

// Update refreshes mutable fields for an existing agent, looked up by the full
// name + workspace identity so a concurrent replacement cannot be mutated.
func (r *Registry) Update(agent Agent) error {
	normalized, err := normalize(agent)
	if err != nil {
		return err
	}
	agent = normalized
	if agent.Name == "" {
		return fmt.Errorf("registry: agent name is required")
	}
	if agent.Workspace == "" {
		return fmt.Errorf("registry: agent workspace is required")
	}
	if agent.Engine == "" {
		agent.Engine = "opencode"
	}
	now := time.Now().UTC()

	result, err := r.db.Exec(
		`UPDATE agents SET description = ?, engine = ?, enabled = ?, last_seen_at = ?
		 WHERE name = ? AND workspace = ?`,
		agent.Description, agent.Engine, boolInt(agent.Enabled), now,
		agent.Name, agent.Workspace,
	)
	if err != nil {
		return fmt.Errorf("registry: update %q: %w", agent.Name, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("registry: update %q: %w", agent.Name, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByName returns one agent by name.
func (r *Registry) GetByName(name string) (Agent, error) {
	return r.get(`SELECT name, workspace, description, engine, enabled, registered_at, last_seen_at
		FROM agents WHERE name = ?`, strings.TrimSpace(name))
}

// GetByWorkspace returns one agent by absolute workspace path.
func (r *Registry) GetByWorkspace(workspace string) (Agent, error) {
	if strings.TrimSpace(workspace) == "" {
		return Agent{}, ErrNotFound
	}
	canonical, err := paths.Canonical(workspace)
	if err != nil {
		return Agent{}, fmt.Errorf("registry: resolve workspace %q: %w", workspace, err)
	}
	return r.get(`SELECT name, workspace, description, engine, enabled, registered_at, last_seen_at
		FROM agents WHERE workspace = ?`, canonical)
}

func (r *Registry) get(query string, arg string) (Agent, error) {
	var agent Agent
	var enabled int
	err := r.db.QueryRow(query, arg).Scan(
		&agent.Name, &agent.Workspace, &agent.Description, &agent.Engine, &enabled,
		&agent.RegisteredAt, &agent.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("registry: read agent: %w", err)
	}
	agent.Enabled = enabled != 0
	return agent, nil
}

// Resolve finds an agent by name first, then by absolute workspace path.
func (r *Registry) Resolve(ref string) (Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Agent{}, ErrNotFound
	}

	agent, err := r.GetByName(ref)
	if err == nil {
		return agent, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Agent{}, err
	}

	agent, err = r.GetByWorkspace(ref)
	if err == nil {
		return agent, nil
	}
	if errors.Is(err, ErrNotFound) {
		return Agent{}, ErrNotFound
	}
	return Agent{}, err
}

// List returns every agent ordered by name.
func (r *Registry) List() ([]Agent, error) {
	rows, err := r.db.Query(`SELECT name, workspace, description, engine, enabled, registered_at, last_seen_at
		FROM agents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("registry: list agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var agent Agent
		var enabled int
		if err := rows.Scan(
			&agent.Name, &agent.Workspace, &agent.Description, &agent.Engine, &enabled,
			&agent.RegisteredAt, &agent.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("registry: scan agent: %w", err)
		}
		agent.Enabled = enabled != 0
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: list agents: %w", err)
	}
	return agents, nil
}

// Delete removes an agent from the registry by name or workspace. The
// workspace folder is never touched.
func (r *Registry) Delete(ref string) (Agent, error) {
	agent, err := r.Resolve(ref)
	if err != nil {
		return Agent{}, err
	}

	// Delete the exact resolved identity, not just the name: another process
	// could have replaced the name with a different workspace in between.
	result, err := r.db.Exec(`DELETE FROM agents WHERE name = ? AND workspace = ?`, agent.Name, agent.Workspace)
	if err != nil {
		return Agent{}, fmt.Errorf("registry: delete %q: %w", agent.Name, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Agent{}, fmt.Errorf("registry: delete %q: %w", agent.Name, err)
	}
	if affected == 0 {
		return Agent{}, ErrNotFound
	}
	return agent, nil
}

func normalize(agent Agent) (Agent, error) {
	agent.Name = strings.TrimSpace(agent.Name)
	if strings.TrimSpace(agent.Workspace) != "" {
		canonical, err := paths.Canonical(agent.Workspace)
		if err != nil {
			return Agent{}, fmt.Errorf("registry: resolve workspace %q: %w", agent.Workspace, err)
		}
		agent.Workspace = canonical
	} else {
		agent.Workspace = ""
	}
	agent.Description = strings.TrimSpace(agent.Description)
	agent.Engine = strings.TrimSpace(agent.Engine)
	return agent, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique ||
		sqliteErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey
}

func (r *Registry) uniqueConflict(name, workspace string) error {
	if _, err := r.GetByName(name); err == nil {
		return ErrNameTaken
	}
	if _, err := r.GetByWorkspace(workspace); err == nil {
		return ErrWorkspaceTaken
	}
	return ErrNameTaken
}
