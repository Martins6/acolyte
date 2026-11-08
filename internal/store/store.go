// Package store opens the per-agent SQLite database and runs the module
// migrations. The database lives at <workspace>/.acolyte.sqlite.db.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/martins6/acolyte/internal/fdverify"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
	sqlite3 "github.com/mattn/go-sqlite3"
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
	before, err := ensureFile(path)
	if err != nil {
		return nil, err
	}
	if err := checkSidecars(path); err != nil {
		return nil, err
	}

	db := sql.OpenDB(verifyingConnector{dsn: fileDSN(path, ""), path: path, before: before})
	// A single connection keeps fd verification unambiguous; SQLite serializes
	// writes anyway, and the DSN busy timeout covers cross-process contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := prepare(db, path); err != nil {
		db.Close()
		return nil, err
	}
	if err := verifyFile(path, before); err != nil {
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
	before, err := inspectFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoStore
		}
		return nil, err
	}
	if err := checkSidecars(path); err != nil {
		return nil, err
	}

	db := sql.OpenDB(verifyingConnector{dsn: fileDSN(path, "mode=ro"), path: path, before: before})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := prepareReadOnly(db, path); err != nil {
		db.Close()
		return nil, err
	}
	if err := verifyFile(path, before); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path, readOnly: true}, nil
}

// fileDSN builds a SQLite URI with the connection-local busy timeout applied
// to every pooled connection.
func fileDSN(path, extra string) string {
	values := url.Values{}
	values.Set("_busy_timeout", "5000")
	for _, pair := range strings.Split(extra, "&") {
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			values.Set(key, value)
		}
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
}

// verifyingConnector opens SQLite and then pins the file descriptor it
// actually opened. Stores are limited to one connection, so the scan cannot
// be satisfied by an older connection from this pool. SQLite's unix VFS also
// opens the main DB, WAL, journal, and SHM with O_NOFOLLOW.
type verifyingConnector struct {
	dsn    string
	path   string
	before os.FileInfo
}

func (c verifyingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if err := fdverify.VerifyOpen(c.path, c.before); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (c verifyingConnector) Driver() driver.Driver {
	return &sqlite3.SQLiteDriver{}
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

func ensureFile(path string) (os.FileInfo, error) {
	if info, err := os.Lstat(path); err == nil {
		if err := regularFile(path, info); err != nil {
			return nil, err
		}
		return info, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("store: stat %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("store: create %s: %w", path, err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, fmt.Errorf("store: stat created %s: %w", path, statErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("store: close created %s: %w", path, closeErr)
	}
	return info, nil
}

func inspectFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("store: stat %s: %w", path, err)
	}
	if err := regularFile(path, info); err != nil {
		return nil, err
	}
	return info, nil
}

func regularFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("store: refusing symlink %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("store: %s is not a regular file", path)
	}
	return nil
}

// verifyFile catches a path swap between the Lstat and SQLite opening the file.
func verifyFile(path string, before os.FileInfo) error {
	after, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("store: stat %s after open: %w", path, err)
	}
	if err := regularFile(path, after); err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("store: %s changed while opening", path)
	}
	return checkSidecars(path)
}

// checkSidecars rejects symlinked WAL/SHM files that SQLite would otherwise
// follow when it creates or opens the sidecars.
func checkSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		info, err := os.Lstat(sidecar)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("store: stat %s: %w", sidecar, err)
		}
		if err := regularFile(sidecar, info); err != nil {
			return err
		}
	}
	return nil
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
