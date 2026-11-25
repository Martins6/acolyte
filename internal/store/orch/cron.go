package orch

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// ErrJobNotFound means no cron job matches the title.
var ErrJobNotFound = errors.New("orch: cron job not found")

// ErrJobExists means a cron job title is already taken.
var ErrJobExists = errors.New("orch: cron job already exists")

// CronJob is one row of orch_cron_jobs.
type CronJob struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Owner       string     `json:"owner"`
	Kind        string     `json:"kind"`
	Spec        string     `json:"spec"`
	Prompt      string     `json:"prompt"`
	ShellPath   string     `json:"shell_path"`
	Routine     string     `json:"routine"`
	RoutineArgs string     `json:"routine_args"`
	AgentTarget string     `json:"agent_target"`
	SessionID   string     `json:"session_id"`
	Timezone    string     `json:"timezone"`
	Enabled     bool       `json:"enabled"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Meta        string     `json:"meta"`
}

// Stats is the summary used by `acolyte status`.
type Stats struct {
	JobCount     int        `json:"job_count"`
	NextJobAt    *time.Time `json:"next_job_at,omitempty"`
	LastActiveAt *time.Time `json:"last_active_at,omitempty"`
}

// Store gives access to the orchestrator tables.
type Store struct {
	db *sql.DB
}

// New returns an orchestrator store on an open database.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cronColumns = `id, title, owner, kind, spec, prompt, shell_path, routine,
	routine_args, agent_target, session_id, timezone, enabled, last_run_at,
	next_run_at, created_at, meta`

// AddCronJobIfMissing inserts a job unless its title already exists. It
// returns false when a job with the same title is already present.
func (s *Store) AddCronJobIfMissing(job CronJob) (bool, error) {
	job.Title = strings.TrimSpace(job.Title)
	job.Kind = strings.TrimSpace(job.Kind)
	job.Spec = strings.TrimSpace(job.Spec)
	if job.Title == "" {
		return false, fmt.Errorf("orch: cron job title is required")
	}
	if job.Kind == "" {
		return false, fmt.Errorf("orch: cron job kind is required")
	}
	if job.Spec == "" {
		return false, fmt.Errorf("orch: cron job spec is required")
	}
	if job.ID == "" {
		job.ID = newID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}

	result, err := s.db.Exec(
		`INSERT INTO orch_cron_jobs (`+cronColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(title) DO NOTHING`,
		job.ID, job.Title, job.Owner, job.Kind, job.Spec, job.Prompt, job.ShellPath,
		job.Routine, defaultString(job.RoutineArgs, "{}"), job.AgentTarget, job.SessionID,
		job.Timezone, boolInt(job.Enabled), job.LastRunAt, job.NextRunAt, job.CreatedAt,
		defaultString(job.Meta, "{}"),
	)
	if err != nil {
		return false, fmt.Errorf("orch: add cron job %q: %w", job.Title, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: add cron job %q: %w", job.Title, err)
	}
	return affected > 0, nil
}

// AddCronJob inserts a user-created job and rejects duplicate titles.
func (s *Store) AddCronJob(job CronJob) error {
	job.Title = strings.TrimSpace(job.Title)
	job.Kind = strings.TrimSpace(job.Kind)
	job.Spec = strings.TrimSpace(job.Spec)
	if job.Title == "" {
		return fmt.Errorf("orch: cron job title is required")
	}
	if job.Kind == "" {
		return fmt.Errorf("orch: cron job kind is required")
	}
	if job.Spec == "" {
		return fmt.Errorf("orch: cron job spec is required")
	}
	if job.ID == "" {
		job.ID = newID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}

	_, err := s.db.Exec(
		`INSERT INTO orch_cron_jobs (`+cronColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Title, job.Owner, job.Kind, job.Spec, job.Prompt, job.ShellPath,
		job.Routine, defaultString(job.RoutineArgs, "{}"), job.AgentTarget, job.SessionID,
		job.Timezone, boolInt(job.Enabled), job.LastRunAt, job.NextRunAt, job.CreatedAt,
		defaultString(job.Meta, "{}"),
	)
	if err == nil {
		return nil
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) &&
		(sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique ||
			sqliteErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey) {
		return ErrJobExists
	}
	return fmt.Errorf("orch: add cron job %q: %w", job.Title, err)
}

