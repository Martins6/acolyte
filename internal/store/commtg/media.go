package commtg

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MediaItem is one downloaded Telegram file.
type MediaItem struct {
	TGFileID string    `json:"tg_file_id"`
	FilePath string    `json:"file_path"`
	Type     string    `json:"type"`
	ChatID   int64     `json:"chat_id"`
	SavedAt  time.Time `json:"saved_at"`
}

// RecordMedia stores or refreshes a downloaded file reference.
func (s *Store) RecordMedia(item MediaItem) error {
	if strings.TrimSpace(item.TGFileID) == "" {
		return fmt.Errorf("comm_tg: telegram file id is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return fmt.Errorf("comm_tg: media path is required")
	}
	if item.SavedAt.IsZero() {
		item.SavedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO comm_tg_media
		(tg_file_id, file_path, type, chat_id, saved_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tg_file_id) DO UPDATE SET
			file_path = excluded.file_path,
			type = excluded.type,
			chat_id = excluded.chat_id,
			saved_at = excluded.saved_at`,
		item.TGFileID, item.FilePath, item.Type, item.ChatID, item.SavedAt,
	)
	if err != nil {
		return fmt.Errorf("comm_tg: record media %q: %w", item.TGFileID, err)
	}
	return nil
}

// ListMedia returns every downloaded Telegram file in save order.
func (s *Store) ListMedia() ([]MediaItem, error) {
	rows, err := s.db.Query(`SELECT tg_file_id, file_path, type, chat_id, saved_at
		FROM comm_tg_media ORDER BY saved_at, tg_file_id`)
	if err != nil {
		return nil, fmt.Errorf("comm_tg: list media: %w", err)
	}
	defer rows.Close()

	var items []MediaItem
	for rows.Next() {
		var item MediaItem
		if err := rows.Scan(&item.TGFileID, &item.FilePath, &item.Type, &item.ChatID, &item.SavedAt); err != nil {
			return nil, fmt.Errorf("comm_tg: scan media: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comm_tg: list media: %w", err)
	}
	return items, nil
}

// GetMedia returns a downloaded file by Telegram file ID.
func (s *Store) GetMedia(tgFileID string) (MediaItem, bool, error) {
	var item MediaItem
	err := s.db.QueryRow(`SELECT tg_file_id, file_path, type, chat_id, saved_at
		FROM comm_tg_media WHERE tg_file_id = ?`, strings.TrimSpace(tgFileID)).Scan(
		&item.TGFileID, &item.FilePath, &item.Type, &item.ChatID, &item.SavedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MediaItem{}, false, nil
	}
	if err != nil {
		return MediaItem{}, false, fmt.Errorf("comm_tg: get media %q: %w", tgFileID, err)
	}
	return item, true, nil
}
