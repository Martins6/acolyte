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
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// UpsertChat records or refreshes a chat seen by the adapter.
func (s *Store) UpsertChat(chatID int64, username string, allowed bool, at time.Time) error {
	if chatID == 0 {
		return fmt.Errorf("comm_tg: chat id is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO comm_tg_chats
		(chat_id, username, allowed, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			username = excluded.username,
			allowed = excluded.allowed,
			last_seen_at = excluded.last_seen_at`,
		chatID, username, boolInt(allowed), at, at,
	)
	if err != nil {
		return fmt.Errorf("comm_tg: upsert chat %d: %w", chatID, err)
	}
	return nil
}

// GetChat returns one chat by ID.
func (s *Store) GetChat(chatID int64) (Chat, bool, error) {
	var chat Chat
	var allowed int
	err := s.db.QueryRow(`SELECT chat_id, username, allowed, first_seen_at, last_seen_at
		FROM comm_tg_chats WHERE chat_id = ?`, chatID).Scan(
		&chat.ChatID, &chat.Username, &allowed, &chat.FirstSeenAt, &chat.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, fmt.Errorf("comm_tg: get chat %d: %w", chatID, err)
	}
	chat.Allowed = allowed != 0
	return chat, true, nil
}

// LatestAllowedChat returns the most recently active allowed chat.
func (s *Store) LatestAllowedChat() (Chat, bool, error) {
	var chat Chat
	var allowed int
	err := s.db.QueryRow(`SELECT chat_id, username, allowed, first_seen_at, last_seen_at
		FROM comm_tg_chats WHERE allowed = 1
		ORDER BY last_seen_at DESC, chat_id DESC LIMIT 1`).Scan(
		&chat.ChatID, &chat.Username, &allowed, &chat.FirstSeenAt, &chat.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, fmt.Errorf("comm_tg: get latest chat: %w", err)
	}
	chat.Allowed = allowed != 0
	return chat, true, nil
}

// ListChats returns every known chat, newest first.
func (s *Store) ListChats() ([]Chat, error) {
	rows, err := s.db.Query(`SELECT chat_id, username, allowed, first_seen_at, last_seen_at
		FROM comm_tg_chats ORDER BY last_seen_at DESC, chat_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list chats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var chat Chat
		var allowed int
		if err := rows.Scan(&chat.ChatID, &chat.Username, &allowed, &chat.FirstSeenAt, &chat.LastSeenAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan chat: %w", err)
		}
		chat.Allowed = allowed != 0
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: list chats: %w", err)
	}
	return chats, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
