// Package envelope defines the single message type exchanged between
// Communication adapters and the Orchestrator (SPEC §7).
package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Direction is the flow of an envelope.
type Direction string

const (
	DirectionIn  Direction = "in"
	DirectionOut Direction = "out"
)

// PartyKind identifies who sent or receives an envelope.
type PartyKind string

const (
	PartyUser   PartyKind = "user"
	PartyAgent  PartyKind = "agent"
	PartySystem PartyKind = "system"
)

// ContentType selects the payload carried by an envelope.
type ContentType string

const (
	ContentText  ContentType = "text"
	ContentMedia ContentType = "media"
	ContentEvent ContentType = "event"
)

// Party is one side of an exchange.
type Party struct {
	Kind PartyKind `json:"kind"`
	ID   string    `json:"id,omitempty"`
	Name string    `json:"name,omitempty"`
}

// Content is the payload of an envelope. Media paths are absolute and already
// resolved by the Communication adapter.
type Content struct {
	Type  ContentType `json:"type"`
	Text  string      `json:"text,omitempty"`
	Media []string    `json:"media,omitempty"`
}

// Envelope is the universal message between Communication and Orchestrator.
type Envelope struct {
	ID        string         `json:"id"`
	Direction Direction      `json:"direction"`
	Channel   string         `json:"channel"`
	Thread    string         `json:"thread"`
	From      Party          `json:"from"`
	To        Party          `json:"to"`
	Content   Content        `json:"content"`
	Meta      map[string]any `json:"meta,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// New builds an envelope with a fresh ID and UTC timestamp.
func New(direction Direction, channel, thread string, from, to Party, content Content) Envelope {
	return Envelope{
		ID:        NewID(),
		Direction: direction,
		Channel:   strings.TrimSpace(channel),
		Thread:    strings.TrimSpace(thread),
		From:      from,
		To:        to,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
}

// NewID returns an opaque envelope identifier.
func NewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "env_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "env_" + hex.EncodeToString(raw[:])
}

// Validate checks the required fields for an envelope.
func (e Envelope) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("envelope: id is required")
	}
	switch e.Direction {
	case DirectionIn, DirectionOut:
	default:
		return fmt.Errorf("envelope: invalid direction %q", e.Direction)
	}
	if strings.TrimSpace(e.Channel) == "" {
		return fmt.Errorf("envelope: channel is required")
	}
	if strings.TrimSpace(e.Thread) == "" {
		return fmt.Errorf("envelope: thread is required")
	}
	if err := validateParty("from", e.From); err != nil {
		return err
	}
	if err := validateParty("to", e.To); err != nil {
		return err
	}
	switch e.Content.Type {
	case ContentText, ContentMedia, ContentEvent:
	default:
		return fmt.Errorf("envelope: invalid content type %q", e.Content.Type)
	}
	if (e.Content.Type == ContentText || e.Content.Type == ContentMedia) &&
		strings.TrimSpace(e.Content.Text) == "" && len(e.Content.Media) == 0 {
		return fmt.Errorf("envelope: %s content requires text or media", e.Content.Type)
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("envelope: created_at is required")
	}
	return nil
}

func validateParty(name string, party Party) error {
	switch party.Kind {
	case PartyUser, PartyAgent, PartySystem:
	default:
		return fmt.Errorf("envelope: invalid %s.kind %q", name, party.Kind)
	}
	return nil
}

// TelegramThread returns the canonical thread key for a Telegram chat.
func TelegramThread(chatID int64) string {
	return "telegram:" + strconv.FormatInt(chatID, 10)
}

// ParseTelegramThread extracts a chat ID from a Telegram thread key.
func ParseTelegramThread(thread string) (int64, bool) {
	thread = strings.TrimSpace(thread)
	if !strings.HasPrefix(thread, "telegram:") {
		return 0, false
	}
	raw := strings.TrimPrefix(thread, "telegram:")
	if raw == "" {
		return 0, false
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return chatID, true
}
