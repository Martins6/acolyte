// Package orch owns the orchestrator's namespaced tables (prefix orch_) in the
// per-agent database.
package orch

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	module  = "orch"
	version = 2
)

// Migrate creates or upgrades the orchestrator schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
		module  TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("orch: create schema_meta: %w", err)
	}

	current, err := schemaVersion(db, module)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("orch: schema version %d is newer than supported version %d", current, version)
	}
	if current == version {
		return nil
	}

	if current < 2 {
		if _, err := db.Exec(schemaV2); err != nil {
			return fmt.Errorf("orch: migrate to version 2: %w", err)
		}
		current = 2
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)
		ON CONFLICT(module) DO UPDATE SET version = excluded.version`, module, version); err != nil {
		return fmt.Errorf("orch: record schema version: %w", err)
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
		return 0, fmt.Errorf("orch: read schema version: %w", err)
	}
	return value, nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS orch_sessions (
  id                  TEXT PRIMARY KEY,
  thread              TEXT NOT NULL UNIQUE,
  opencode_session_id TEXT NOT NULL,
  created_at          DATETIME NOT NULL,
  last_active_at      DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orch_sessions_last_active ON orch_sessions(last_active_at);

CREATE TABLE IF NOT EXISTS orch_cron_jobs (
  id           TEXT PRIMARY KEY,
  title        TEXT NOT NULL UNIQUE,
  owner        TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL,
  spec         TEXT NOT NULL,
  prompt       TEXT NOT NULL DEFAULT '',
  shell_path   TEXT NOT NULL DEFAULT '',
  routine      TEXT NOT NULL DEFAULT '',
  routine_args TEXT NOT NULL DEFAULT '{}',
  agent_target TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  timezone     TEXT NOT NULL DEFAULT '',
  enabled      INTEGER NOT NULL DEFAULT 1,
  last_run_at  DATETIME,
  next_run_at  DATETIME,
  created_at   DATETIME NOT NULL,
  meta         TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_orch_cron_jobs_next ON orch_cron_jobs(enabled, next_run_at);

CREATE TABLE IF NOT EXISTS orch_logs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  level      TEXT,
  source     TEXT,
  message    TEXT,
  meta       TEXT,
  created_at DATETIME
);

CREATE TABLE IF NOT EXISTS orch_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

const schemaV2 = schemaV1 + `
CREATE TABLE IF NOT EXISTS orch_cron_routes (
  id                  TEXT PRIMARY KEY,
  agent_target        TEXT NOT NULL,
  chat_id             INTEGER NOT NULL,
  text                TEXT NOT NULL,
  opencode_session_id TEXT NOT NULL DEFAULT '',
  source_message_id   TEXT NOT NULL DEFAULT '',
  attempts            INTEGER NOT NULL DEFAULT 0,
  next_attempt_at     DATETIME,
  last_error          TEXT NOT NULL DEFAULT '',
  created_at          DATETIME NOT NULL,
  delivered_at        DATETIME
);
CREATE INDEX IF NOT EXISTS idx_orch_cron_routes_due ON orch_cron_routes(delivered_at, next_attempt_at);
`
