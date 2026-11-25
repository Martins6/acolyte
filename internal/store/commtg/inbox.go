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
	PendingReply      string     `json:"-"`
	AdmittedAt        *time.Time `json:"admitted_at,omitempty"`
	ProcessedAt       *time.Time `json:"processed_at,omitempty"`
	Attempts          int        `json:"attempts,omitempty"`
	ResumeAttempts    int        `json:"resume_attempts,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	ReceivedAt        time.Time  `json:"received_at"`
}

const inboxColumns = `id, tg_update_id, chat_id, envelope_id, media_path,
	opencode_session_id, opencode_message_id, pending_envelope, pending_reply,
	admitted_at, processed_at, attempts, resume_attempts, last_error, received_at`

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
		 pending_reply, admitted_at, processed_at, attempts, resume_attempts,
		 last_error, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, 0, '', ?)
		ON CONFLICT(tg_update_id) DO NOTHING`,
		item.ID, item.TGUpdateID, item.ChatID, item.EnvelopeID, item.MediaPath,
		item.OpenCodeSessionID, item.OpenCodeMessageID, item.PendingEnvelope,
		item.PendingReply, item.AdmittedAt, item.ReceivedAt,
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

// SetInboundMedia records the downloaded media path for an inbound envelope.
func (s *Store) SetInboundMedia(envelopeID, mediaPath string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox SET media_path = ? WHERE envelope_id = ?`, mediaPath, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: set inbound media %q: %w", envelopeID, err)
	}
	return nil
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

// MarkInboundAdmitted records that the inbound's turn was durably admitted to
// OpenCode, storing the session and marker needed to re-collect it later. It is
// idempotent and never resurrects a processed row. Callers are expected to hold
// the inbound claim; the processed_at guard keeps a late write from reopening a
// finished message.
func (s *Store) MarkInboundAdmitted(envelopeID, opencodeSessionID, opencodeMessageID string, at time.Time) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET opencode_session_id = ?, opencode_message_id = ?, admitted_at = ?
		WHERE envelope_id = ? AND processed_at IS NULL`,
		opencodeSessionID, opencodeMessageID, at, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admitted %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admitted %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// MarkInboundAdmissionStarted records admission intent before OpenCode is
// called. If every later marker write fails, admitted_at is the durable signal
// that replay must not re-admit the message: the prompt may already be running.
func (s *Store) MarkInboundAdmissionStarted(envelopeID string, at time.Time) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox SET admitted_at = ?
		WHERE envelope_id = ? AND processed_at IS NULL`, at, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admission started %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admission started %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// ClearInboundAdmission removes the admission intent when the turn never
// reached OpenCode, so replay may admit it normally. It never touches a row
// that already has a marker.
func (s *Store) ClearInboundAdmission(envelopeID string) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox SET admitted_at = NULL
		WHERE envelope_id = ? AND processed_at IS NULL AND opencode_message_id = ''`, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: clear inbound admission %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: clear inbound admission %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// MarkInboundAdmissionIndeterminate records that OpenCode accepted an input
// whose marker could not be persisted. admitted_at without a marker is the
// durable signal that replay must not re-admit the message: the turn may be
// running, so a second prompt could duplicate its work.
func (s *Store) MarkInboundAdmissionIndeterminate(envelopeID, opencodeSessionID string, at time.Time) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET opencode_session_id = ?, admitted_at = ?,
		    last_error = 'admission confirmed; marker not persisted'
		WHERE envelope_id = ? AND processed_at IS NULL`,
		opencodeSessionID, at, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admission indeterminate %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound admission indeterminate %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// RecordResumeAttempt counts a recovery attempt and clears the admission
// marker so the row becomes unadmitted again: the next replay pass admits it
// into a fresh session instead of trying to collect the abandoned turn.
func (s *Store) RecordResumeAttempt(envelopeID, reason string) error {
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return fmt.Errorf("comm_tg: envelope id is required")
	}
	result, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET resume_attempts = resume_attempts + 1,
		    opencode_session_id = '', opencode_message_id = '',
		    admitted_at = NULL, last_error = ?
		WHERE envelope_id = ? AND processed_at IS NULL`, reason, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: record resume attempt %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: record resume attempt %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// SetInboundReply stores a turn reply that could not be enqueued yet.
func (s *Store) SetInboundReply(envelopeID, reply string) error {
	result, err := s.db.Exec(`UPDATE comm_tg_inbox SET pending_reply = ?
		WHERE envelope_id = ? AND processed_at IS NULL`, reply, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: set inbound reply %q: %w", envelopeID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: set inbound reply %q: %w", envelopeID, err)
	}
	if affected == 0 {
		return ErrInboundNotFound
	}
	return nil
}

