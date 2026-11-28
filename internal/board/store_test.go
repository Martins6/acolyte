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
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
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
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
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

func TestMigrateAddsBindingsAndTurnReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (
		module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES ('brd', 1)`); err != nil {
		t.Fatalf("record v1: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v1: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v1) error: %v", err)
	}
	defer st.Close()

	columns := map[string]bool{}
	rows, err := st.DB().Query(`PRAGMA table_info(brd_inbox)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	rows.Close()
	if !columns["turn_reserved_at"] {
		t.Error("brd_inbox.turn_reserved_at was not added")
	}
	var table string
	if err := st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'brd_session_bindings'`,
	).Scan(&table); err != nil {
		t.Errorf("brd_session_bindings is missing: %v", err)
	}
}

func TestMigrateToleratesExistingTurnReservationColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (
		module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE brd_inbox ADD COLUMN turn_reserved_at DATETIME`); err != nil {
		t.Fatalf("pre-add column: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES ('brd', 1)`); err != nil {
		t.Fatalf("record v1: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v1: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v1 with column) error: %v", err)
	}
	defer st.Close()
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.sqlite.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET version = 3 WHERE module = 'brd'`); err != nil {
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
