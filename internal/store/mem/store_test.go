package mem

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestRecordDream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	store := New(db)
	ranAt := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordDream(Dream{
		RanAt: ranAt, SessionsScanned: 3, SkillsCreated: 1, DryRun: true, Note: "proposal",
	}); err != nil {
		t.Fatalf("RecordDream() error: %v", err)
	}
	var id, note string
	var scanned, created, dryRun int
	var storedAt time.Time
	if err := db.QueryRow(`SELECT id, ran_at, sessions_scanned, skills_created, dry_run, note FROM mem_dreams`).Scan(
		&id, &storedAt, &scanned, &created, &dryRun, &note,
	); err != nil {
		t.Fatalf("query dream: %v", err)
	}
	if id == "" || scanned != 3 || created != 1 || dryRun != 1 || note != "proposal" {
		t.Errorf("row = id:%q scanned:%d created:%d dry:%d note:%q", id, scanned, created, dryRun, note)
	}
	if !storedAt.Equal(ranAt) {
		t.Errorf("ran_at = %v, want %v", storedAt, ranAt)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(second) error: %v", err)
	}
}
