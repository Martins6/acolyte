// Package store opens the per-agent SQLite database and runs the module
// migrations. The database lives at <workspace>/.acolyte.sqlite.db.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
	_ "github.com/mattn/go-sqlite3"
)

// ErrNoStore means the workspace has no per-agent database yet.
var ErrNoStore = errors.New("store: database not found")

// Store is one open per-agent database.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open opens (and creates/migrates) <workspace>/.acolyte.sqlite.db. The
// workspace directory must already exist.
func Open(workspace string) (*Store, error) {
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("store: workspace %s does not exist", workspace)
		}
		return nil, fmt.Errorf("store: stat workspace %s: %w", workspace, err)
	}

	path := paths.WorkspaceDB(workspace)
	if err := ensureFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := prepare(db, path); err != nil {
		db.Close()
		return nil, err
	}
	if err := orch.Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := commtg.Migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db, path: path}, nil
}

// OpenReadOnly opens an existing database without creating or migrating it.
// It returns ErrNoStore when the file does not exist.
func OpenReadOnly(workspace string) (*Store, error) {
	path := paths.WorkspaceDB(workspace)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoStore
		}
		return nil, fmt.Errorf("store: stat %s: %w", path, err)
	}

	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	if err := prepareReadOnly(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path, readOnly: true}, nil
}

func prepare(db *sql.DB, path string) error {
	if err := prepareReadOnly(db, path); err != nil {
		return err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("store: set WAL mode: %w", err)
	}
	return nil
}

func prepareReadOnly(db *sql.DB, path string) error {
	if err := db.Ping(); err != nil {
		return fmt.Errorf("store: ping %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("store: set busy timeout: %w", err)
	}
	return nil
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("store: stat %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	return file.Close()
}

// DB returns the underlying database handle.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// ReadOnly reports whether the store was opened read-only.
func (s *Store) ReadOnly() bool { return s.readOnly }

// Orch returns the orchestrator module store.
func (s *Store) Orch() *orch.Store { return orch.New(s.db) }

// CommTG returns the Telegram communication module store.
func (s *Store) CommTG() *commtg.Store { return commtg.New(s.db) }

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
