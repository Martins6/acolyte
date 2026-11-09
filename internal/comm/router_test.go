package comm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/storepool"
)

type fakeTurn struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	prompts   []string
	result    *opencode.TurnResult
	err       error
	delay     time.Duration
}

func (f *fakeTurn) RunUserTurn(ctx context.Context, sessionID, text string, _ opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.prompts = append(f.prompts, text)
	result := f.result
	err := f.err
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if result == nil {
		result = &opencode.TurnResult{SessionID: "ses_fake", InputID: "msg_in", ResponseText: "reply", MessageIDs: []string{"msg_out"}}
	}
	copy := *result
	return &copy, err
}

type outboundRecorder struct {
	mu     sync.Mutex
	events []envelope.Envelope
}

func (r *outboundRecorder) send(_ context.Context, _ registry.Agent, env envelope.Envelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, env)
	return nil
}

type routerHarness struct {
	router   *Router
	pool     *storepool.Pool
	store    string
	agent    registry.Agent
	turn     *fakeTurn
	outbound *outboundRecorder
}

func newRouterHarness(t *testing.T, maxConcurrency int) *routerHarness {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pool := storepool.New()
	t.Cleanup(func() { _ = pool.CloseAll() })
	turn := &fakeTurn{}
	outbound := &outboundRecorder{}
	router, err := NewRouter(RouterOptions{
		Stores:         pool,
		Turn:           turn,
		Outbound:       outbound.send,
		MaxConcurrency: maxConcurrency,
		Now:            func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("NewRouter() error: %v", err)
	}
	return &routerHarness{
		router:   router,
		pool:     pool,
		store:    ws,
		agent:    registry.Agent{Name: "valet", Workspace: ws},
		turn:     turn,
		outbound: outbound,
	}
}

func (h *routerHarness) seedInbound(t *testing.T, env envelope.Envelope, updateID int64) {
	t.Helper()
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	inserted, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: updateID, ChatID: 10, EnvelopeID: env.ID,
	})
	if err != nil || !inserted {
		t.Fatalf("RecordInbound() = %v, %v", inserted, err)
	}
}

func TestRouterInboundPersistsSessionLinksAndRoutes(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyUser, ID: "10", Name: "alice"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	env.Meta = map[string]any{"telegram_message_id": float64(77)}
	h.seedInbound(t, env, 100)

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if len(h.outbound.events) != 1 {
		t.Fatalf("outbound events = %d, want 1", len(h.outbound.events))
	}
	out := h.outbound.events[0]
	if out.Direction != envelope.DirectionOut || out.Content.Text != "reply" || out.Thread != env.Thread {
		t.Errorf("outbound = %+v", out)
	}
	if out.Meta["source_message_id"] != "msg_out" || out.Meta["reply_to_message_id"] != float64(77) {
		t.Errorf("outbound meta = %+v", out.Meta)
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	session, ok, err := st.Orch().GetSessionByThread(env.Thread)
	if err != nil || !ok || session.OpenCodeSessionID != "ses_fake" {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	inbox, ok, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil || !ok || inbox.OpenCodeMessageID != "msg_in" || inbox.OpenCodeSessionID != "ses_fake" {
		t.Errorf("inbox = %+v, %v, %v", inbox, ok, err)
	}
}

func TestRouterMediaPrompt(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(11),
		envelope.Party{Kind: envelope.PartyUser, ID: "11"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentMedia, Media: []string{"/tmp/photo.jpg"}})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if len(h.turn.prompts) != 1 || !strings.Contains(h.turn.prompts[0], "/tmp/photo.jpg") {
		t.Errorf("prompts = %v", h.turn.prompts)
	}
}

func TestRouterTurnErrorPersistsAndRoutesError(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.turn.result = &opencode.TurnResult{SessionID: "ses_err", InputID: "msg_in"}
	h.turn.err = errors.New("boom")
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(12),
		envelope.Party{Kind: envelope.PartyUser, ID: "12"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err == nil {
		t.Fatal("HandleInbound() error = nil, want error")
	}
	if len(h.outbound.events) != 1 || !strings.Contains(h.outbound.events[0].Content.Text, "Error: boom") {
		t.Errorf("outbound = %+v", h.outbound.events)
	}
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	session, ok, err := st.Orch().GetSessionByThread(env.Thread)
	if err != nil || !ok || session.OpenCodeSessionID != "ses_err" {
		t.Errorf("session = %+v, %v, %v", session, ok, err)
	}
}

func TestRouterSerializesSameThread(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.turn.delay = 30 * time.Millisecond
	thread := envelope.TelegramThread(13)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := envelope.New(envelope.DirectionIn, "telegram", thread,
				envelope.Party{Kind: envelope.PartyUser, ID: "13"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentText, Text: "hello"})
			if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
				t.Errorf("HandleInbound() error: %v", err)
			}
		}()
	}
	wg.Wait()
	if h.turn.maxActive != 1 {
		t.Errorf("max active turns = %d, want 1 for the same thread", h.turn.maxActive)
	}
}

func TestRouterBoundsDifferentThreads(t *testing.T) {
	h := newRouterHarness(t, 1)
	h.turn.delay = 30 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		chatID := int64(20 + i)
		go func() {
			defer wg.Done()
			env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(chatID),
				envelope.Party{Kind: envelope.PartyUser, ID: "20"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentText, Text: "hello"})
			if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
				t.Errorf("HandleInbound() error: %v", err)
			}
		}()
	}
	wg.Wait()
	if h.turn.maxActive != 1 {
		t.Errorf("max active turns = %d, want 1", h.turn.maxActive)
	}
}

func TestRouterOutboundFailureKeepsPendingReply(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.outbound = func(context.Context, registry.Agent, envelope.Envelope) error {
		return errors.New("database busy")
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(50),
		envelope.Party{Kind: envelope.PartyUser, ID: "50"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 300)

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err == nil {
		t.Fatal("HandleInbound() error = nil, want outbound error")
	}
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	item, ok, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil || !ok {
		t.Fatalf("inbox = %+v, %v, %v", item, ok, err)
	}
	if item.PendingReply == "" {
		t.Error("pending reply was not recorded for replay")
	}
	if item.OpenCodeMessageID != "" {
		t.Error("turn input was linked before the reply was durably queued")
	}
}

func TestRouterWithoutOutboundStillPersists(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.outbound = nil
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(30),
		envelope.Party{Kind: envelope.PartyUser, ID: "30"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	if _, ok, err := st.Orch().GetSessionByThread(env.Thread); err != nil || !ok {
		t.Errorf("session missing: ok:%v err:%v", ok, err)
	}
}

func TestRouterReleasesThreadLocks(t *testing.T) {
	h := newRouterHarness(t, 4)
	thread := envelope.TelegramThread(40)
	for index := 0; index < 20; index++ {
		env := envelope.New(envelope.DirectionIn, "telegram", thread,
			envelope.Party{Kind: envelope.PartyUser, ID: "40"},
			envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
			envelope.Content{Type: envelope.ContentText, Text: "hello"})
		if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
			t.Fatalf("HandleInbound() error: %v", err)
		}
	}
	h.router.lockMu.Lock()
	defer h.router.lockMu.Unlock()
	if len(h.router.locks) != 0 {
		t.Errorf("thread locks = %d, want 0 after all callers finish", len(h.router.locks))
	}
}
