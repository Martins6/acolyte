package commtg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInboundNotFound means no inbound row matches the envelope.
var ErrInboundNotFound = errors.New("comm_tg: inbound envelope not found")

// InboxItem is one parsed inbound Telegram update. Conversation text is not
// stored after admission; pending_envelope holds the replay payload only while
// the turn is unprocessed.
type InboxItem struct {
	ID                string     `json:"id"`
	TGUpdateID        int64      `json:"tg_update_id"`
	ChatID            int64      `json:"chat_id"`
	EnvelopeID        string     `json:"envelope_id"`
	MediaPath         string     `json:"media_path"`
	OpenCodeSessionID string     `json:"opencode_session_id"`
	OpenCodeMessageID string     `json:"opencode_message_id"`
	PendingEnvelope   string     `json:"-"`
	ProcessedAt       *time.Time `json:"processed_at,omitempty"`
	Attempts          int        `json:"attempts,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	ReceivedAt        time.Time  `json:"received_at"`
}

const inboxColumns = `id, tg_update_id, chat_id, envelope_id, media_path,
	opencode_session_id, opencode_message_id, pending_envelope, processed_at,
	attempts, last_error, received_at`

// RecordInbound stores a pending inbound update and reports whether it was new.
// Duplicate Telegram update IDs are ignored; a duplicate envelope ID is an
// error because it would make envelope-to-session linkage ambiguous.
func (s *Store) RecordInbound(item InboxItem) (bool, error) {
	if item.TGUpdateID <= 0 {
		return false, fmt.Errorf("comm_tg: telegram update id is required")
	}
	if item.ChatID == 0 {
		return false, fmt.Errorf("comm_tg: chat id is required")
	}
	if strings.TrimSpace(item.EnvelopeID) == "" {
		return false, fmt.Errorf("comm_tg: envelope id is required")
	}
	if item.ID == "" {
		id, err := newID("comm_in_")
		if err != nil {
			return false, err
		}
		item.ID = id
	}
	if item.ReceivedAt.IsZero() {
		item.ReceivedAt = time.Now().UTC()
	}
	result, err := s.db.Exec(`INSERT INTO comm_tg_inbox
		(id, tg_update_id, chat_id, envelope_id, media_path,
		 opencode_session_id, opencode_message_id, pending_envelope,
		 processed_at, attempts, last_error, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, '', ?)
		ON CONFLICT(tg_update_id) DO NOTHING`,
		item.ID, item.TGUpdateID, item.ChatID, item.EnvelopeID, item.MediaPath,
		item.OpenCodeSessionID, item.OpenCodeMessageID, item.PendingEnvelope, item.ReceivedAt,
	)
	if err != nil {
		return false, fmt.Errorf("comm_tg: record inbound %d: %w", item.TGUpdateID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("comm_tg: record inbound %d: %w", item.TGUpdateID, err)
	}
	return affected > 0, nil
}

// LinkInbound attaches the admitted OpenCode session and message to an inbound
// envelope after the turn is admitted.
func (s *Store) LinkInbound(envelopeID, opencodeSessionID, opencodeMessageID string) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET opencode_session_id = ?, opencode_message_id = ?
		WHERE envelope_id = ?`, opencodeSessionID, opencodeMessageID, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: link inbound %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: link inbound %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// MarkInboundProcessed clears the replay payload and records admission. The
// token must match the live claim.
func (s *Store) MarkInboundProcessed(envelopeID, token string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET processed_at = ?, pending_envelope = '', last_error = '', claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL AND claim_token = ?`, at, envelopeID, token)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound processed %q: %w", envelopeID, err)
	}
	return nil
}

// MarkInboundProcessedUnconditional is used for rows already linked to an
// OpenCode message (or pre-replay rows) before a claim is taken.
func (s *Store) MarkInboundProcessedUnconditional(envelopeID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET processed_at = ?, pending_envelope = '', last_error = '', claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL`, at, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound processed %q: %w", envelopeID, err)
	}
	return nil
}