// UpsertCronJobIfMatch inserts a job, or updates the job with the same title
// only when its current owner/kind/routine match the expected capability
// identity. The identity check runs in the same statement as the write, so a
// user job inserted concurrently under the title can never be overwritten. It
// reports whether a row was written.
func (s *Store) UpsertCronJobIfMatch(job CronJob, owner, kind, routine string) (bool, error) {
	job.Title = strings.TrimSpace(job.Title)
	job.Kind = strings.TrimSpace(job.Kind)
	job.Spec = strings.TrimSpace(job.Spec)
	if job.Title == "" {
		return false, fmt.Errorf("orch: cron job title is required")
	}
	if job.Kind == "" {
		return false, fmt.Errorf("orch: cron job kind is required")
	}
	if job.Spec == "" {
		return false, fmt.Errorf("orch: cron job spec is required")
	}
	if job.Owner != owner || job.Kind != kind || job.Routine != routine {
		return false, fmt.Errorf("orch: cron job %q identity %q/%q/%q does not match the expected %q/%q/%q",
			job.Title, job.Owner, job.Kind, job.Routine, owner, kind, routine)
	}
	if job.ID == "" {
		job.ID = newID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}

	result, err := s.db.Exec(
		`INSERT INTO orch_cron_jobs (`+cronColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(title) DO UPDATE SET
		   owner = excluded.owner,
		   kind = excluded.kind,
		   spec = excluded.spec,
		   prompt = excluded.prompt,
		   shell_path = excluded.shell_path,
		   routine = excluded.routine,
		   routine_args = excluded.routine_args,
		   agent_target = excluded.agent_target,
		   session_id = excluded.session_id,
		   timezone = excluded.timezone,
		   enabled = excluded.enabled,
		   next_run_at = excluded.next_run_at,
		   meta = excluded.meta
		 WHERE orch_cron_jobs.owner = ? AND orch_cron_jobs.kind = ? AND orch_cron_jobs.routine = ?`,
		job.ID, job.Title, job.Owner, job.Kind, job.Spec, job.Prompt, job.ShellPath,
		job.Routine, defaultString(job.RoutineArgs, "{}"), job.AgentTarget, job.SessionID,
		job.Timezone, boolInt(job.Enabled), job.LastRunAt, job.NextRunAt, job.CreatedAt,
		defaultString(job.Meta, "{}"),
		owner, kind, routine,
	)
	if err != nil {
		return false, fmt.Errorf("orch: upsert cron job %q: %w", job.Title, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: upsert cron job %q: %w", job.Title, err)
	}
	return affected > 0, nil
}

// DeleteCronJob removes a job by title.
func (s *Store) DeleteCronJob(title string) error {
	result, err := s.db.Exec(`DELETE FROM orch_cron_jobs WHERE title = ?`, strings.TrimSpace(title))
	if err != nil {
		return fmt.Errorf("orch: delete cron job %q: %w", title, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("orch: delete cron job %q: %w", title, err)
	}
	if affected == 0 {
		return ErrJobNotFound
	}
	return nil
}

// DeleteCronJobIfMatch removes a job by title only when its current
// owner/kind/routine match the expected capability identity, so a replacement
// job inserted concurrently under the title is never deleted. It reports
// whether a row was deleted.
func (s *Store) DeleteCronJobIfMatch(title, owner, kind, routine string) (bool, error) {
	result, err := s.db.Exec(
		`DELETE FROM orch_cron_jobs WHERE title = ? AND owner = ? AND kind = ? AND routine = ?`,
		strings.TrimSpace(title), owner, kind, routine,
	)
	if err != nil {
		return false, fmt.Errorf("orch: delete cron job %q: %w", title, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: delete cron job %q: %w", title, err)
	}
	return affected > 0, nil
}

// SetCronJobEnabled toggles a job without deleting it.
func (s *Store) SetCronJobEnabled(title string, enabled bool) error {
	result, err := s.db.Exec(`UPDATE orch_cron_jobs SET enabled = ? WHERE title = ?`,
		boolInt(enabled), strings.TrimSpace(title))
	if err != nil {
		return fmt.Errorf("orch: set cron job %q enabled: %w", title, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("orch: set cron job %q enabled: %w", title, err)
	}
	if affected == 0 {
		return ErrJobNotFound
	}
	return nil
}

// DueCronJobs returns enabled jobs whose next run is due, or unset.
func (s *Store) DueCronJobs(now time.Time) ([]CronJob, error) {
	rows, err := s.db.Query(`SELECT `+cronColumns+` FROM orch_cron_jobs
		WHERE enabled = 1 AND (next_run_at IS NULL OR next_run_at <= ?)
		ORDER BY COALESCE(next_run_at, created_at) ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("orch: list due cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("orch: scan due cron job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list due cron jobs: %w", err)
	}
	return jobs, nil
}

// ClaimCronJob advances next_run_at only if the expected value is still current.
func (s *Store) ClaimCronJob(id string, expectedNext *time.Time, next time.Time) (bool, error) {
	var expected any
	if expectedNext != nil {
		expected = *expectedNext
	}
	result, err := s.db.Exec(`UPDATE orch_cron_jobs SET next_run_at = ?
		WHERE id = ? AND enabled = 1 AND next_run_at IS ?`, next, id, expected)
	if err != nil {
		return false, fmt.Errorf("orch: claim cron job %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: claim cron job %q: %w", id, err)
	}
	return affected > 0, nil
}

// RequeueCronJob moves the next run so a job whose output could not be
// persisted is retried instead of being silently dropped.
func (s *Store) RequeueCronJob(id string, next time.Time) error {
	_, err := s.db.Exec(`UPDATE orch_cron_jobs SET next_run_at = ? WHERE id = ?`, next, id)
	if err != nil {
		return fmt.Errorf("orch: requeue cron job %q: %w", id, err)
	}
	return nil
}

// MarkCronJobRun records a completed run. Missing jobs are ignored.
func (s *Store) MarkCronJobRun(id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if _, err := s.db.Exec(`UPDATE orch_cron_jobs SET last_run_at = ? WHERE id = ?`, at, id); err != nil {
		return fmt.Errorf("orch: mark cron job %q run: %w", id, err)
	}
	return nil
}

// GetCronJobByTitle returns one job by title.
func (s *Store) GetCronJobByTitle(title string) (CronJob, error) {
	row := s.db.QueryRow(`SELECT `+cronColumns+` FROM orch_cron_jobs WHERE title = ?`, strings.TrimSpace(title))
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CronJob{}, ErrJobNotFound
	}
	if err != nil {
		return CronJob{}, fmt.Errorf("orch: get cron job %q: %w", title, err)
	}
	return job, nil
}

