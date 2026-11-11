package commtg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Store gives access to the Telegram communication tables.
type Store struct {
	db *sql.DB
}

// New returns a Telegram communication store on an open database.
func New(db *sql.DB) *Store { return &Store{db: db} }

// SetSetting upserts a non-secret channel setting.
func (s *Store) SetSetting(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("comm_tg: setting key is required")
	}
	_, err := s.db.Exec(`INSERT INTO comm_tg_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("comm_tg: set setting %q: %w", key, err)
	}
	return nil
}

// GetSetting returns a setting and whether it exists.
func (s *Store) GetSetting(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM comm_tg_settings WHERE key = ?`, strings.TrimSpace(key)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("comm_tg: get setting %q: %w", key, err)
	}
	return value, true, nil
}

// ListSettings returns every non-secret channel setting.
func (s *Store) ListSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM comm_tg_settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list settings: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("comm_tg: scan setting: %w", err)
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: list settings: %w", err)
	}
	return settings, nil
}
