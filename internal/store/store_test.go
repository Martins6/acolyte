package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/store/orch"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ws
}

func TestOpenCreatesSchema(t *testing.T) {
	s, ws := openTest(t)

	info, err := os.Stat(filepath.Join(ws, ".acolyte.sqlite.db"))
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("db mode = %o, want 0600", perm)
	}

	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		got[name] = true
	}
	for _, want := range []string{
		"schema_meta", "orch_sessions", "orch_cron_jobs", "orch_logs",
		"orch_settings", "comm_tg_settings", "comm_tg_chats", "comm_tg_inbox",
		"comm_tg_outbox", "comm_tg_media",
	} {
		if !got[want] {
			t.Errorf("missing table %q", want)
		}
	}

	var orchVersion, commVersion int
	if err := s.DB().QueryRow(`SELECT version FROM schema_meta WHERE module = 'orch'`).Scan(&orchVersion); err != nil {
		t.Fatalf("read orch version: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT version FROM schema_meta WHERE module = 'comm_tg'`).Scan(&commVersion); err != nil {
		t.Fatalf("read comm_tg version: %v", err)
	}
	if orchVersion != 1 || commVersion != 6 {
		t.Errorf("versions = orch:%d comm_tg:%d, want 1/6", orchVersion, commVersion)
	}
}

func TestOpenMissingWorkspace(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("Open(missing) error = nil, want error")
	}
}

func TestOpenIdempotent(t *testing.T) {
	ws := t.TempDir()
	first, err := Open(ws)
	if err != nil {
		t.Fatalf("Open(first) error: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error: %v", err)
	}

	second, err := Open(ws)
	if err != nil {
		t.Fatalf("Open(second) error: %v", err)
	}
	defer second.Close()
}

func TestOpenRejectsSymlinkedWorkspace(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Open(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Open(symlinked workspace) error = %v, want symlink error", err)
	}
	if _, err := OpenReadOnly(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("OpenReadOnly(symlinked workspace) error = %v, want symlink error", err)
	}
}

func TestOpenRejectsSymlink(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.db")
	if err := os.Symlink(target, filepath.Join(ws, ".acolyte.sqlite.db")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Open(ws); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Open() error = %v, want symlink error", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target was created: %v", err)
	}
}

func TestOpenReadOnlyRejectsSymlink(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.db")
	if err := os.Symlink(target, filepath.Join(ws, ".acolyte.sqlite.db")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := OpenReadOnly(ws); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("OpenReadOnly() error = %v, want symlink error", err)
	}
}

func TestOpenRejectsNonRegular(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".acolyte.sqlite.db"), 0755); err != nil {
		t.Fatalf("mkdir db path: %v", err)
	}
	if _, err := Open(ws); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Open() error = %v, want non-regular error", err)
	}
}

func TestOpenRejectsSymlinkedSidecar(t *testing.T) {
	s, ws := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	target := filepath.Join(t.TempDir(), "outside-wal")
	if err := os.Symlink(target, filepath.Join(ws, ".acolyte.sqlite.db-wal")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Open(ws); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Open() error = %v, want sidecar symlink error", err)
	}
}

func TestOpenRejectsDuplicate(t *testing.T) {
	s, ws := openTest(t)
	if _, err := Open(ws); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("Open(duplicate) error = %v, want already-open error", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	reopened, err := Open(ws)
	if err != nil {
		t.Fatalf("Open(reopen) error: %v", err)
	}
	defer reopened.Close()
}

func TestOpenReadOnlyRejectsDuplicate(t *testing.T) {
	s, ws := openTest(t)
	if _, err := OpenReadOnly(ws); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("OpenReadOnly(duplicate) error = %v, want already-open error", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	ro, err := OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("OpenReadOnly() error: %v", err)
	}
	defer ro.Close()
}

func TestOpenRejectsSymlinkedJournal(t *testing.T) {
	s, ws := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	target := filepath.Join(t.TempDir(), "outside-journal")
	if err := os.Symlink(target, filepath.Join(ws, ".acolyte.sqlite.db-journal")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Open(ws); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Open() error = %v, want journal symlink error", err)
	}
}

func TestOpenReadOnly(t *testing.T) {
	s, ws := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	ro, err := OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("OpenReadOnly() error: %v", err)
	}
	defer ro.Close()
	if !ro.ReadOnly() {
		t.Error("ReadOnly() = false, want true")
	}
}

