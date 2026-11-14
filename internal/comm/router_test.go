package comm

import (
	"context"
	"encoding/json"
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
	configs   []opencode.TurnConfig
	events    []opencode.Event
}

func (f *fakeTurn) RunUserTurn(ctx context.Context, sessionID, text string, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.prompts = append(f.prompts, text)
	f.configs = append(f.configs, cfg)
	events := append([]opencode.Event(nil), f.events...)
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
	for _, event := range events {
		if cfg.OnEvent != nil {
			if err := cfg.OnEvent(event); err != nil {
				return nil, err
			}
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
		EventRecorder:  ActivityEventRecorder,
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

func TestRouterTurnRecordsToolActivity(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.turn.events = []opencode.Event{
		{ID: "evt_called", Type: "session.tool.called", Data: json.RawMessage(
			`{"sessionID":"ses_evt","assistantMessageID":"msg_evt","id":"tool_evt","input":{"api_key":"secret"}}`)},
		{ID: "evt_text", Type: "session.message.content.updated", Data: json.RawMessage(
			`{"sessionID":"ses_evt","content":[{"type":"text","text":"secret"}]}`)},
		{ID: "evt_failed", Type: "session.tool.failed", Data: json.RawMessage(
			`{"sessionID":"ses_evt","assistantMessageID":"msg_evt","id":"tool_evt","error":{"message":"secret"}}`)},
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(60),
		envelope.Party{Kind: envelope.PartyUser, ID: "60"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if len(h.turn.configs) != 1 || h.turn.configs[0].OnEvent == nil {
		t.Fatalf("turn configs = %+v, want OnEvent wired", h.turn.configs)
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("ListLogs() error: %v", err)
	}
	var activity []string
	for _, entry := range logs {
		if entry.Source == "opencode:valet" {
			activity = append(activity, entry.Level+"/"+entry.Message+"/"+entry.Meta)
			if strings.Contains(entry.Meta, "secret") {
				t.Errorf("meta leaked content: %q", entry.Meta)
			}
		}
	}
	if len(activity) != 2 {
		t.Fatalf("activity logs = %v, want called and failed rows", activity)
	}
	if !strings.HasPrefix(activity[0], "info/tool called/") || !strings.Contains(activity[0], `"tool_call_id":"tool_evt"`) {
		t.Errorf("called log = %q", activity[0])
	}
	if !strings.HasPrefix(activity[1], "warn/tool failed/") {
		t.Errorf("failed log = %q", activity[1])
	}
}

func TestRouterWithoutEventRecorderLeavesOnEventNil(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.events = nil
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(61),
		envelope.Party{Kind: envelope.PartyUser, ID: "61"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if len(h.turn.configs) != 1 || h.turn.configs[0].OnEvent != nil {
		t.Fatalf("turn configs = %+v, want nil OnEvent without a recorder", h.turn.configs)
	}
}

func TestRouterPromptIncludesMedia(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		media     []string
		want      string
		pathLines int
	}{
		{
			name:      "text and media",
			text:      "look at this",
			media:     []string{"/tmp/photo.jpg"},
			want:      "look at this\nFile located at: /tmp/photo.jpg",
			pathLines: 1,
		},
		{
			name:      "media only",
			media:     []string{"/tmp/photo.jpg"},
			want:      "File located at: /tmp/photo.jpg",
			pathLines: 1,
		},
		{
			name:      "text already names the path",
			text:      "File located at: /tmp/photo.jpg\nFile type: Photo\n\nUser message: look",
			media:     []string{"/tmp/photo.jpg"},
			want:      "File located at: /tmp/photo.jpg\nFile type: Photo\n\nUser message: look",
			pathLines: 1,
		},
		{
			name:      "longer path with the same prefix is not the media path",
			text:      "back up /tmp/dir/photo.jpg.backup first",
			media:     []string{"/tmp/dir/photo.jpg"},
			want:      "back up /tmp/dir/photo.jpg.backup first\nFile located at: /tmp/dir/photo.jpg",
			pathLines: 1,
		},
		{
			name:      "multiple media files",
			text:      "see these",
			media:     []string{"/tmp/a.jpg", "/tmp/b.pdf"},
			want:      "see these\nFile located at: /tmp/a.jpg\nFile located at: /tmp/b.pdf",
			pathLines: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newRouterHarness(t, 1)
			env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(11),
				envelope.Party{Kind: envelope.PartyUser, ID: "11"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentMedia, Text: tt.text, Media: tt.media})
			if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
				t.Fatalf("HandleInbound() error: %v", err)
			}
			if len(h.turn.prompts) != 1 {
				t.Fatalf("prompts = %d, want 1", len(h.turn.prompts))
			}
			got := h.turn.prompts[0]
			if got != tt.want {
				t.Errorf("prompt = %q, want %q", got, tt.want)
			}
			if lines := strings.Count(got, "File located at:"); lines != tt.pathLines {
				t.Errorf("File located at lines = %d, want %d in %q", lines, tt.pathLines, got)
			}
		})
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
