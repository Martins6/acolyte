package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewAndValidate(t *testing.T) {
	env := New(DirectionIn, "telegram", TelegramThread(123), Party{Kind: PartyUser, ID: "123", Name: "alice"}, Party{Kind: PartyAgent, ID: "valet"}, Content{Type: ContentText, Text: "hello"})
	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
	if !strings.HasPrefix(env.ID, "env_") {
		t.Errorf("ID = %q, want env_ prefix", env.ID)
	}
	if env.CreatedAt.IsZero() || env.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v, want non-zero UTC", env.CreatedAt)
	}
	if env.Thread != "telegram:123" {
		t.Errorf("Thread = %q", env.Thread)
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	base := New(DirectionIn, "telegram", "telegram:1", Party{Kind: PartyUser}, Party{Kind: PartyAgent}, Content{Type: ContentText, Text: "x"})
	cases := []func(*Envelope){
		func(e *Envelope) { e.ID = "" },
		func(e *Envelope) { e.Direction = "sideways" },
		func(e *Envelope) { e.Channel = "" },
		func(e *Envelope) { e.Thread = "" },
		func(e *Envelope) { e.From.Kind = "robot" },
		func(e *Envelope) { e.To.Kind = "robot" },
		func(e *Envelope) { e.Content.Type = "binary" },
		func(e *Envelope) { e.Content = Content{Type: ContentText} },
		func(e *Envelope) { e.Content = Content{Type: ContentMedia, Media: []string{"relative.jpg"}} },
		func(e *Envelope) { e.Content = Content{Type: ContentMedia, Media: []string{""}} },
		func(e *Envelope) { e.CreatedAt = time.Time{} },
	}
	for index, mutate := range cases {
		env := base
		mutate(&env)
		if err := env.Validate(); err == nil {
			t.Errorf("case %d: Validate() error = nil, want error", index)
		}
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	env := New(DirectionOut, "telegram", "telegram:-42", Party{Kind: PartyAgent, ID: "valet"}, Party{Kind: PartyUser, ID: "-42"}, Content{Type: ContentMedia, Text: "caption", Media: []string{"/tmp/a.jpg"}})
	env.Meta = map[string]any{"reply_to": float64(7)}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded Validate() error: %v", err)
	}
	if decoded.ID != env.ID || decoded.Content.Type != ContentMedia || len(decoded.Content.Media) != 1 {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestTelegramThread(t *testing.T) {
	for _, chatID := range []int64{1, 123456789, -1001234567890} {
		thread := TelegramThread(chatID)
		got, ok := ParseTelegramThread(thread)
		if !ok || got != chatID {
			t.Errorf("ParseTelegramThread(%q) = %d, %v; want %d, true", thread, got, ok, chatID)
		}
	}
	for _, thread := range []string{"", "telegram:", "telegram:abc", "slack:1", "telegram"} {
		if _, ok := ParseTelegramThread(thread); ok {
			t.Errorf("ParseTelegramThread(%q) = true, want false", thread)
		}
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
}
