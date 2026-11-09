package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
)

type fakeAPI struct {
	mu           sync.Mutex
	sent         []*bot.SendMessageParams
	sendErr      error
	getFileCalls int
	nextID       int
}

func (f *fakeAPI) SendMessage(_ context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, params)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.nextID++
	return &models.Message{ID: f.nextID}, nil
}

func (f *fakeAPI) GetFile(_ context.Context, _ *bot.GetFileParams) (*models.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getFileCalls++
	return &models.File{FilePath: "files/x.jpg"}, nil
}

func (f *fakeAPI) Token() string { return "token" }

func (f *fakeAPI) messages() []*bot.SendMessageParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*bot.SendMessageParams(nil), f.sent...)
}

func newTestAdapter(t *testing.T, allowed ...string) (*Adapter, *fakeAPI, *store.Store, *time.Time) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	api := &fakeAPI{}
	adapter, err := New(Options{
		Agent:        registry.Agent{Name: "valet", Workspace: ws},
		Store:        st,
		Token:        "token",
		AllowedUsers: allowed,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	adapter.api = api
	return adapter, api, st, &now
}

func TestHandleUpdateAdmitsAllowedUserAndDeduplicates(t *testing.T) {
	adapter, api, st, _ := newTestAdapter(t, "@alice", "42")
	var captured []envelope.Envelope
	adapter.sink = func(env envelope.Envelope) { captured = append(captured, env) }

	update := &models.Update{ID: 100, Message: &models.Message{
		ID:   7,
		From: &models.User{ID: 42, Username: "alice"},
		Chat: models.Chat{ID: 10},
		Text: "hello",
	}}
	adapter.handleUpdate(context.Background(), nil, update)
	adapter.handleUpdate(context.Background(), nil, update)

	if len(captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(captured))
	}
	env := captured[0]
	if env.Content.Text != "hello" || env.Thread != "telegram:10" || env.Channel != "telegram" {
		t.Errorf("envelope = %+v", env)
	}
	if env.Meta["telegram_message_id"] != int64(7) {
		t.Errorf("meta = %+v", env.Meta)
	}
	chat, ok, err := st.CommTG().GetChat(10)
	if err != nil || !ok || chat.Username != "alice" || !chat.Allowed {
		t.Errorf("chat = %+v, %v, %v", chat, ok, err)
	}
	inbox, ok, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil || !ok || inbox.TGUpdateID != 100 {
		t.Errorf("inbox = %+v, %v, %v", inbox, ok, err)
	}
	if len(api.messages()) != 0 {
		t.Errorf("unexpected bot messages: %+v", api.messages())
	}
}

func TestHandleUpdateDeniesUnknownUser(t *testing.T) {
	adapter, api, st, _ := newTestAdapter(t, "@alice")
	called := false
	adapter.sink = func(envelope.Envelope) { called = true }
	update := &models.Update{ID: 101, Message: &models.Message{
		ID:   8,
		From: &models.User{ID: 43, Username: "bob"},
		Chat: models.Chat{ID: 11},
		Text: "hello",
	}}
	adapter.handleUpdate(context.Background(), nil, update)

	if called {
		t.Error("sink was called for an unauthorized user")
	}
	messages := api.messages()
	if len(messages) != 1 || !strings.Contains(messages[0].Text, "not authorized") {
		t.Errorf("messages = %+v", messages)
	}
	if chats, err := st.CommTG().ListChats(); err != nil || len(chats) != 0 {
		t.Errorf("chats = %+v, %v", chats, err)
	}
}

func TestHandleUpdateReusesDownloadedMedia(t *testing.T) {
	adapter, api, st, now := newTestAdapter(t, "42")
	if err := st.CommTG().RecordMedia(commtg.MediaItem{
		TGFileID: "file_1", FilePath: "/tmp/photo.jpg", Type: "images", ChatID: 10, SavedAt: *now,
	}); err != nil {
		t.Fatalf("RecordMedia() error: %v", err)
	}
	var captured []envelope.Envelope
	adapter.sink = func(env envelope.Envelope) { captured = append(captured, env) }
	update := &models.Update{ID: 102, Message: &models.Message{
		ID:      9,
		From:    &models.User{ID: 42, Username: "alice"},
		Chat:    models.Chat{ID: 10},
		Caption: "look",
		Photo:   []models.PhotoSize{{FileID: "file_1", FileSize: 123}},
	}}
	adapter.handleUpdate(context.Background(), nil, update)

	if len(captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(captured))
	}
	env := captured[0]
	if env.Content.Type != envelope.ContentMedia || len(env.Content.Media) != 1 || env.Content.Media[0] != "/tmp/photo.jpg" {
		t.Errorf("content = %+v", env.Content)
	}
	if !strings.Contains(env.Content.Text, "/tmp/photo.jpg") || !strings.Contains(env.Content.Text, "look") {
		t.Errorf("prompt = %q", env.Content.Text)
	}
	if api.getFileCalls != 0 {
		t.Errorf("GetFile calls = %d, want 0 for reused media", api.getFileCalls)
	}
}

