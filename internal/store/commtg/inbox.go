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

// InboxItem is one parsed inbound Telegram update.
type InboxItem struct {
	ID                string    `json:"id"`
	TGUpdateID        int64     `json:"tg_update_id"`
	ChatID            int64     `json:"chat_id"`
	EnvelopeID        string    `json:"envelope_id"`
	Text              string    `json:"text"`
	MediaPath         string    `json:"media_path"`
	OpenCodeSessionID string    `json:"opencode_session_id"`
	OpenCodeMessageID string    `json:"opencode_message_id"`
	ReceivedAt        time.Time `json:"received_at"`
}

// RecordInbound stores an inbound update and reports whether it was new.
// Duplicate Telegram update IDs are ignored.
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
	result, err := s.db.Exec(`INSERT OR IGNORE INTO comm_tg_inbox
		(id, tg_update_id, chat_id, envelope_id, text, media_path,
		 opencode_session_id, opencode_message_id, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.TGUpdateID, item.ChatID, item.EnvelopeID, item.Text, item.MediaPath,
		item.OpenCodeSessionID, item.OpenCodeMessageID, item.ReceivedAt,
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

// ListInbox returns inbound rows for a chat in chronological order.
func (s *Store) ListInbox(chatID int64) ([]InboxItem, error) {
	rows, err := s.db.Query(`SELECT id, tg_update_id, chat_id, envelope_id, text, media_path,
		opencode_session_id, opencode_message_id, received_at
		FROM comm_tg_inbox WHERE chat_id = ? ORDER BY received_at, id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list inbox: %w", err)
	}
	defer rows.Close()

	var items []InboxItem
	for rows.Next() {
		var item InboxItem
		if err := rows.Scan(&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID,
			&item.Text, &item.MediaPath, &item.OpenCodeSessionID, &item.OpenCodeMessageID,
			&item.ReceivedAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan inbox: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: list inbox: %w", err)
	}
	return items, nil
}

// GetInboxByEnvelope returns one inbound row.
func (s *Store) GetInboxByEnvelope(envelopeID string) (InboxItem, bool, error) {
	var item InboxItem
	err := s.db.QueryRow(`SELECT id, tg_update_id, chat_id, envelope_id, text, media_path,
		opencode_session_id, opencode_message_id, received_at
		FROM comm_tg_inbox WHERE envelope_id = ?`, envelopeID).Scan(
		&item.ID, &item.TGUpdateID, &item.ChatID, &item.EnvelopeID, &item.Text, &item.MediaPath,
		&item.OpenCodeSessionID, &item.OpenCodeMessageID, &item.ReceivedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxItem{}, false, nil
	}
	if err != nil {
		return InboxItem{}, false, fmt.Errorf("comm_tg: get inbox %q: %w", envelopeID, err)
	}
	return item, true, nil
}
