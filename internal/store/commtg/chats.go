package commtg

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Chat is one resolved Telegram chat.
type Chat struct {
	ChatID      int64     `json:"chat_id"`
	Username    string    `json:"username"`
	Allowed     bool      `json:"allowed"`
	Private     bool      `json:"private"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// UpsertChat records or refreshes a chat seen by the adapter. Only private
// chats are eligible as user-notification destinations.
func (s *Store) UpsertChat(chatID int64, username string, allowed, private bool, at time.Time) error {
	if chatID == 0 {
		return fmt.Errorf("comm_tg: chat id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO comm_tg_chats
		(chat_id, username, allowed, private, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			username = excluded.username,
			allowed = excluded.allowed,
			private = excluded.private,
			last_seen_at = excluded.last_seen_at`,
		chatID, username, boolInt(allowed), boolInt(private), at, at,
	)
	if err != nil {
		return fmt.Errorf("comm_tg: upsert chat %d: %w", chatID, err)
	}
	return nil
}

// GetChat returns one chat by ID.
func (s *Store) GetChat(chatID int64) (Chat, bool, error) {
	var chat Chat
	var allowed, private int
	err := s.db.QueryRow(`SELECT chat_id, username, allowed, private, first_seen_at, last_seen_at
		FROM comm_tg_chats WHERE chat_id = ?`, chatID).Scan(
		&chat.ChatID, &chat.Username, &allowed, &private, &chat.FirstSeenAt, &chat.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, fmt.Errorf("comm_tg: get chat %d: %w", chatID, err)
	}
	chat.Allowed = allowed != 0
	chat.Private = private != 0
	return chat, true, nil
}

// LatestAllowedChat returns the most recently active allowed private chat.
// Groups and channels are excluded so notifications cannot leak to members.
func (s *Store) LatestAllowedChat() (Chat, bool, error) {
	var chat Chat
	var allowed, private int
	err := s.db.QueryRow(`SELECT chat_id, username, allowed, private, first_seen_at, last_seen_at
		FROM comm_tg_chats WHERE allowed = 1 AND private = 1
		ORDER BY last_seen_at DESC, chat_id DESC LIMIT 1`).Scan(
		&chat.ChatID, &chat.Username, &allowed, &private, &chat.FirstSeenAt, &chat.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, fmt.Errorf("comm_tg: get latest chat: %w", err)
	}
	chat.Allowed = allowed != 0
	chat.Private = private != 0
	return chat, true, nil
}

// ListChats returns every known chat, newest first.
func (s *Store) ListChats() ([]Chat, error) {
	rows, err := s.db.Query(`SELECT chat_id, username, allowed, private, first_seen_at, last_seen_at
		FROM comm_tg_chats ORDER BY last_seen_at DESC, chat_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list chats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var chat Chat
		var allowed, private int
		if err := rows.Scan(&chat.ChatID, &chat.Username, &allowed, &private, &chat.FirstSeenAt, &chat.LastSeenAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan chat: %w", err)
		}
		chat.Allowed = allowed != 0
		chat.Private = private != 0
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: list chats: %w", err)
	}
	return chats, nil
}

// LastMessageAt returns the newest communication timestamp: the latest inbound
// received_at or the latest delivered outbound sent_at. Pending, failed, or
// otherwise unsent outbound rows do not count. ok is false when neither table
// holds a timestamp.
func (s *Store) LastMessageAt() (time.Time, bool, error) {
	inbound, inboundOK, err := s.lastTimestamp(`SELECT received_at FROM comm_tg_inbox
		ORDER BY julianday(received_at) DESC LIMIT 1`)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("comm_tg: last inbound message: %w", err)
	}
	outbound, outboundOK, err := s.lastTimestamp(`SELECT sent_at FROM comm_tg_outbox
		WHERE sent_at IS NOT NULL ORDER BY julianday(sent_at) DESC LIMIT 1`)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("comm_tg: last outbound message: %w", err)
	}
	switch {
	case !inboundOK && !outboundOK:
		return time.Time{}, false, nil
	case !inboundOK:
		return outbound.UTC(), true, nil
	case !outboundOK || !outbound.After(inbound):
		return inbound.UTC(), true, nil
	default:
		return outbound.UTC(), true, nil
	}
}

func (s *Store) lastTimestamp(query string) (time.Time, bool, error) {
	var value time.Time
	err := s.db.QueryRow(query).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return value, true, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
