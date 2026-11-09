// Package mem owns the memory module's namespaced tables (prefix mem_) in the
// per-agent database.
package mem

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	module  = "mem"
	version = 1
)

// Migrate creates or upgrades the memory schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
		module  TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("mem: create schema_meta: %w", err)
	}
	current, err := schemaVersion(db, module)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("mem: schema version %d is newer than supported version %d", current, version)
	}
	if current == version {
		return nil
	}
	if _, err := db.Exec(schemaV1); err != nil {
		return fmt.Errorf("mem: migrate to version %d: %w", version, err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)
		ON CONFLICT(module) DO UPDATE SET version = excluded.version`, module, version); err != nil {
		return fmt.Errorf("mem: record schema version: %w", err)
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
		return 0, fmt.Errorf("mem: read schema version: %w", err)
	}
	return value, nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS mem_dreams (
  id               TEXT PRIMARY KEY,
  ran_at           DATETIME NOT NULL,
  sessions_scanned INTEGER NOT NULL,
  skills_created   INTEGER NOT NULL DEFAULT 0,
  dry_run          INTEGER NOT NULL DEFAULT 0,
  note             TEXT NOT NULL DEFAULT ''
);
`
