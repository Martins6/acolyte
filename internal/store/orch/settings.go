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

// ListSettings returns every non-secret capability setting.
func (s *Store) ListSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM orch_settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("orch: list settings: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("orch: scan setting: %w", err)
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orch: list settings: %w", err)
	}
	return settings, nil
}

// DeleteSetting removes a non-secret capability setting. Missing keys are
// ignored so callers can clear state idempotently.
func (s *Store) DeleteSetting(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("orch: setting key is required")
	}
	if _, err := s.db.Exec(`DELETE FROM orch_settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("orch: delete setting %q: %w", key, err)
	}
	return nil
}
