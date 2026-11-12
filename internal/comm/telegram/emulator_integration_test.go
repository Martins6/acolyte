package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/comm/telegram/telegramtest"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type emulatedHarness struct {
	adapter *Adapter
	server  *telegramtest.Server
	store   *store.Store
	clock   *testClock
}

func newEmulatedHarness(t *testing.T, allowed ...string) *emulatedHarness {
	t.Helper()
	server := telegramtest.New()
	t.Cleanup(server.Close)

	workspace := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := store.Open(workspace)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	clock := &testClock{at: time.Now().UTC().Truncate(time.Second)}
	adapter, err := New(Options{
		Agent:        registry.Agent{Name: "valet", Workspace: workspace},
		Store:        st,
		Token:        server.Token,
		AllowedUsers: allowed,
		Now:          clock.Now,
		PollInterval: 10 * time.Millisecond,
		MaxAttempts:  3,
		ServerURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return &emulatedHarness{adapter: adapter, server: server, store: st, clock: clock}
}

func (h *emulatedHarness) start(t *testing.T, sink func(context.Context, envelope.Envelope) error) {
	t.Helper()
	if err := h.adapter.Start(context.Background(), sink); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { _ = h.adapter.Stop() })
}

func waitEnvelope(t *testing.T, ch <-chan envelope.Envelope) envelope.Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for inbound envelope")
		return envelope.Envelope{}
	}
}

