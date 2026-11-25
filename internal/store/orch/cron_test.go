package orch

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	return db
}

func migrateTestStore(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return New(db)
}

func TestMigrateV3AddsPendingTurnColumns(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, 2)`, module); err != nil {
		t.Fatalf("record v2: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO orch_cron_jobs
		(id, title, kind, spec, created_at) VALUES (?, ?, ?, ?, ?)`,
		"job_old", "legacy", "prompt", "@every 1h", now); err != nil {
		t.Fatalf("insert v2 job: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(v2) error: %v", err)
	}

	var versionValue int
	if err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, module).Scan(&versionValue); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if versionValue != version {
		t.Errorf("version = %d, want %d", versionValue, version)
	}

	// The pre-v3 row survives with the documented defaults.
	store := New(db)
	job, err := store.GetCronJobByID("job_old")
	if err != nil {
		t.Fatalf("GetCronJobByID() error: %v", err)
	}
	if job.Title != "legacy" {
		t.Errorf("title = %q, want legacy", job.Title)
	}
	turns, err := store.PendingCronTurns(time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want none for a migrated row", turns)
	}

	// Migrate is re-entrant.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() second run error: %v", err)
	}
}

func TestCronPendingTurnLifecycle(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.AddCronJob(CronJob{
		ID: "job_1", Title: "report", Kind: "prompt", Spec: "@every 1h", CreatedAt: now, Enabled: true,
	}); err != nil {
		t.Fatalf("AddCronJob() error: %v", err)
	}

	if err := store.SetCronJobPending("job_1", "ses_1", "msg_1", "turn", now); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}

	// Not yet past the grace boundary.
	if turns, err := store.PendingCronTurns(now.Add(-time.Minute), 10); err != nil || len(turns) != 0 {
		t.Fatalf("PendingCronTurns(before grace) = %+v, %v; want none", turns, err)
	}

	turns, err := store.PendingCronTurns(now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("pending turns = %+v, want one", turns)
	}
	got := turns[0]
	if got.JobID != "job_1" || got.SessionID != "ses_1" || got.MarkerID != "msg_1" || got.Kind != "turn" {
		t.Errorf("pending turn = %+v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(now) {
		t.Errorf("started at = %v, want %v", got.StartedAt, now)
	}

	// A second admission replaces the marker rather than queuing another.
	if err := store.SetCronJobPending("job_1", "ses_2", "msg_2", "routine", now.Add(time.Second)); err != nil {
		t.Fatalf("SetCronJobPending() overwrite error: %v", err)
	}
	turns, err = store.PendingCronTurns(now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 1 || turns[0].MarkerID != "msg_2" || turns[0].Kind != "routine" {
		t.Errorf("pending turns = %+v, want the latest admission only", turns)
	}

	if err := store.ClearCronJobPending("job_1"); err != nil {
		t.Fatalf("ClearCronJobPending() error: %v", err)
	}
	turns, err = store.PendingCronTurns(now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared", turns)
	}

	if err := store.SetCronJobPending("job_missing", "ses", "msg", "turn", now); err == nil {
		t.Error("SetCronJobPending(missing) error = nil, want error")
	}
}

func TestClearCronJobPendingIf(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.AddCronJob(CronJob{
		ID: "job_1", Title: "report", Kind: "prompt", Spec: "@every 1h", CreatedAt: now, Enabled: true,
	}); err != nil {
		t.Fatalf("AddCronJob() error: %v", err)
	}
	if err := store.SetCronJobPending("job_1", "ses_1", "msg_1", "turn", now); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}

	cleared, err := store.ClearCronJobPendingIf("job_1", "msg_stale")
	if err != nil {
		t.Fatalf("ClearCronJobPendingIf() error: %v", err)
	}
	if cleared {
		t.Error("stale generation cleared the pending state")
	}
	pending, ok, err := store.CronJobPending("job_1")
	if err != nil || !ok || pending.MarkerID != "msg_1" {
		t.Fatalf("pending = %+v, ok = %v, err = %v; want msg_1 intact", pending, ok, err)
	}

	cleared, err = store.ClearCronJobPendingIf("job_1", "msg_1")
	if err != nil {
		t.Fatalf("ClearCronJobPendingIf() error: %v", err)
	}
	if !cleared {
		t.Error("matching generation was not cleared")
	}
	if _, ok, err := store.CronJobPending("job_1"); err != nil || ok {
		t.Errorf("pending after clear = ok %v, err %v; want none", ok, err)
	}
}
