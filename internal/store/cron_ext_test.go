package store

import (
	"errors"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/store/orch"
)

func timePtr(t time.Time) *time.Time { return &t }

func TestAddCronJobStrictDuplicate(t *testing.T) {
	s, _ := openTest(t)
	job := orch.CronJob{Title: "hello", Kind: "shell", Spec: "@every 1h", Enabled: true}
	if err := s.Orch().AddCronJob(job); err != nil {
		t.Fatalf("AddCronJob() error: %v", err)
	}
	if err := s.Orch().AddCronJob(job); !errors.Is(err, orch.ErrJobExists) {
		t.Fatalf("AddCronJob(duplicate) error = %v, want ErrJobExists", err)
	}

	inserted, err := s.Orch().AddCronJobIfMissing(orch.CronJob{
		Title: "hello", Kind: "shell", Spec: "@every 2h", Enabled: true,
	})
	if err != nil {
		t.Fatalf("AddCronJobIfMissing() error: %v", err)
	}
	if inserted {
		t.Fatal("AddCronJobIfMissing(duplicate) inserted = true, want false")
	}
}

func TestDeleteAndEnableCronJob(t *testing.T) {
	s, _ := openTest(t)
	job := orch.CronJob{Title: "hello", Kind: "shell", Spec: "@every 1h", Enabled: true}
	if err := s.Orch().AddCronJob(job); err != nil {
		t.Fatalf("AddCronJob() error: %v", err)
	}

	if err := s.Orch().SetCronJobEnabled("hello", false); err != nil {
		t.Fatalf("SetCronJobEnabled() error: %v", err)
	}
	got, err := s.Orch().GetCronJobByTitle("hello")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if got.Enabled {
		t.Error("job enabled = true, want false")
	}

	if err := s.Orch().DeleteCronJob("hello"); err != nil {
		t.Fatalf("DeleteCronJob() error: %v", err)
	}
	if _, err := s.Orch().GetCronJobByTitle("hello"); !errors.Is(err, orch.ErrJobNotFound) {
		t.Fatalf("GetCronJobByTitle() error = %v, want ErrJobNotFound", err)
	}
	if err := s.Orch().DeleteCronJob("hello"); !errors.Is(err, orch.ErrJobNotFound) {
		t.Fatalf("DeleteCronJob(missing) error = %v, want ErrJobNotFound", err)
	}
	if err := s.Orch().SetCronJobEnabled("missing", true); !errors.Is(err, orch.ErrJobNotFound) {
		t.Fatalf("SetCronJobEnabled(missing) error = %v, want ErrJobNotFound", err)
	}
}

func TestDueCronJobsAndClaim(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	due := orch.CronJob{Title: "due", Kind: "shell", Spec: "@every 1h", Enabled: true, NextRunAt: &past}
	futureJob := orch.CronJob{Title: "future", Kind: "shell", Spec: "@every 1h", Enabled: true, NextRunAt: &future}
	disabled := orch.CronJob{Title: "disabled", Kind: "shell", Spec: "@every 1h", Enabled: false, NextRunAt: &past}
	for _, job := range []orch.CronJob{due, futureJob, disabled} {
		if err := s.Orch().AddCronJob(job); err != nil {
			t.Fatalf("AddCronJob(%s) error: %v", job.Title, err)
		}
	}

	jobs, err := s.Orch().DueCronJobs(now)
	if err != nil {
		t.Fatalf("DueCronJobs() error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Title != "due" {
		t.Fatalf("due jobs = %+v, want due only", jobs)
	}

	next := now.Add(time.Hour)
	claimed, err := s.Orch().ClaimCronJob(jobs[0].ID, jobs[0].NextRunAt, next)
	if err != nil {
		t.Fatalf("ClaimCronJob() error: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimCronJob() = false, want true")
	}
	claimed, err = s.Orch().ClaimCronJob(jobs[0].ID, jobs[0].NextRunAt, next.Add(time.Hour))
	if err != nil {
		t.Fatalf("ClaimCronJob(second) error: %v", err)
	}
	if claimed {
		t.Fatal("ClaimCronJob(second) = true, want false")
	}

	updated, err := s.Orch().GetCronJobByTitle("due")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(next) {
		t.Errorf("next run = %v, want %v", updated.NextRunAt, next)
	}

	if err := s.Orch().MarkCronJobRun(updated.ID, now); err != nil {
		t.Fatalf("MarkCronJobRun() error: %v", err)
	}
	updated, err = s.Orch().GetCronJobByTitle("due")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Errorf("last run = %v, want %v", updated.LastRunAt, now)
	}
}

func TestSessions(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now().UTC()

	created, err := s.Orch().UpsertSession("cron:one", "ses_1", now)
	if err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	if created.Thread != "cron:one" || created.OpenCodeSessionID != "ses_1" {
		t.Errorf("session = %+v", created)
	}

	updated, err := s.Orch().UpsertSession("cron:one", "ses_2", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("UpsertSession(update) error: %v", err)
	}
	if updated.ID != created.ID || updated.OpenCodeSessionID != "ses_2" {
		t.Errorf("updated session = %+v, want same id and ses_2", updated)
	}

	latest, ok, err := s.Orch().LatestSession()
	if err != nil || !ok {
		t.Fatalf("LatestSession() = %+v, %v, %v", latest, ok, err)
	}
	if latest.OpenCodeSessionID != "ses_2" {
		t.Errorf("latest = %+v, want ses_2", latest)
	}

	if err := s.Orch().TouchSession("cron:one", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("TouchSession() error: %v", err)
	}
	if err := s.Orch().TouchSession("missing", now); !errors.Is(err, orch.ErrSessionNotFound) {
		t.Fatalf("TouchSession(missing) error = %v, want ErrSessionNotFound", err)
	}
	if _, ok, err := s.Orch().GetSessionByThread("missing"); err != nil || ok {
		t.Errorf("GetSessionByThread(missing) = ok:%v err:%v, want false/nil", ok, err)
	}
	found, ok, err := s.Orch().GetSessionByOpenCodeID("ses_2")
	if err != nil || !ok || found.Thread != "cron:one" {
		t.Errorf("GetSessionByOpenCodeID(ses_2) = %+v, %v, %v", found, ok, err)
	}
	if _, ok, err := s.Orch().GetSessionByOpenCodeID("ses_missing"); err != nil || ok {
		t.Errorf("GetSessionByOpenCodeID(missing) = ok:%v err:%v, want false/nil", ok, err)
	}
}

func TestAppendLog(t *testing.T) {
	s, _ := openTest(t)
	if err := s.Orch().AppendLog("info", "cron:test", "hello", `{"x":1}`, time.Now().UTC()); err != nil {
		t.Fatalf("AppendLog() error: %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM orch_logs`).Scan(&count); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if count != 1 {
		t.Errorf("log count = %d, want 1", count)
	}
}