func waitCondition(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestEmulatedTelegramInboundAndOutbound(t *testing.T) {
	h := newEmulatedHarness(t, "@alice")
	inbound := make(chan envelope.Envelope, 1)
	h.start(t, func(_ context.Context, env envelope.Envelope) error {
		inbound <- env
		return nil
	})

	update := h.server.SendText(10, 42, "alice", "hello over the emulator")
	env := waitEnvelope(t, inbound)
	if env.Channel != Channel || env.Thread != envelope.TelegramThread(10) {
		t.Fatalf("envelope channel/thread = %q/%q", env.Channel, env.Thread)
	}
	if env.Content.Text != "hello over the emulator" {
		t.Fatalf("envelope text = %q", env.Content.Text)
	}
	if env.Meta["telegram_update_id"] != update.ID {
		t.Errorf("update id = %v, want %d", env.Meta["telegram_update_id"], update.ID)
	}
	if env.Meta["telegram_message_id"] != int64(update.Message.ID) {
		t.Errorf("message id = %v, want %d", env.Meta["telegram_message_id"], update.Message.ID)
	}

	if err := h.adapter.Send(envelope.New(envelope.DirectionOut, Channel, env.Thread, env.To, env.From,
		envelope.Content{Type: envelope.ContentText, Text: "reply over the emulator"})); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	messages, err := h.server.WaitForChatMessages(testContext(t), 10, 1)
	if err != nil {
		t.Fatalf("wait messages: %v", err)
	}
	if messages[0].Text != "reply over the emulator" {
		t.Errorf("sent text = %q", messages[0].Text)
	}

	waitCondition(t, "inbound to be marked processed", func() bool {
		item, ok, err := h.store.CommTG().GetInboxByEnvelope(env.ID)
		return err == nil && ok && item.ProcessedAt != nil
	})
	chat, ok, err := h.store.CommTG().GetChat(10)
	if err != nil || !ok || !chat.Allowed || !chat.Private {
		t.Fatalf("chat = %+v, %v, %v", chat, ok, err)
	}
}

func TestEmulatedTelegramSendsPhoto(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	h.start(t, func(context.Context, envelope.Envelope) error { return nil })

	imagePath := filepath.Join(h.adapter.agent.Workspace, "media", "outbound", "images", "chart.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := []byte("emulated-photo-bytes")
	if err := os.WriteFile(imagePath, payload, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	env := envelope.New(envelope.DirectionOut, Channel, envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentMedia, Text: "chart", Media: []string{imagePath}})
	if err := h.adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	messages, err := h.server.WaitForMessages(testContext(t), 1)
	if err != nil {
		t.Fatalf("wait messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Text != "chart" || string(messages[0].Media) != string(payload) {
		t.Fatalf("messages = %+v", messages)
	}
	if h.server.Calls("sendPhoto") != 1 {
		t.Errorf("sendPhoto calls = %d, want 1", h.server.Calls("sendPhoto"))
	}
}

func TestEmulatedTelegramDeniesUnauthorizedUser(t *testing.T) {
	h := newEmulatedHarness(t, "@alice")
	var sinkCalls atomic.Int32
	h.start(t, func(context.Context, envelope.Envelope) error {
		sinkCalls.Add(1)
		return nil
	})

	h.server.SendText(11, 43, "bob", "let me in")
	messages, err := h.server.WaitForChatMessages(testContext(t), 11, 1)
	if err != nil {
		t.Fatalf("wait messages: %v", err)
	}
	if !strings.Contains(messages[0].Text, "not authorized") {
		t.Errorf("refusal = %q", messages[0].Text)
	}
	if sinkCalls.Load() != 0 {
		t.Errorf("sink calls = %d, want 0", sinkCalls.Load())
	}
	if chats, err := h.store.CommTG().ListChats(); err != nil || len(chats) != 0 {
		t.Errorf("chats = %+v, %v", chats, err)
	}
}

func TestEmulatedTelegramDownloadsMedia(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	inbound := make(chan envelope.Envelope, 1)
	h.start(t, func(_ context.Context, env envelope.Envelope) error {
		inbound <- env
		return nil
	})

	data := []byte("fake-jpeg-bytes")
	h.server.SendPhoto(10, 42, "alice", "look at this", data)
	env := waitEnvelope(t, inbound)
	if env.Content.Type != envelope.ContentMedia || len(env.Content.Media) != 1 {
		t.Fatalf("content = %+v", env.Content)
	}
	if !strings.Contains(env.Content.Text, "look at this") {
		t.Errorf("prompt = %q, want caption", env.Content.Text)
	}
	got, err := os.ReadFile(env.Content.Media[0])
	if err != nil {
		t.Fatalf("read downloaded media: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("media bytes = %q, want %q", got, data)
	}
	if h.server.Calls("getFile") != 1 {
		t.Errorf("getFile calls = %d, want 1", h.server.Calls("getFile"))
	}

	waitCondition(t, "media path persisted", func() bool {
		item, ok, err := h.store.CommTG().GetInboxByEnvelope(env.ID)
		return err == nil && ok && item.MediaPath == env.Content.Media[0]
	})
}

func TestEmulatedTelegramReplaysFailedMediaDownload(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	inbound := make(chan envelope.Envelope, 1)
	h.start(t, func(_ context.Context, env envelope.Envelope) error {
		inbound <- env
		return nil
	})

	h.server.FailNext("getFile", 500, "Bad Gateway")
	data := []byte("retry-media")
	h.server.SendPhoto(10, 42, "alice", "retry this", data)

	if _, err := h.server.WaitForChatMessages(testContext(t), 10, 1); err != nil {
		t.Fatalf("wait for processing error reply: %v", err)
	}
	waitCondition(t, "the failed media download", func() bool {
		items, err := h.store.CommTG().ListInbox(10)
		return err == nil && len(items) == 1 && items[0].Attempts == 1 && items[0].MediaPath == ""
	})

	h.adapter.replayPending(context.Background())
	env := waitEnvelope(t, inbound)
	got, err := os.ReadFile(env.Content.Media[0])
	if err != nil {
		t.Fatalf("read replayed media: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("media bytes = %q, want %q", got, data)
	}
	if h.server.Calls("getFile") != 2 {
		t.Errorf("getFile calls = %d, want 2 (failure then replay)", h.server.Calls("getFile"))
	}
}

func TestEmulatedTelegramSplitsLongOutbound(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	h.adapter.maxMessageChars = 10
	h.start(t, func(context.Context, envelope.Envelope) error { return nil })

	env := envelope.New(envelope.DirectionOut, Channel, envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "one two three four"})
	if err := h.adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	messages, err := h.server.WaitForMessages(testContext(t), 2)
	if err != nil {
		t.Fatalf("wait messages: %v", err)
	}
	if messages[0].Text != "one two" || messages[1].Text != "three four" {
		t.Fatalf("chunks = %q, %q", messages[0].Text, messages[1].Text)
	}
	outbox, err := h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].ChunksSent != 2 || outbox[0].Delivery != "sent" {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
}

func TestEmulatedTelegramRetriesTransientFailure(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	h.start(t, func(context.Context, envelope.Envelope) error { return nil })

	h.server.FailNext("sendMessage", 500, "Internal Server Error")
	env := envelope.New(envelope.DirectionOut, Channel, envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "retry me"})
	if err := h.adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	outbox, err := h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Delivery != "pending" || outbox[0].Attempts != 1 {
		t.Fatalf("outbox after failure = %+v, %v", outbox, err)
	}

	h.clock.Advance(6 * time.Second)
	if err := h.adapter.deliverDue(context.Background()); err != nil {
		t.Fatalf("deliverDue() error: %v", err)
	}
	messages, err := h.server.WaitForMessages(testContext(t), 1)
	if err != nil {
		t.Fatalf("wait messages: %v", err)
	}
	if messages[0].Text != "retry me" {
		t.Errorf("sent text = %q", messages[0].Text)
	}
	outbox, err = h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Delivery != "sent" || outbox[0].ChunksSent != 1 {
		t.Fatalf("outbox after retry = %+v, %v", outbox, err)
	}
}

func TestEmulatedTelegramResumesAfterChunkFailure(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	h.adapter.maxMessageChars = 10
	h.start(t, func(context.Context, envelope.Envelope) error { return nil })

	h.server.FailOnCall("sendMessage", 2, 500, "Internal Server Error")
	env := envelope.New(envelope.DirectionOut, Channel, envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "one two three four"})
	if err := h.adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	messages, err := h.server.WaitForMessages(testContext(t), 1)
	if err != nil {
		t.Fatalf("wait for first chunk: %v", err)
	}
	if messages[0].Text != "one two" {
		t.Fatalf("first chunk = %q", messages[0].Text)
	}
	outbox, err := h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].ChunksSent != 1 || outbox[0].Delivery != "pending" {
		t.Fatalf("outbox after chunk failure = %+v, %v", outbox, err)
	}

	h.clock.Advance(6 * time.Second)
	if err := h.adapter.deliverDue(context.Background()); err != nil {
		t.Fatalf("deliverDue() error: %v", err)
	}
	messages, err = h.server.WaitForMessages(testContext(t), 2)
	if err != nil {
		t.Fatalf("wait for retried chunk: %v", err)
	}
	if messages[1].Text != "three four" {
		t.Errorf("retried chunk = %q, want only the missing chunk", messages[1].Text)
	}
	outbox, err = h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].ChunksSent != 2 || outbox[0].Delivery != "sent" {
		t.Fatalf("outbox after resume = %+v, %v", outbox, err)
	}
}