// ClearInboundReply clears a recovered reply.
func (s *Store) ClearInboundReply(envelopeID string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox SET pending_reply = ''
		WHERE envelope_id = ? AND processed_at IS NULL`, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: clear inbound reply %q: %w", envelopeID, err)
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
		SET processed_at = ?, pending_envelope = '', pending_reply = '', last_error = '', claimed_until = NULL, claim_token = ''
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
		SET processed_at = ?, pending_envelope = '', pending_reply = '', last_error = '', claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL`, at, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: mark inbound processed %q: %w", envelopeID, err)
	}
	return nil
}

// ReleaseInboundClaim drops a live claim without counting an attempt, so a
// caller that found nothing to do (a turn still running, for example) can hand
// the row back immediately instead of waiting for the lease to expire.
func (s *Store) ReleaseInboundClaim(envelopeID, token string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox SET claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL AND claim_token = ?`, envelopeID, token)
	if err != nil {
		return fmt.Errorf("comm_tg: release inbound claim %q: %w", envelopeID, err)
	}
	return nil
}

// RecordInboundFailure records a failed admission attempt and leaves the row
// pending for replay. The token must match the live claim.
func (s *Store) RecordInboundFailure(envelopeID, token, errText string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET attempts = attempts + 1, last_error = ?, claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL AND claim_token = ?`,
		errText, envelopeID, token)
	if err != nil {
		return fmt.Errorf("comm_tg: record inbound failure %q: %w", envelopeID, err)
	}
	return nil
}

// RecordInboundFailureUnclaimed records a pre-claim failure without touching a
// claim owned by another worker.
func (s *Store) RecordInboundFailureUnclaimed(envelopeID, errText string) error {
	_, err := s.db.Exec(`UPDATE comm_tg_inbox
		SET attempts = attempts + 1, last_error = ?, claimed_until = NULL, claim_token = ''
		WHERE envelope_id = ? AND processed_at IS NULL AND claim_token = ''`,
		errText, envelopeID)
	if err != nil {
		return fmt.Errorf("comm_tg: record unclaimed inbound failure %q: %w", envelopeID, err)
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

// ListAllInbox returns every inbound row in chronological order.
func (s *Store) ListAllInbox() ([]InboxItem, error) {
	rows, err := s.db.Query(`SELECT ` + inboxColumns + ` FROM comm_tg_inbox
		ORDER BY received_at, id`)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list all inbox: %w", err)
	}
	defer rows.Close()
	return scanInbox(rows)
}

// GetInboxByEnvelope returns one inbound row.
func (s *Store) GetInboxByEnvelope(envelopeID string) (InboxItem, bool, error) {
	var item InboxItem
	var admittedAt, processedAt sql.NullTime
	err := s.db.QueryRow(`SELECT `+inboxColumns+` FROM comm_tg_inbox
		WHERE envelope_id = ?`, envelopeID).Scan(
		&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID, &item.MediaPath,
		&item.OpenCodeSessionID, &item.OpenCodeMessageID, &item.PendingEnvelope, &item.PendingReply,
		&admittedAt, &processedAt, &item.Attempts, &item.ResumeAttempts, &item.LastError, &item.ReceivedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxItem{}, false, nil
	}
	if err != nil {
		return InboxItem{}, false, fmt.Errorf("comm_tg: get inbox %q: %w", envelopeID, err)
	}
	item.AdmittedAt = timePtr(admittedAt)
	item.ProcessedAt = timePtr(processedAt)
	return item, true, nil
}

func scanInbox(rows *sql.Rows) ([]InboxItem, error) {
	var items []InboxItem
	for rows.Next() {
		var item InboxItem
		var admittedAt, processedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID,
			&item.MediaPath, &item.OpenCodeSessionID, &item.OpenCodeMessageID,
			&item.PendingEnvelope, &item.PendingReply, &admittedAt, &processedAt,
			&item.Attempts, &item.ResumeAttempts, &item.LastError,
			&item.ReceivedAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan inbox: %w", err)
		}
		item.AdmittedAt = timePtr(admittedAt)
		item.ProcessedAt = timePtr(processedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: scan inbox: %w", err)
	}
	return items, nil
}