func TestSendEnqueuesAndDelivers(t *testing.T) {
	adapter, api, st, _ := newTestAdapter(t, "42")
	env := envelope.New(envelope.DirectionOut, "telegram", envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	env.Meta = map[string]any{
		"reply_to_message_id": int64(7),
		"source_message_id":   "msg_out",
	}
	if err := adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	messages := api.messages()
	if len(messages) != 1 || messages[0].Text != "hello" {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[0].ReplyParameters == nil || messages[0].ReplyParameters.MessageID != 7 {
		t.Errorf("reply parameters = %+v", messages[0].ReplyParameters)
	}
	list, err := st.CommTG().ListOutbox(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("outbox = %+v, %v", list, err)
	}
	if list[0].Delivery != "sent" || list[0].Text != "" || list[0].SourceMessageID != "msg_out" {
		t.Errorf("outbox item = %+v", list[0])
	}
	if due, _ := st.CommTG().DueOutbound(time.Now().Add(time.Hour), 10); len(due) != 0 {
		t.Errorf("due after send = %+v", due)
	}
}

func TestSendChunksLongText(t *testing.T) {
	adapter, api, st, _ := newTestAdapter(t, "42")
	adapter.maxMessageChars = 10
	env := envelope.New(envelope.DirectionOut, "telegram", envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "one two three four"})
	if err := adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	messages := api.messages()
	if len(messages) != 2 || messages[0].Text != "one two" || messages[1].Text != "three four" {
		t.Fatalf("messages = %+v", messages)
	}
	list, _ := st.CommTG().ListOutbox(10)
	if len(list) != 1 || list[0].ChunksSent != 2 || list[0].Delivery != "sent" {
		t.Errorf("outbox = %+v", list)
	}
}

func TestSendRetriesTransientFailure(t *testing.T) {
	adapter, api, st, now := newTestAdapter(t, "42")
	api.sendErr = errors.New("temporary")
	env := envelope.New(envelope.DirectionOut, "telegram", envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if err := adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	list, _ := st.CommTG().ListOutbox(10)
	if len(list) != 1 || list[0].Delivery != "pending" || list[0].Attempts != 1 || list[0].NextAttemptAt == nil {
		t.Fatalf("outbox after failure = %+v", list)
	}
	api.sendErr = nil
	*now = list[0].NextAttemptAt.Add(time.Second)
	if err := adapter.deliverDue(context.Background()); err != nil {
		t.Fatalf("deliverDue() error: %v", err)
	}
	list, _ = st.CommTG().ListOutbox(10)
	if len(list) != 1 || list[0].Delivery != "sent" {
		t.Errorf("outbox after retry = %+v", list)
	}
}

func TestSendRequiresChat(t *testing.T) {
	adapter, _, _, _ := newTestAdapter(t, "42")
	env := envelope.New(envelope.DirectionOut, "telegram", "",
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if err := adapter.Send(env); err == nil {
		t.Fatal("Send() error = nil, want no-chat error")
	}
}

func TestSplitText(t *testing.T) {
	chunks := splitText("one two three four", 10)
	if len(chunks) != 2 || chunks[0] != "one two" || chunks[1] != "three four" {
		t.Errorf("chunks = %v", chunks)
	}
	if chunks := splitText("short", 10); len(chunks) != 1 || chunks[0] != "short" {
		t.Errorf("short chunks = %v", chunks)
	}
}

func TestSplitTextUTF16(t *testing.T) {
	text := strings.Repeat("😀", 3000)
	chunks := splitText(text, 4000)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		if units := len(utf16.Encode([]rune(chunk))); units > 4000 {
			t.Errorf("chunk units = %d, want <= 4000", units)
		}
	}
}

func TestIsAllowed(t *testing.T) {
	adapter, _, _, _ := newTestAdapter(t, "@Alice", "42")
	if !adapter.isAllowed(42, "anything") {
		t.Error("numeric ID was not allowed")
	}
	if !adapter.isAllowed(99, "alice") {
		t.Error("username was not allowed case-insensitively")
	}
	if adapter.isAllowed(99, "bob") {
		t.Error("unknown user was allowed")
	}
	empty, _, _, _ := newTestAdapter(t)
	if empty.isAllowed(42, "alice") {
		t.Error("empty allowlist admitted a user")
	}
}
