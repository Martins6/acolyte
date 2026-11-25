package orch

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CronRoute is one durable cron output delivery intent.
type CronRoute struct {
	ID                string     `json:"id"`
	AgentTarget       string     `json:"agent_target"`
	Action            string     `json:"action"`
	JobID             string     `json:"job_id,omitempty"`
	ChatID            int64      `json:"chat_id,omitempty"`
	Text              string     `json:"text"`
	OpenCodeSessionID string     `json:"opencode_session_id,omitempty"`
	SourceMessageID   string     `json:"source_message_id,omitempty"`
	Meta              string     `json:"meta,omitempty"`
	Attempts          int        `json:"attempts"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	ClaimedUntil      *time.Time `json:"claimed_until,omitempty"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
}

const cronRouteColumns = `id, agent_target, action, job_id, chat_id, text, opencode_session_id,
	source_message_id, meta, attempts, next_attempt_at, last_error, created_at, claimed_until, delivered_at`

// AddCronRoute persists a delivery intent. The ID is idempotent.
func (s *Store) AddCronRoute(route CronRoute) error {
	if strings.TrimSpace(route.Text) == "" {
		return fmt.Errorf("orch: cron route text is required")
	}
	route.Action = strings.TrimSpace(route.Action)
	if route.Action == "" {
		route.Action = "notify-user"
	}
	if route.Meta == "" {
		route.Meta = "{}"
	}
	if route.ID == "" {
		route.ID = newID()
	}
	if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now().UTC()
	}
	if route.NextAttemptAt == nil {
		next := route.CreatedAt
		route.NextAttemptAt = &next
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO orch_cron_routes
		(id, agent_target, action, job_id, chat_id, text, opencode_session_id, source_message_id,
		 meta, attempts, next_attempt_at, last_error, created_at, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		route.ID, route.AgentTarget, route.Action, route.JobID, route.ChatID, route.Text,
		route.OpenCodeSessionID, route.SourceMessageID, route.Meta, route.Attempts,
		route.NextAttemptAt, route.LastError, route.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("orch: add cron route: %w", err)
	}
	return nil
}

