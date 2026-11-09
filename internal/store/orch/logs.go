package orch

import (
	"fmt"
	"strings"
	"time"
)

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
