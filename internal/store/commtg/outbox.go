package commtg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrOutboxNotFound means no outbound row matches the ID.
var ErrOutboxNotFound = errors.New("comm_tg: outbox item not found")

// OutboxItem is one durable outbound Telegram message.
type OutboxItem struct {
	ID                string     `json:"id"`
	EnvelopeID        string     `json:"envelope_id"`
	ChatID            int64      `json:"chat_id"`
	Text              string     `json:"text"`
	ReplyTo           *int64     `json:"reply_to,omitempty"`
	ChunksSent        int        `json:"chunks_sent"`
	Attempts          int        `json:"attempts"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	Delivery          string     `json:"delivery"`
	TGMessageID       *int64     `json:"tg_message_id,omitempty"`
	OpenCodeSessionID string     `json:"opencode_session_id,omitempty"`
	SourceMessageID   string     `json:"source_message_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	SentAt            *time.Time `json:"sent_at,omitempty"`
}

const outboxColumns = `id, envelope_id, chat_id, text, reply_to, chunks_sent,
	attempts, next_attempt_at, last_error, delivery, tg_message_id,
	opencode_session_id, source_message_id, created_at, sent_at`

// EnqueueOutbound inserts a pending outbound message.
func (s *Store) EnqueueOutbound(item OutboxItem) (OutboxItem, error) {
	if strings.TrimSpace(item.EnvelopeID) == "" {
		return OutboxItem{}, fmt.Errorf("comm_tg: envelope id is required")
	}
	if item.ChatID == 0 {
		return OutboxItem{}, fmt.Errorf("comm_tg: chat id is required")
	}
	if strings.TrimSpace(item.Text) == "" {
		return OutboxItem{}, fmt.Errorf("comm_tg: outbound text is required")
	}
	if item.ID == "" {
		id, err := newID("comm_out_")
		if err != nil {
			return OutboxItem{}, err
		}
		item.ID = id
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.Delivery == "" {
		item.Delivery = "pending"
	}
	if item.NextAttemptAt == nil {
		next := item.CreatedAt
		item.NextAttemptAt = &next
	}
	_, err := s.db.Exec(`INSERT INTO comm_tg_outbox
		(id, envelope_id, chat_id, text, reply_to, chunks_sent, attempts,
		 next_attempt_at, last_error, delivery, tg_message_id,
		 opencode_session_id, source_message_id, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.EnvelopeID, item.ChatID, item.Text, nullableInt64(item.ReplyTo),
		item.ChunksSent, item.Attempts, nullableTime(item.NextAttemptAt), item.LastError,
		item.Delivery, nullableInt64(item.TGMessageID), item.OpenCodeSessionID,
		item.SourceMessageID, item.CreatedAt, nullableTime(item.SentAt),
	)
	if err != nil {
		return OutboxItem{}, fmt.Errorf("comm_tg: enqueue outbound: %w", err)
	}
	return item, nil
}

// DueOutbound returns pending rows whose next attempt is due.
func (s *Store) DueOutbound(now time.Time, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+outboxColumns+` FROM comm_tg_outbox
		WHERE delivery = 'pending' AND COALESCE(next_attempt_at, created_at) <= ?
		ORDER BY created_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list due outbox: %w", err)
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// ListOutbox returns outbound rows for a chat in chronological order.
func (s *Store) ListOutbox(chatID int64) ([]OutboxItem, error) {
	rows, err := s.db.Query(`SELECT `+outboxColumns+` FROM comm_tg_outbox
		WHERE chat_id = ? ORDER BY created_at, id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list outbox: %w", err)
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// MarkOutboundProgress records successfully sent chunks without completing the
// row.
func (s *Store) MarkOutboundProgress(id string, chunksSent int, tgMessageID int64) error {
	if chunksSent < 0 {
		return fmt.Errorf("comm_tg: chunks sent must be >= 0")
	}
	result, err := s.db.Exec(`UPDATE comm_tg_outbox
		SET chunks_sent = ?, tg_message_id = ? WHERE id = ? AND delivery = 'pending'`,
		chunksSent, tgMessageID, id)
	if err != nil {
		return fmt.Errorf("comm_tg: mark outbound progress: %w", err)
	}
	return requireAffected(result, id)
}

// MarkOutboundSent marks the row delivered.
func (s *Store) MarkOutboundSent(id string, chunksSent int, tgMessageID int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE comm_tg_outbox
		SET delivery = 'sent', chunks_sent = ?, tg_message_id = ?,
			sent_at = ?, next_attempt_at = NULL, last_error = '', text = ''
		WHERE id = ? AND delivery = 'pending'`, chunksSent, tgMessageID, at, id)
	if err != nil {
		return fmt.Errorf("comm_tg: mark outbound sent: %w", err)
	}
	return requireAffected(result, id)
}

// MarkOutboundRetry schedules the next delivery attempt.
func (s *Store) MarkOutboundRetry(id string, attempts int, next time.Time, errText string) error {
	if attempts < 0 {
		return fmt.Errorf("comm_tg: attempts must be >= 0")
	}
	if next.IsZero() {
		next = time.Now().UTC()
	}
	result, err := s.db.Exec(`UPDATE comm_tg_outbox
		SET attempts = ?, next_attempt_at = ?, last_error = ?, delivery = 'pending'
		WHERE id = ? AND delivery = 'pending'`, attempts, next, errText, id)
	if err != nil {
		return fmt.Errorf("comm_tg: mark outbound retry: %w", err)
	}
	return requireAffected(result, id)
}

// MarkOutboundFailed marks the row permanently failed.
func (s *Store) MarkOutboundFailed(id, errText string) error {
	result, err := s.db.Exec(`UPDATE comm_tg_outbox
		SET delivery = 'failed', last_error = ?, next_attempt_at = NULL, text = ''
		WHERE id = ? AND delivery = 'pending'`, errText, id)
	if err != nil {
		return fmt.Errorf("comm_tg: mark outbound failed: %w", err)
	}
	return requireAffected(result, id)
}

func requireAffected(result sql.Result, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("comm_tg: outbox %q: %w", id, err)
	}
	if affected == 0 {
		return ErrOutboxNotFound
	}
	return nil
}

func scanOutbox(rows *sql.Rows) ([]OutboxItem, error) {
	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		var replyTo, tgMessageID sql.NullInt64
		var nextAttempt, sentAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.EnvelopeID, &item.ChatID, &item.Text, &replyTo,
			&item.ChunksSent, &item.Attempts, &nextAttempt, &item.LastError, &item.Delivery,
			&tgMessageID, &item.OpenCodeSessionID, &item.SourceMessageID, &item.CreatedAt,
			&sentAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan outbox: %w", err)
		}
		item.ReplyTo = int64Ptr(replyTo)
		item.TGMessageID = int64Ptr(tgMessageID)
		item.NextAttemptAt = timePtr(nextAttempt)
		item.SentAt = timePtr(sentAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: scan outbox: %w", err)
	}
	return items, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}