// SetCronRouteAdmitted records the session and marker of an inbox route's
// synthetic turn, so a retry collects instead of re-admitting it.
func (s *Store) SetCronRouteAdmitted(id, sessionID, markerID string) error {
	result, err := s.db.Exec(`UPDATE orch_cron_routes
		SET opencode_session_id = ?, source_message_id = ?
		WHERE id = ? AND delivered_at IS NULL`, sessionID, markerID, id)
	if err != nil {
		return fmt.Errorf("orch: set cron route admitted %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("orch: set cron route admitted %q: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("orch: cron route %q is no longer pending", id)
	}
	return nil
}

// ClaimCronRoute leases a route for one delivery worker.
func (s *Store) ClaimCronRoute(id string, now, leaseUntil time.Time) (bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if leaseUntil.IsZero() {
		leaseUntil = now.Add(2 * time.Minute)
	}
	result, err := s.db.Exec(`UPDATE orch_cron_routes SET claimed_until = ?
		WHERE id = ? AND delivered_at IS NULL
		AND (claimed_until IS NULL OR claimed_until <= ?)`, leaseUntil, id, now)
	if err != nil {
		return false, fmt.Errorf("orch: claim cron route %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: claim cron route %q: %w", id, err)
	}
	return affected > 0, nil
}

func scanCronRoutes(rows *sql.Rows) ([]CronRoute, error) {
	var routes []CronRoute
	for rows.Next() {
		var route CronRoute
		var nextAttempt, claimedUntil, deliveredAt sql.NullTime
		if err := rows.Scan(&route.ID, &route.AgentTarget, &route.Action, &route.JobID, &route.ChatID,
			&route.Text, &route.OpenCodeSessionID, &route.SourceMessageID, &route.Meta,
			&route.Attempts, &nextAttempt, &route.LastError, &route.CreatedAt, &claimedUntil, &deliveredAt); err != nil {
			return nil, fmt.Errorf("orch: scan cron route: %w", err)
		}
		route.NextAttemptAt = timePtr(nextAttempt)
		route.ClaimedUntil = timePtr(claimedUntil)
		route.DeliveredAt = timePtr(deliveredAt)
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: scan cron routes: %w", err)
	}
	return routes, nil
}

// GetCronRoute returns one route by ID.
func (s *Store) GetCronRoute(id string) (CronRoute, bool, error) {
	rows, err := s.db.Query(`SELECT `+cronRouteColumns+` FROM orch_cron_routes WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return CronRoute{}, false, fmt.Errorf("orch: get cron route %q: %w", id, err)
	}
	defer rows.Close()
	routes, err := scanCronRoutes(rows)
	if err != nil {
		return CronRoute{}, false, err
	}
	if len(routes) == 0 {
		return CronRoute{}, false, nil
	}
	return routes[0], true, nil
}

// HoldCronRoute parks a route with an unpersisted admission marker so no worker
// re-admits it. It stays held until the lease expires (operator recovery can
// shorten it directly in the store).
func (s *Store) HoldCronRoute(id string, until time.Time) error {
	_, err := s.db.Exec(`UPDATE orch_cron_routes SET claimed_until = ?
		WHERE id = ? AND delivered_at IS NULL`, until, id)
	if err != nil {
		return fmt.Errorf("orch: hold cron route %q: %w", id, err)
	}
	return nil
}

// RenewCronRouteClaim extends a lease only if expected is still the claimed
// value, so a worker that lost the route cannot renew it.
func (s *Store) RenewCronRouteClaim(id string, expected, next time.Time) (bool, error) {
	result, err := s.db.Exec(`UPDATE orch_cron_routes SET claimed_until = ?
		WHERE id = ? AND delivered_at IS NULL AND claimed_until = ?`, next, id, expected)
	if err != nil {
		return false, fmt.Errorf("orch: renew cron route claim %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("orch: renew cron route claim %q: %w", id, err)
	}
	return affected > 0, nil
}

// DueCronRoutes returns undelivered routes whose next attempt is due.
func (s *Store) DueCronRoutes(now time.Time, limit int) ([]CronRoute, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+cronRouteColumns+` FROM orch_cron_routes
		WHERE delivered_at IS NULL AND COALESCE(next_attempt_at, created_at) <= ?
		ORDER BY created_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("orch: list due cron routes: %w", err)
	}
	defer rows.Close()

	var routes []CronRoute
	for rows.Next() {
		var route CronRoute
		var nextAttempt, claimedUntil, deliveredAt sql.NullTime
		if err := rows.Scan(&route.ID, &route.AgentTarget, &route.Action, &route.JobID, &route.ChatID,
			&route.Text, &route.OpenCodeSessionID, &route.SourceMessageID, &route.Meta,
			&route.Attempts, &nextAttempt, &route.LastError, &route.CreatedAt, &claimedUntil, &deliveredAt); err != nil {
			return nil, fmt.Errorf("orch: scan cron route: %w", err)
		}
		route.NextAttemptAt = timePtr(nextAttempt)
		route.ClaimedUntil = timePtr(claimedUntil)
		route.DeliveredAt = timePtr(deliveredAt)
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list due cron routes: %w", err)
	}
	return routes, nil
}

// ListCronRoutes returns every durable delivery intent in chronological order.
func (s *Store) ListCronRoutes() ([]CronRoute, error) {
	rows, err := s.db.Query(`SELECT ` + cronRouteColumns + ` FROM orch_cron_routes
		ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("orch: list cron routes: %w", err)
	}
	defer rows.Close()

	var routes []CronRoute
	for rows.Next() {
		var route CronRoute
		var nextAttempt, claimedUntil, deliveredAt sql.NullTime
		if err := rows.Scan(&route.ID, &route.AgentTarget, &route.Action, &route.JobID, &route.ChatID,
			&route.Text, &route.OpenCodeSessionID, &route.SourceMessageID, &route.Meta,
			&route.Attempts, &nextAttempt, &route.LastError, &route.CreatedAt, &claimedUntil, &deliveredAt); err != nil {
			return nil, fmt.Errorf("orch: scan cron route: %w", err)
		}
		route.NextAttemptAt = timePtr(nextAttempt)
		route.ClaimedUntil = timePtr(claimedUntil)
		route.DeliveredAt = timePtr(deliveredAt)
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list cron routes: %w", err)
	}
	return routes, nil
}

// MarkCronRouteDelivered completes a route.
func (s *Store) MarkCronRouteDelivered(id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE orch_cron_routes SET delivered_at = ?, next_attempt_at = NULL, claimed_until = NULL
		WHERE id = ? AND delivered_at IS NULL`, at, id)
	if err != nil {
		return fmt.Errorf("orch: mark cron route delivered: %w", err)
	}
	return nil
}

// MarkCronRouteRetry schedules the next delivery attempt.
func (s *Store) MarkCronRouteRetry(id string, attempts int, next time.Time, errText string) error {
	if next.IsZero() {
		next = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE orch_cron_routes
		SET attempts = ?, next_attempt_at = ?, last_error = ?, claimed_until = NULL
		WHERE id = ? AND delivered_at IS NULL`, attempts, next, errText, id)
	if err != nil {
		return fmt.Errorf("orch: mark cron route retry: %w", err)
	}
	return nil
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}
