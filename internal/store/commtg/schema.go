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
	version = 2
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

	// schemaV2 is idempotent and includes the v1 settings table, so it
	// upgrades both fresh databases and v1 installations in one step.
	if _, err := db.Exec(schemaV2); err != nil {
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

// schemaV1 is kept for migration tests and for understanding past installs.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS comm_tg_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

const schemaV2 = `
CREATE TABLE IF NOT EXISTS comm_tg_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS comm_tg_chats (
  chat_id       INTEGER PRIMARY KEY,
  username      TEXT NOT NULL DEFAULT '',
  allowed       INTEGER NOT NULL DEFAULT 1,
  first_seen_at DATETIME NOT NULL,
  last_seen_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS comm_tg_inbox (
  id                  TEXT PRIMARY KEY,
  tg_update_id        INTEGER NOT NULL UNIQUE,
  chat_id             INTEGER NOT NULL,
  envelope_id         TEXT NOT NULL,
  text                TEXT NOT NULL DEFAULT '',
  media_path          TEXT NOT NULL DEFAULT '',
  opencode_session_id TEXT NOT NULL DEFAULT '',
  opencode_message_id TEXT NOT NULL DEFAULT '',
  received_at         DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_comm_tg_inbox_chat ON comm_tg_inbox(chat_id, received_at);

CREATE TABLE IF NOT EXISTS comm_tg_outbox (
  id                  TEXT PRIMARY KEY,
  envelope_id         TEXT NOT NULL,
  chat_id             INTEGER NOT NULL,
  text                TEXT NOT NULL,
  reply_to            INTEGER,
  chunks_sent         INTEGER NOT NULL DEFAULT 0,
  attempts            INTEGER NOT NULL DEFAULT 0,
  next_attempt_at     DATETIME,
  last_error          TEXT NOT NULL DEFAULT '',
  delivery            TEXT NOT NULL DEFAULT 'pending',
  tg_message_id       INTEGER,
  opencode_session_id TEXT NOT NULL DEFAULT '',
  source_message_id   TEXT NOT NULL DEFAULT '',
  created_at          DATETIME NOT NULL,
  sent_at             DATETIME
);
CREATE INDEX IF NOT EXISTS idx_comm_tg_outbox_due ON comm_tg_outbox(delivery, next_attempt_at);

CREATE TABLE IF NOT EXISTS comm_tg_media (
  tg_file_id TEXT PRIMARY KEY,
  file_path  TEXT NOT NULL,
  type       TEXT NOT NULL DEFAULT '',
  chat_id    INTEGER NOT NULL,
  saved_at   DATETIME NOT NULL
);
`
