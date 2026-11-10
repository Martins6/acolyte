package orch

import (
	"fmt"
	"strings"
	"time"
)

// LogEntry is one orchestrator audit-log row.
type LogEntry struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	Meta      string    `json:"meta"`
	CreatedAt time.Time `json:"created_at"`
}

// AppendLog writes one orchestrator audit-log row.
func (s *Store) AppendLog(level, source, message, meta string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if strings.TrimSpace(meta) == "" {
		meta = "{}"
	}
	_, err := s.db.Exec(`INSERT INTO orch_logs (level, source, message, meta, created_at)
		VALUES (?, ?, ?, ?, ?)`, strings.TrimSpace(level), strings.TrimSpace(source), message, meta, at)
	if err != nil {
		return fmt.Errorf("orch: append log: %w", err)
	}
	return nil
}

// ListLogs returns audit-log rows in chronological order. limit <= 0 returns
// every row.
func (s *Store) ListLogs(limit int) ([]LogEntry, error) {
	query := `SELECT id, level, source, message, meta, created_at FROM orch_logs
		ORDER BY created_at, id`
	args := []any{}
	if limit > 0 {
		query = `SELECT id, level, source, message, meta, created_at FROM (
			SELECT id, level, source, message, meta, created_at FROM orch_logs
			ORDER BY created_at DESC, id DESC LIMIT ?
		) ORDER BY created_at, id`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("orch: list logs: %w", err)
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var entry LogEntry
		if err := rows.Scan(&entry.ID, &entry.Level, &entry.Source, &entry.Message,
			&entry.Meta, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("orch: scan log: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list logs: %w", err)
	}
	return entries, nil
}
