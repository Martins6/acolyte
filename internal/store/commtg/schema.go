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
	version = 4
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
	if current < 2 {
		if _, err := db.Exec(schemaV2); err != nil {
			return fmt.Errorf("comm_tg: migrate to version 2: %w", err)
		}
		current = 2
	}
	if current < 3 {
		if err := migrateV3(db); err != nil {
			return fmt.Errorf("comm_tg: migrate to version 3: %w", err)
		}
		current = 3
	}
	if current < 4 {
		if err := migrateV4(db); err != nil {
			return fmt.Errorf("comm_tg: migrate to version 4: %w", err)
		}
		current = 4
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
  envelope_id         TEXT NOT NULL UNIQUE,
  media_path          TEXT NOT NULL DEFAULT '',
  opencode_session_id TEXT NOT NULL DEFAULT '',
  opencode_message_id TEXT NOT NULL DEFAULT '',
  received_at         DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_comm_tg_inbox_envelope ON comm_tg_inbox(envelope_id);
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

// schemaV3 adds the private-chat flag. Existing rows default to 0 (fail
// closed): pre-v3 data cannot distinguish private chats from groups, so a user
// must message the bot again before notifications become eligible.
func migrateV3(db *sql.DB) error {
	columns, err := tableColumns(db, "comm_tg_chats")
	if err != nil {
		return err
	}
	if columns["private"] {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE comm_tg_chats ADD COLUMN private INTEGER NOT NULL DEFAULT 0`)
	return err
}

// migrateV4 adds durable inbound replay state. Pre-v4 rows have no pending
// payload and are marked processed; new rows stay pending until admission.
func migrateV4(db *sql.DB) error {
	columns, err := tableColumns(db, "comm_tg_inbox")
	if err != nil {
		return err
	}
	additions := []struct {
		name string
		ddl  string
	}{
		{"pending_envelope", `ALTER TABLE comm_tg_inbox ADD COLUMN pending_envelope TEXT NOT NULL DEFAULT ''`},
		{"processed_at", `ALTER TABLE comm_tg_inbox ADD COLUMN processed_at DATETIME`},
		{"attempts", `ALTER TABLE comm_tg_inbox ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`},
		{"last_error", `ALTER TABLE comm_tg_inbox ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`},
	}
	for _, addition := range additions {
		if columns[addition.name] {
			continue
		}
		if _, err := db.Exec(addition.ddl); err != nil {
			return err
		}
	}
	_, err = db.Exec(`UPDATE comm_tg_inbox SET processed_at = received_at
		WHERE processed_at IS NULL AND pending_envelope = ''`)
	return err
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}
