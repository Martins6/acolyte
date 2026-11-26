package board

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer st.Close()

	tables := []string{
		"brd_posts",
		"brd_entries",
		"brd_files",
		"brd_participants",
		"brd_tags",
		"brd_reads",
		"brd_subscriptions",
		"brd_mutes",
		"brd_automations",
		"brd_inbox",
		"brd_alerts",
	}
	for _, table := range tables {
		var name string
		err := st.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %s is missing", table)
			continue
		}
		if err != nil {
			t.Errorf("look up table %s: %v", table, err)
		}
	}

	var version int
	if err := st.DB().QueryRow(
		`SELECT version FROM schema_meta WHERE module = 'brd'`,
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	var journal string
	if err := st.DB().QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal mode = %q, want wal", journal)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	defer st.Close()

	var version int
	if err := st.DB().QueryRow(
		`SELECT version FROM schema_meta WHERE module = 'brd'`,
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}
}

func TestOpenDefaultUsesACOLYTEHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ACOLYTE_HOME", home)

	st, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault() error: %v", err)
	}
	defer st.Close()

	want := filepath.Join(home, "board.sqlite.db")
	if st.Path() != want {
		t.Errorf("path = %q, want %q", st.Path(), want)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET version = 2 WHERE module = 'brd'`); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	st, err = Open(path)
	if err == nil {
		st.Close()
		t.Fatal("Open() succeeded, want newer-schema error")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("Open() error = %v, want newer-schema error", err)
	}
}

func TestOpenCreatesPrivateDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer st.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("database mode = %o, want 600", info.Mode().Perm())
	}
}
