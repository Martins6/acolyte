package orch

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SetSetting upserts a non-secret capability setting.
func (s *Store) SetSetting(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("orch: setting key is required")
	}
	_, err := s.db.Exec(`INSERT INTO orch_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("orch: set setting %q: %w", key, err)
	}
	return nil
}

// GetSetting returns a setting and whether it exists.
func (s *Store) GetSetting(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM orch_settings WHERE key = ?`, strings.TrimSpace(key)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("orch: get setting %q: %w", key, err)
	}
	return value, true, nil
}