// ListCronJobs returns every job ordered by title.
func (s *Store) ListCronJobs() ([]CronJob, error) {
	rows, err := s.db.Query(`SELECT ` + cronColumns + ` FROM orch_cron_jobs ORDER BY title`)
	if err != nil {
		return nil, fmt.Errorf("orch: list cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("orch: scan cron job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list cron jobs: %w", err)
	}
	return jobs, nil
}

// Stats returns the job count, next enabled job, and latest session activity.
func (s *Store) Stats() (Stats, error) {
	var stats Stats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM orch_cron_jobs WHERE enabled = 1`).Scan(&stats.JobCount); err != nil {
		return Stats{}, fmt.Errorf("orch: count cron jobs: %w", err)
	}

	var next sql.NullTime
	err := s.db.QueryRow(`SELECT next_run_at FROM orch_cron_jobs
		WHERE enabled = 1 AND next_run_at IS NOT NULL
		ORDER BY next_run_at ASC LIMIT 1`).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Stats{}, fmt.Errorf("orch: read next job: %w", err)
	}
	if next.Valid {
		value := next.Time
		stats.NextJobAt = &value
	}

	var last sql.NullTime
	err = s.db.QueryRow(`SELECT last_active_at FROM orch_sessions
		ORDER BY last_active_at DESC LIMIT 1`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Stats{}, fmt.Errorf("orch: read last activity: %w", err)
	}
	if last.Valid {
		value := last.Time
		stats.LastActiveAt = &value
	}
	return stats, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// PendingTurn is a cron run whose turn was admitted but whose output has not
// been collected yet. It is the recovery handle for a run cut short by a
// restart.
type PendingTurn struct {
	JobID     string
	SessionID string
	MarkerID  string
	Kind      string
	StartedAt *time.Time
	Attempts  int
}

// SetCronJobPending records the turn a job just admitted.
func (s *Store) SetCronJobPending(id, sessionID, markerID, kind string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE orch_cron_jobs
		SET pending_session_id = ?, pending_marker_id = ?, pending_kind = ?, pending_started_at = ?
		WHERE id = ?`, sessionID, markerID, kind, at, id)
	if err != nil {
		return fmt.Errorf("orch: set pending turn for job %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("orch: set pending turn for job %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("orch: cron job %q not found", id)
	}
	return nil
}

// ClearCronJobPending marks the job's admitted turn as collected.
func (s *Store) ClearCronJobPending(id string) error {
	_, err := s.db.Exec(`UPDATE orch_cron_jobs
		SET pending_session_id = '', pending_marker_id = '', pending_kind = '',
		    pending_started_at = NULL, pending_attempts = 0
		WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("orch: clear pending turn for job %q: %w", id, err)
	}
	return nil
}

// PendingCronTurns returns jobs whose admitted turn started at or before before.
func (s *Store) PendingCronTurns(before time.Time, limit int) ([]PendingTurn, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, pending_session_id, pending_marker_id, pending_kind,
			pending_started_at, pending_attempts
		FROM orch_cron_jobs
		WHERE pending_marker_id != '' AND pending_started_at <= ?
		ORDER BY pending_started_at LIMIT ?`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("orch: list pending cron turns: %w", err)
	}
	defer rows.Close()

	var turns []PendingTurn
	for rows.Next() {
		var turn PendingTurn
		var startedAt sql.NullTime
		if err := rows.Scan(&turn.JobID, &turn.SessionID, &turn.MarkerID, &turn.Kind, &startedAt, &turn.Attempts); err != nil {
			return nil, fmt.Errorf("orch: scan pending cron turn: %w", err)
		}
		if startedAt.Valid {
			value := startedAt.Time
			turn.StartedAt = &value
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: scan pending cron turns: %w", err)
	}
	return turns, nil
}

// GetCronJobByID returns one job by its identifier.
func (s *Store) GetCronJobByID(id string) (CronJob, error) {
	row := s.db.QueryRow(`SELECT `+cronColumns+` FROM orch_cron_jobs WHERE id = ?`, strings.TrimSpace(id))
	return scanJob(row)
}

func scanJob(row scanner) (CronJob, error) {
	var job CronJob
	var enabled int
	var last, next sql.NullTime
	if err := row.Scan(
		&job.ID, &job.Title, &job.Owner, &job.Kind, &job.Spec, &job.Prompt,
		&job.ShellPath, &job.Routine, &job.RoutineArgs, &job.AgentTarget,
		&job.SessionID, &job.Timezone, &enabled, &last, &next, &job.CreatedAt,
		&job.Meta,
	); err != nil {
		return CronJob{}, err
	}
	job.Enabled = enabled != 0
	if last.Valid {
		value := last.Time
		job.LastRunAt = &value
	}
	if next.Valid {
		value := next.Time
		job.NextRunAt = &value
	}
	return job, nil
}

func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
