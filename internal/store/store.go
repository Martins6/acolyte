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
	"sync"

	"github.com/martins6/acolyte/internal/fdverify"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
	sqlite3 "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

// ErrNoStore means the workspace has no per-agent database yet.
var ErrNoStore = errors.New("store: database not found")

// openStores guarantees one store per database path in this process, which
// makes the per-connection fd verification unambiguous.
var openStores sync.Map

// Store is one open per-agent database.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open opens (and creates/migrates) <workspace>/.acolyte.sqlite.db. The
// workspace directory must already exist.
func Open(workspace string) (*Store, error) {
	path, pin, before, err := openPinnedDB(workspace, true)
	if err != nil {
		return nil, err
	}
	defer pin.Close()

	release, err := claim(path)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			release()
		}
	}()

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
	if err := orch.Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := commtg.Migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	success = true
	return &Store{db: db, path: path}, nil
}

// OpenReadOnly opens an existing database without creating or migrating it.
// It returns ErrNoStore when the file does not exist.
func OpenReadOnly(workspace string) (*Store, error) {
	path, pin, before, err := openPinnedDB(workspace, false)
	if err != nil {
		return nil, err
	}
	defer pin.Close()

	release, err := claim(path)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			release()
		}
	}()

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
	success = true
	return &Store{db: db, path: path, readOnly: true}, nil
}

// openPinnedDB opens the workspace directory with O_NOFOLLOW, opens the
// database relative to that directory fd, and returns the pinned file info.
// SQLite later opens the same pathname; VerifyConn compares SQLite's actual
// descriptor with this pinned inode.
func openPinnedDB(workspace string, write bool) (string, *os.File, os.FileInfo, error) {
	info, err := os.Lstat(workspace)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil, fmt.Errorf("store: workspace %s does not exist", workspace)
		}
		return "", nil, nil, fmt.Errorf("store: stat workspace %s: %w", workspace, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, nil, fmt.Errorf("store: refusing symlinked workspace %s", workspace)
	}
	if !info.IsDir() {
		return "", nil, nil, fmt.Errorf("store: workspace %s is not a directory", workspace)
	}

	dirFD, err := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, nil, fmt.Errorf("store: open workspace %s: %w", workspace, err)
	}
	defer unix.Close(dirFD)

	const name = ".acolyte.sqlite.db"
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			return "", nil, nil, fmt.Errorf("store: refusing symlink %s", paths.WorkspaceDB(workspace))
		case unix.S_IFREG:
		default:
			return "", nil, nil, fmt.Errorf("store: %s is not a regular file", paths.WorkspaceDB(workspace))
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return "", nil, nil, fmt.Errorf("store: stat database: %w", err)
	}

	flags := unix.O_RDONLY
	if write {
		flags = unix.O_RDWR | unix.O_CREAT
	}
	dbFD, err := unix.Openat(dirFD, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		if !write && errors.Is(err, unix.ENOENT) {
			return "", nil, nil, ErrNoStore
		}
		return "", nil, nil, fmt.Errorf("store: open database: %w", err)
	}
	pin := os.NewFile(uintptr(dbFD), name)
	pinInfo, err := pin.Stat()
	if err != nil {
		pin.Close()
		return "", nil, nil, fmt.Errorf("store: stat database: %w", err)
	}
	return paths.WorkspaceDB(workspace), pin, pinInfo, nil
}

func claim(path string) (func(), error) {
	if _, loaded := openStores.LoadOrStore(path, struct{}{}); loaded {
		return nil, fmt.Errorf("store: %s is already open in this process", path)
	}
	return func() { openStores.Delete(path) }, nil
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
	if err := fdverify.VerifyConn(conn, c.path, c.before); err != nil {
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

func regularFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("store: refusing symlink %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("store: %s is not a regular file", path)
	}
	return nil
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
	err := s.db.Close()
	openStores.Delete(s.path)
	return err
}