func TestEmulatedTelegramMarksPersistentFailure(t *testing.T) {
	h := newEmulatedHarness(t, "42")
	h.adapter.maxAttempts = 2
	h.start(t, func(context.Context, envelope.Envelope) error { return nil })

	h.server.FailNext("sendMessage", 500, "Internal Server Error")
	h.server.FailNext("sendMessage", 500, "Internal Server Error")
	env := envelope.New(envelope.DirectionOut, Channel, envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Party{Kind: envelope.PartyUser, ID: "42"},
		envelope.Content{Type: envelope.ContentText, Text: "never delivered"})
	if err := h.adapter.Send(env); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	h.clock.Advance(6 * time.Second)
	_ = h.adapter.deliverDue(context.Background())

	outbox, err := h.store.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %+v, %v", outbox, err)
	}
	if outbox[0].Delivery != "failed" {
		t.Errorf("delivery = %q, want failed", outbox[0].Delivery)
	}
	if outbox[0].Text != "" {
		t.Errorf("failed row text = %q, want cleared", outbox[0].Text)
	}
	if !strings.Contains(outbox[0].LastError, "Internal Server Error") {
		t.Errorf("last error = %q", outbox[0].LastError)
	}
}

func TestEmulatedTelegramDeduplicatesUpdates(t *testing.T) {
	h := newEmulatedHarness(t, "@alice")
	var sinkCalls atomic.Int32
	h.start(t, func(context.Context, envelope.Envelope) error {
		sinkCalls.Add(1)
		return nil
	})

	update := h.server.SendUpdate(models.Update{Message: &models.Message{
		Date: int(time.Now().Unix()),
		From: &models.User{ID: 42, Username: "alice"},
		Chat: models.Chat{ID: 10, Type: models.ChatTypePrivate},
		Text: "only once",
	}})
	// Telegram can redeliver an update that was not acknowledged; feed the
	// same update ID twice.
	h.server.SendUpdate(update)

	waitCondition(t, "the duplicate update to be recorded", func() bool {
		items, err := h.store.CommTG().ListInbox(10)
		return err == nil && len(items) == 1 && items[0].ProcessedAt != nil
	})
	time.Sleep(100 * time.Millisecond)
	if sinkCalls.Load() != 1 {
		t.Errorf("sink calls = %d, want 1", sinkCalls.Load())
	}
}

func TestEmulatedTelegramReplaysFailedInbound(t *testing.T) {
	h := newEmulatedHarness(t, "@alice")
	var calls atomic.Int32
	h.start(t, func(context.Context, envelope.Envelope) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary turn failure")
		}
		return nil
	})

	h.server.SendText(10, 42, "alice", "replay me")
	waitCondition(t, "the first failed admission", func() bool {
		items, err := h.store.CommTG().ListInbox(10)
		return err == nil && len(items) == 1 && items[0].Attempts == 1
	})

	h.adapter.replayPending(context.Background())
	waitCondition(t, "the replayed admission", func() bool {
		items, err := h.store.CommTG().ListInbox(10)
		return err == nil && len(items) == 1 && items[0].ProcessedAt != nil
	})
	if calls.Load() != 2 {
		t.Errorf("sink calls = %d, want 2", calls.Load())
	}
}
