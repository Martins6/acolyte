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
	ChatID            int64      `json:"chat_id"`
	Text              string     `json:"text"`
	OpenCodeSessionID string     `json:"opencode_session_id,omitempty"`
	SourceMessageID   string     `json:"source_message_id,omitempty"`
	Attempts          int        `json:"attempts"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
}

const cronRouteColumns = `id, agent_target, chat_id, text, opencode_session_id,
	source_message_id, attempts, next_attempt_at, last_error, created_at, delivered_at`

// AddCronRoute persists a delivery intent. The ID is idempotent.
func (s *Store) AddCronRoute(route CronRoute) error {
	if strings.TrimSpace(route.Text) == "" {
		return fmt.Errorf("orch: cron route text is required")
	}
	if route.ChatID == 0 {
		return fmt.Errorf("orch: cron route chat id is required")
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
		(id, agent_target, chat_id, text, opencode_session_id, source_message_id,
		 attempts, next_attempt_at, last_error, created_at, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		route.ID, route.AgentTarget, route.ChatID, route.Text, route.OpenCodeSessionID,
		route.SourceMessageID, route.Attempts, route.NextAttemptAt, route.LastError, route.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("orch: add cron route: %w", err)
	}
	return nil
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
		var nextAttempt, deliveredAt sql.NullTime
		if err := rows.Scan(&route.ID, &route.AgentTarget, &route.ChatID, &route.Text,
			&route.OpenCodeSessionID, &route.SourceMessageID, &route.Attempts,
			&nextAttempt, &route.LastError, &route.CreatedAt, &deliveredAt); err != nil {
			return nil, fmt.Errorf("orch: scan cron route: %w", err)
		}
		route.NextAttemptAt = timePtr(nextAttempt)
		route.DeliveredAt = timePtr(deliveredAt)
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list due cron routes: %w", err)
	}
	return routes, nil
}

// MarkCronRouteDelivered completes a route.
func (s *Store) MarkCronRouteDelivered(id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE orch_cron_routes SET delivered_at = ?, next_attempt_at = NULL
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
		SET attempts = ?, next_attempt_at = ?, last_error = ?
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
