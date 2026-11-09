package orch

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Session maps a conversation/thread key to an OpenCode session.
type Session struct {
	ID                string    `json:"id"`
	Thread            string    `json:"thread"`
	OpenCodeSessionID string    `json:"opencode_session_id"`
	CreatedAt         time.Time `json:"created_at"`
	LastActiveAt      time.Time `json:"last_active_at"`
}

// ErrSessionNotFound means no session matches the thread.
var ErrSessionNotFound = errors.New("orch: session not found")

const sessionColumns = `id, thread, opencode_session_id, created_at, last_active_at`

// GetSessionByThread returns the mapping for a thread.
func (s *Store) GetSessionByThread(thread string) (Session, bool, error) {
	thread = strings.TrimSpace(thread)
	if thread == "" {
		return Session{}, false, fmt.Errorf("orch: session thread is required")
	}
	var session Session
	err := s.db.QueryRow(`SELECT `+sessionColumns+` FROM orch_sessions WHERE thread = ?`, thread).Scan(
		&session.ID, &session.Thread, &session.OpenCodeSessionID, &session.CreatedAt, &session.LastActiveAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("orch: get session %q: %w", thread, err)
	}
	return session, true, nil
}

// LatestSession returns the most recently active session.
func (s *Store) LatestSession() (Session, bool, error) {
	var session Session
	err := s.db.QueryRow(`SELECT `+sessionColumns+` FROM orch_sessions
		ORDER BY last_active_at DESC LIMIT 1`).Scan(
		&session.ID, &session.Thread, &session.OpenCodeSessionID, &session.CreatedAt, &session.LastActiveAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("orch: get latest session: %w", err)
	}
	return session, true, nil
}

// UpsertSession creates or updates the thread mapping and returns it.
func (s *Store) UpsertSession(thread, opencodeSessionID string, at time.Time) (Session, error) {
	thread = strings.TrimSpace(thread)
	opencodeSessionID = strings.TrimSpace(opencodeSessionID)
	if thread == "" {
		return Session{}, fmt.Errorf("orch: session thread is required")
	}
	if opencodeSessionID == "" {
		return Session{}, fmt.Errorf("orch: opencode session id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}

	id := newID()
	_, err := s.db.Exec(`INSERT INTO orch_sessions
		(id, thread, opencode_session_id, created_at, last_active_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(thread) DO UPDATE SET
			opencode_session_id = excluded.opencode_session_id,
			last_active_at = excluded.last_active_at`,
		id, thread, opencodeSessionID, at, at,
	)
	if err != nil {
		return Session{}, fmt.Errorf("orch: upsert session %q: %w", thread, err)
	}

	session, ok, err := s.GetSessionByThread(thread)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return session, nil
}

// TouchSession updates last_active_at for an existing thread.
func (s *Store) TouchSession(thread string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE orch_sessions SET last_active_at = ? WHERE thread = ?`, at, strings.TrimSpace(thread))
	if err != nil {
		return fmt.Errorf("orch: touch session %q: %w", thread, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("orch: touch session %q: %w", thread, err)
	}
	if affected == 0 {
		return ErrSessionNotFound
	}
	return nil
}