func TestOpenReadOnlyMissing(t *testing.T) {
	_, err := OpenReadOnly(t.TempDir())
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("OpenReadOnly(missing) error = %v, want ErrNoStore", err)
	}
}

func TestCronJobs(t *testing.T) {
	s, _ := openTest(t)
	store := s.Orch()
	next := time.Now().Add(time.Hour).UTC()

	job := orch.CronJob{
		Title:       "heartbeat",
		Owner:       "heartbeat",
		Kind:        "routine",
		Spec:        "@every 1h",
		Routine:     "heartbeat",
		RoutineArgs: `{"interval":60}`,
		Enabled:     true,
		NextRunAt:   &next,
	}
	inserted, err := store.AddCronJobIfMissing(job)
	if err != nil {
		t.Fatalf("AddCronJobIfMissing() error: %v", err)
	}
	if !inserted {
		t.Fatal("AddCronJobIfMissing() inserted = false, want true")
	}

	inserted, err = store.AddCronJobIfMissing(job)
	if err != nil {
		t.Fatalf("AddCronJobIfMissing(duplicate) error: %v", err)
	}
	if inserted {
		t.Fatal("AddCronJobIfMissing(duplicate) inserted = true, want false")
	}

	got, err := store.GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if got.Kind != "routine" || got.Routine != "heartbeat" || !got.Enabled {
		t.Errorf("job = %+v, want routine/heartbeat/enabled", got)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Errorf("next run = %v, want %v", got.NextRunAt, next)
	}

	jobs, err := store.ListCronJobs()
	if err != nil {
		t.Fatalf("ListCronJobs() error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}

	stats, err := store.Stats()
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.JobCount != 1 {
		t.Errorf("job count = %d, want 1", stats.JobCount)
	}
	if stats.NextJobAt == nil || !stats.NextJobAt.Equal(next) {
		t.Errorf("next job = %v, want %v", stats.NextJobAt, next)
	}
	if stats.LastActiveAt != nil {
		t.Errorf("last active = %v, want nil", stats.LastActiveAt)
	}
}

func TestGetCronJobMissing(t *testing.T) {
	s, _ := openTest(t)
	_, err := s.Orch().GetCronJobByTitle("missing")
	if !errors.Is(err, orch.ErrJobNotFound) {
		t.Fatalf("GetCronJobByTitle(missing) error = %v, want ErrJobNotFound", err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := openTest(t)

	if err := s.Orch().SetSetting("schedule.timezone", "America/Sao_Paulo"); err != nil {
		t.Fatalf("SetSetting() error: %v", err)
	}
	value, ok, err := s.Orch().GetSetting("schedule.timezone")
	if err != nil {
		t.Fatalf("GetSetting() error: %v", err)
	}
	if !ok || value != "America/Sao_Paulo" {
		t.Errorf("GetSetting() = %q, %v; want America/Sao_Paulo, true", value, ok)
	}

	if err := s.Orch().SetSetting("schedule.timezone", "UTC"); err != nil {
		t.Fatalf("SetSetting(update) error: %v", err)
	}
	value, _, err = s.Orch().GetSetting("schedule.timezone")
	if err != nil {
		t.Fatalf("GetSetting(update) error: %v", err)
	}
	if value != "UTC" {
		t.Errorf("GetSetting(update) = %q, want UTC", value)
	}

	if _, ok, err := s.Orch().GetSetting("missing"); err != nil || ok {
		t.Errorf("GetSetting(missing) = _, %v, %v; want _, false, nil", ok, err)
	}
}

func TestCommSettings(t *testing.T) {
	s, _ := openTest(t)
	if err := s.CommTG().SetSetting("telegram.allowed_users", `["@you"]`); err != nil {
		t.Fatalf("SetSetting() error: %v", err)
	}
	value, ok, err := s.CommTG().GetSetting("telegram.allowed_users")
	if err != nil {
		t.Fatalf("GetSetting() error: %v", err)
	}
	if !ok || value != `["@you"]` {
		t.Errorf("GetSetting() = %q, %v; want [\"@you\"], true", value, ok)
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	s, ws := openTest(t)
	if _, err := s.DB().Exec(`UPDATE schema_meta SET version = 99 WHERE module = 'orch'`); err != nil {
		t.Fatalf("bump schema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if _, err := Open(ws); err == nil {
		t.Fatal("Open(newer schema) error = nil, want error")
	}
}
