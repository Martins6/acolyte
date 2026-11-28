// Package board owns the global board database and its namespaced tables
// (prefix brd_) at $ACOLYTE_HOME/board.sqlite.db.
package board

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	module  = "brd"
	version = 2
)

// Migrate creates or upgrades the board schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
		module  TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("brd: create schema_meta: %w", err)
	}

	current, err := schemaVersion(db, module)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("brd: schema version %d is newer than supported version %d", current, version)
	}
	if current == version {
		return nil
	}

	if current < 1 {
		if _, err := db.Exec(schemaV1); err != nil {
			return fmt.Errorf("brd: migrate to version 1: %w", err)
		}
		current = 1
	}
	if current < 2 {
		// A development database created before the schema was versioned may
		// already carry the column, so add it only when it is missing.
		if err := addColumnIfMissing(db, "brd_inbox", "turn_reserved_at", "DATETIME"); err != nil {
			return err
		}
		if _, err := db.Exec(schemaV2); err != nil {
			return fmt.Errorf("brd: migrate to version 2: %w", err)
		}
		current = 2
	}

	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)
		ON CONFLICT(module) DO UPDATE SET version = excluded.version`, module, version); err != nil {
		return fmt.Errorf("brd: record schema version: %w", err)
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
		return 0, fmt.Errorf("brd: read schema version: %w", err)
	}
	return value, nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS brd_posts (
  id              TEXT PRIMARY KEY,
  title           TEXT NOT NULL DEFAULT '',
  created_by_kind TEXT NOT NULL,
  created_by_id   TEXT NOT NULL,
  created_at      DATETIME NOT NULL,
  updated_at      DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS brd_posts_updated ON brd_posts(updated_at);

CREATE TABLE IF NOT EXISTS brd_entries (
  id           TEXT PRIMARY KEY,
  envelope_id  TEXT NOT NULL,
  post_id      TEXT NOT NULL,
  seq          INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  author_kind  TEXT NOT NULL,
  author_id    TEXT NOT NULL,
  text         TEXT NOT NULL DEFAULT '',
  reply_to_id  TEXT NOT NULL DEFAULT '',
  created_at   DATETIME NOT NULL,
  UNIQUE (post_id, seq)
);
CREATE UNIQUE INDEX IF NOT EXISTS brd_entries_one_post
  ON brd_entries(post_id) WHERE kind = 'post';
CREATE INDEX IF NOT EXISTS brd_entries_post ON brd_entries(post_id, seq);

CREATE TABLE IF NOT EXISTS brd_files (
  entry_id    TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  name        TEXT NOT NULL,
  path        TEXT NOT NULL,
  mode        TEXT NOT NULL,
  source_path TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL,
  sha256      TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (entry_id, seq)
);

CREATE TABLE IF NOT EXISTS brd_participants (
  post_id      TEXT NOT NULL,
  kind         TEXT NOT NULL,
  id           TEXT NOT NULL,
  added_at     DATETIME NOT NULL,
  added_by_kind TEXT NOT NULL,
  added_by_id   TEXT NOT NULL,
  PRIMARY KEY (post_id, kind, id)
);
CREATE INDEX IF NOT EXISTS brd_participants_actor ON brd_participants(kind, id);

CREATE TABLE IF NOT EXISTS brd_tags (
  post_id TEXT NOT NULL,
  tag     TEXT NOT NULL,
  PRIMARY KEY (post_id, tag)
);
CREATE INDEX IF NOT EXISTS brd_tags_tag ON brd_tags(tag);

CREATE TABLE IF NOT EXISTS brd_reads (
  post_id       TEXT NOT NULL,
  kind          TEXT NOT NULL,
  id            TEXT NOT NULL,
  last_read_seq INTEGER NOT NULL,
  PRIMARY KEY (post_id, kind, id)
);

CREATE TABLE IF NOT EXISTS brd_subscriptions (
  agent_id   TEXT NOT NULL,
  post_id    TEXT NOT NULL DEFAULT '',
  aware      INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL,
  PRIMARY KEY (agent_id, post_id)
);

CREATE TABLE IF NOT EXISTS brd_mutes (
  agent_id   TEXT NOT NULL,
  post_id    TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  PRIMARY KEY (agent_id, post_id)
);

CREATE TABLE IF NOT EXISTS brd_automations (
  agent_id     TEXT NOT NULL,
  post_id      TEXT NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  created_at   DATETIME NOT NULL,
  updated_at   DATETIME NOT NULL,
  last_turn_at DATETIME,
  turns        INTEGER NOT NULL DEFAULT 0,
  paused_at    DATETIME,
  pause_reason TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (agent_id, post_id)
);

CREATE TABLE IF NOT EXISTS brd_inbox (
  agent_id     TEXT NOT NULL,
  entry_id     TEXT NOT NULL,
  post_id      TEXT NOT NULL,
  created_at   DATETIME NOT NULL,
  read_at      DATETIME,
  processed_at DATETIME,
  mode         TEXT NOT NULL,
  aware        INTEGER NOT NULL DEFAULT 1,
  session_id   TEXT NOT NULL DEFAULT '',
  message_id   TEXT NOT NULL DEFAULT '',
  attempts     INTEGER NOT NULL DEFAULT 0,
  error        TEXT NOT NULL DEFAULT '',
  claimed_at   DATETIME,
  claim_token  TEXT NOT NULL DEFAULT '',
  promotion_claimed_at DATETIME,
  promotion_token      TEXT NOT NULL DEFAULT '',
  promotion_marker     TEXT NOT NULL DEFAULT '',
  promotion_session_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (agent_id, entry_id)
);
CREATE INDEX IF NOT EXISTS brd_inbox_agent ON brd_inbox(agent_id, read_at);
CREATE INDEX IF NOT EXISTS brd_inbox_pending ON brd_inbox(agent_id, post_id, processed_at);

CREATE TABLE IF NOT EXISTS brd_alerts (
  id          TEXT PRIMARY KEY,
  agent_id    TEXT NOT NULL,
  post_id     TEXT NOT NULL,
  message     TEXT NOT NULL,
  created_at  DATETIME NOT NULL,
  notified_at DATETIME
);
CREATE INDEX IF NOT EXISTS brd_alerts_agent ON brd_alerts(agent_id, notified_at);
`

// schemaV2 adds per-entry turn reservations and short-lived session bindings
// that confine board tools to one post.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS brd_session_bindings (
  session_id TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL,
  post_id    TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS brd_session_bindings_expiry ON brd_session_bindings(expires_at);
`

// addColumnIfMissing adds a column to an existing table unless PRAGMA reports
// it already present. SQLite has no ADD COLUMN IF NOT EXISTS.
func addColumnIfMissing(db *sql.DB, table, column, kind string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("brd: inspect %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("brd: inspect %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("brd: inspect %s: %w", table, err)
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, kind)); err != nil {
		return fmt.Errorf("brd: add %s.%s: %w", table, column, err)
	}
	return nil
}
