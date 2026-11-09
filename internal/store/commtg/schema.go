// Package commtg owns the Telegram communication module's namespaced tables
// (prefix comm_tg_) in the per-agent database.
package commtg

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	module  = "comm_tg"
	version = 1
)

// Migrate creates or upgrades the Telegram communication schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
		module  TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("comm_tg: create schema_meta: %w", err)
	}

	current, err := schemaVersion(db, module)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("comm_tg: schema version %d is newer than supported version %d", current, version)
	}
	if current == version {
		return nil
	}

	if _, err := db.Exec(schemaV1); err != nil {
		return fmt.Errorf("comm_tg: migrate to version %d: %w", version, err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)
		ON CONFLICT(module) DO UPDATE SET version = excluded.version`, module, version); err != nil {
		return fmt.Errorf("comm_tg: record schema version: %w", err)
	}
	return nil
}

func schemaVersion(db *sql.DB, moduleName string) (int, error) {
	var value int
	err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, moduleName).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("comm_tg: read schema version: %w", err)
	}
	return value, nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS comm_tg_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`
