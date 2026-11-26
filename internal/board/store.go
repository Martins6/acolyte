package board

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/paths"
	_ "github.com/mattn/go-sqlite3"
)

// Store is one open global board database.
type Store struct {
	db   *sql.DB
	path string
	// Now overrides the clock in tests. Nil uses time.Now.
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// OpenDefault opens $ACOLYTE_HOME/board.sqlite.db.
func OpenDefault() (*Store, error) {
	path, err := paths.BoardDB()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// Open opens (and creates/migrates) the board database at path.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("board: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("board: create directory: %w", err)
	}
	if err := ensureFile(path); err != nil {
		return nil, err
	}

	// busy_timeout is connection-local; set it in the DSN so every pooled
	// connection gets it, not just the first one.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=5000&_txlock=immediate"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("board: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("board: ping %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("board: set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("board: set WAL mode: %w", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("board: stat %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("board: create %s: %w", path, err)
	}
	return file.Close()
}

// Path returns the database path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// DB returns the underlying database handle.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