// RecordInboundFailure records a failed admission attempt and leaves the row
// pending for replay. The token must match the live claim.
func (s *Store) RecordInboundFailure(envelopeID, token, errText string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET attempts = attempts + 1, last_error = ?, claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL AND (claim_token = ? OR ? = '')`,
		errText, envelopeID, token, token)
	if err != nil {
		return fmt.Errorf("comm_tg: record inbound failure %q: %w", envelopeID, err)
	}
	return nil
}

// ClaimInbound atomically leases an unprocessed row for one admission attempt.
// It returns false when another caller holds a live lease or the row is done.
func (s *Store) ClaimInbound(envelopeID, token string, now, leaseUntil time.Time) (bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if leaseUntil.IsZero() {
		leaseUntil = now.Add(2 * time.Minute)
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox SET claimed_until = ?, claim_token = ?
		WHERE envelope_id = ? AND processed_at IS NULL
		AND (claimed_until IS NULL OR claimed_until <= ?)`,
		leaseUntil, token, envelopeID, now)
	if err != nil {
		return false, fmt.Errorf("comm_tg: claim inbound %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("comm_tg: claim inbound %q: %w", envelopeID, err)
	}
	return affected > 0, nil
}

// RenewInboundClaim extends a live claim; false means the claim was lost or
// the row was completed by another worker.
func (s *Store) RenewInboundClaim(envelopeID, token string, leaseUntil time.Time) (bool, error) {
	if leaseUntil.IsZero() {
		leaseUntil = time.Now().UTC().Add(2 * time.Minute)
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox SET claimed_until = ?
		WHERE envelope_id = ? AND processed_at IS NULL AND claim_token = ?`,
		leaseUntil, envelopeID, token)
	if err != nil {
		return false, fmt.Errorf("comm_tg: renew inbound claim %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("comm_tg: renew inbound claim %q: %w", envelopeID, err)
	}
	return affected > 0, nil
}

// PendingInbound returns unprocessed, unleased rows in chronological order.
func (s *Store) PendingInbound(now time.Time, limit int) ([]InboxItem, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+inboxColumns+` FROM comm_tg_inbox
		WHERE processed_at IS NULL AND (claimed_until IS NULL OR claimed_until <= ?)
		ORDER BY received_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list pending inbound: %w", err)
	}
	defer rows.Close()
	return scanInbox(rows)
}

// ListInbox returns inbound rows for a chat in chronological order.
func (s *Store) ListInbox(chatID int64) ([]InboxItem, error) {
	rows, err := s.db.Query(`SELECT `+inboxColumns+` FROM comm_tg_inbox
		WHERE chat_id = ? ORDER BY received_at, id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list inbox: %w", err)
	}
	defer rows.Close()
	return scanInbox(rows)
}

// GetInboxByEnvelope returns one inbound row.
func (s *Store) GetInboxByEnvelope(envelopeID string) (InboxItem, bool, error) {
	var item InboxItem
	var processedAt sql.NullTime
	err := s.db.QueryRow(`SELECT `+inboxColumns+` FROM comm_tg_inbox
		WHERE envelope_id = ?`, envelopeID).Scan(
		&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID, &item.MediaPath,
		&item.OpenCodeSessionID, &item.OpenCodeMessageID, &item.PendingEnvelope,
		&processedAt, &item.Attempts, &item.LastError, &item.ReceivedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxItem{}, false, nil
	}
	if err != nil {
		return InboxItem{}, false, fmt.Errorf("comm_tg: get inbox %q: %w", envelopeID, err)
	}
	item.ProcessedAt = timePtr(processedAt)
	return item, true, nil
}

func scanInbox(rows *sql.Rows) ([]InboxItem, error) {
	var items []InboxItem
	for rows.Next() {
		var item InboxItem
		var processedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID,
			&item.MediaPath, &item.OpenCodeSessionID, &item.OpenCodeMessageID,
			&item.PendingEnvelope, &processedAt, &item.Attempts, &item.LastError,
			&item.ReceivedAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan inbox: %w", err)
		}
		item.ProcessedAt = timePtr(processedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: scan inbox: %w", err)
	}
	return items, nil
}
