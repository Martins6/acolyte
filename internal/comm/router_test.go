package comm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	// Mirror the real client: the marker is handed over before the turn is
	// collected.
	if cfg.OnAdmitted != nil {
		admitted := opencode.Admitted{SessionID: result.SessionID, InputID: result.InputID}
		if err := cfg.OnAdmitted(admitted); err != nil {
			return nil, err
		}
	}
	copy := *result
	return &copy, err
}

// fakeProber adds the optional recovery half to the fake turn runner.
type fakeProber struct {
	fakeTurn
	session      *opencode.SessionInfo
	sessionErr   error
	collected    *opencode.TurnResult
	delivered    bool
	collectErr   error
	pending      bool
	pendingByID  map[string]bool
	inboxErr     error
	nextInput    string
	nextFound    bool
	nextErr      error
	interrupted  []string
	interruptErr error
}

func (f *fakeProber) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	if f.session == nil {
		return &opencode.SessionInfo{ID: "ses_fake"}, nil
	}
	return f.session, nil
}

func (f *fakeProber) NextInputID(context.Context, string, string) (string, bool, error) {
	if f.nextErr != nil {
		return "", false, f.nextErr
	}
	return f.nextInput, f.nextFound, nil
}

func (f *fakeProber) CollectDelivered(context.Context, string, string) (*opencode.TurnResult, bool, error) {
	if f.collectErr != nil {
		return nil, false, f.collectErr
	}
	return f.collected, f.delivered, nil
}

func (f *fakeProber) InboxPending(_ context.Context, _, itemID string) (bool, error) {
	if f.inboxErr != nil {
		return false, f.inboxErr
	}
	if f.pendingByID != nil {
		if pending, ok := f.pendingByID[itemID]; ok {
			return pending, nil
		}
	}
	return f.pending, nil
}

func (f *fakeProber) Interrupt(_ context.Context, sessionID string) error {
	if f.interruptErr != nil {
		return f.interruptErr
	}
	f.interrupted = append(f.interrupted, sessionID)
	return nil
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

func (h *routerHarness) seedAdmitted(t *testing.T, env envelope.Envelope, updateID int64, sessionID, markerID string) {
	t.Helper()
	h.seedInbound(t, env, updateID)
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	if err := st.CommTG().MarkInboundAdmitted(env.ID, sessionID, markerID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkInboundAdmitted() error: %v", err)
	}
}

func (h *routerHarness) seedSession(t *testing.T, thread, sessionID string) {
	t.Helper()
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	if _, err := st.Orch().UpsertSession(thread, sessionID, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
}

func (h *routerHarness) hasSession(t *testing.T, thread string) bool {
	t.Helper()
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	_, ok, err := st.Orch().GetSessionByThread(thread)
	if err != nil {
		t.Fatalf("GetSessionByThread() error: %v", err)
	}
	return ok
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
	if len(h.outbound.events) != 2 {
		t.Fatalf("outbound events = %d, want ack and reply", len(h.outbound.events))
	}
	ack := h.outbound.events[0]
	if ack.ID != ackEnvelopeID(env.ID) || ack.Meta["kind"] != "ack" {
		t.Errorf("ack = %+v, want identity %q with kind ack", ack, ackEnvelopeID(env.ID))
	}
	if !strings.HasPrefix(ack.Content.Text, SystemNoticeMarker) || !strings.Contains(ack.Content.Text, "working") {
		t.Errorf("ack text = %q, want an honest working notice", ack.Content.Text)
	}
	out := h.outbound.events[1]
	if out.Direction != envelope.DirectionOut || out.Content.Text != "reply" || out.Thread != env.Thread {
		t.Errorf("outbound = %+v", out)
	}
	if out.ID != replyEnvelopeID(env.ID) {
		t.Errorf("reply id = %q, want %q", out.ID, replyEnvelopeID(env.ID))
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

// admittedSignalTurn blocks after admitting so a test can inspect outbound
// traffic while collection is still running.
type admittedSignalTurn struct {
	admitted chan struct{}
	release  chan struct{}
}

func (f *admittedSignalTurn) RunUserTurn(_ context.Context, _, _ string, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	if cfg.OnAdmitted == nil {
		return nil, errors.New("OnAdmitted was not configured")
	}
	if err := cfg.OnAdmitted(opencode.Admitted{SessionID: "ses_live", InputID: "msg_live"}); err != nil {
		return nil, err
	}
	close(f.admitted)
	<-f.release
	return &opencode.TurnResult{SessionID: "ses_live", InputID: "msg_live", ResponseText: "done"}, nil
}

// TestRouterAcknowledgesAtAdmission covers the no-silence invariant: a durable
// acknowledgement must exist while the turn is still running, not only once
// collection finishes.
func TestRouterAcknowledgesAtAdmission(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(92),
		envelope.Party{Kind: envelope.PartyUser, ID: "92"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	env.Meta = map[string]any{"telegram_message_id": float64(93)}
	h.seedInbound(t, env, 700)

	turn := &admittedSignalTurn{admitted: make(chan struct{}), release: make(chan struct{})}
	h.router.turn = turn
	done := make(chan error, 1)
	go func() { done <- h.router.HandleInbound(context.Background(), h.agent, env) }()

	select {
	case <-turn.admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("turn was never admitted")
	}
	if len(h.outbound.events) != 1 {
		t.Fatalf("outbound events mid-turn = %d, want the ack only", len(h.outbound.events))
	}
	ack := h.outbound.events[0]
	if ack.ID != ackEnvelopeID(env.ID) || ack.Meta["kind"] != "ack" {
		t.Errorf("ack = %+v, want identity %q with kind ack", ack, ackEnvelopeID(env.ID))
	}
	if !strings.HasPrefix(ack.Content.Text, SystemNoticeMarker) {
		t.Errorf("ack text = %q, want the %q marker", ack.Content.Text, SystemNoticeMarker)
	}
	if strings.Contains(strings.ToLower(ack.Content.Text), "complete") {
		t.Errorf("ack text = %q, must not imply completion", ack.Content.Text)
	}
	if ack.Meta["reply_to_message_id"] != float64(93) {
		t.Errorf("ack meta = %+v, want reply_to_message_id preserved", ack.Meta)
	}

	close(turn.release)
	if err := <-done; err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if len(h.outbound.events) != 2 {
		t.Fatalf("outbound events = %d, want ack and reply", len(h.outbound.events))
	}
	if h.outbound.events[1].ID != replyEnvelopeID(env.ID) || h.outbound.events[1].Content.Text != "done" {
		t.Errorf("reply = %+v, want identity %q with the collected text", h.outbound.events[1], replyEnvelopeID(env.ID))
	}
}

// TestRouterAckFailureDoesNotFailTurn keeps the acknowledgement a courtesy: if
// its enqueue fails after the marker is durable, the running turn's reply must
// still be collected and routed.
func TestRouterAckFailureDoesNotFailTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(94),
		envelope.Party{Kind: envelope.PartyUser, ID: "94"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 701)

	recorder := &outboundRecorder{}
	h.router.outbound = func(ctx context.Context, agent registry.Agent, out envelope.Envelope) error {
		if out.ID == ackEnvelopeID(env.ID) {
			return errors.New("queue unavailable")
		}
		return recorder.send(ctx, agent, out)
	}
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error = %v, want the ack failure to be tolerated", err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Content.Text != "reply" {
		t.Errorf("outbound = %+v, want the collected reply", recorder.events)
	}
}

// TestRouterAckIsIdempotent exercises the same enqueue path as production: the
// stable ack identity plus the outbox unique index must collapse a repeated
// admission into one row.
func TestRouterAckIsIdempotent(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(99),
		envelope.Party{Kind: envelope.PartyUser, ID: "99"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	if ackEnvelopeID(env.ID) == replyEnvelopeID(env.ID) {
		t.Fatal("ack and reply identities must not collide")
	}

	h.router.outbound = func(_ context.Context, _ registry.Agent, out envelope.Envelope) error {
		st, release, err := h.pool.Acquire(h.store)
		if err != nil {
			return err
		}
		defer release()
		_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
			EnvelopeID: out.ID,
			ChatID:     99,
			Text:       out.Content.Text,
		})
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := h.router.sendAck(context.Background(), h.agent, env); err != nil {
			t.Fatalf("sendAck() attempt %d error: %v", attempt, err)
		}
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(99)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	if len(outbox) != 1 || outbox[0].EnvelopeID != ackEnvelopeID(env.ID) {
		t.Fatalf("outbox = %+v, want one ack row", outbox)
	}
}

// failingTurn returns a pre-admission error and never calls OnAdmitted.
type failingTurn struct{}

func (failingTurn) RunUserTurn(context.Context, string, string, opencode.TurnConfig) (*opencode.TurnResult, error) {
	return nil, errors.New("prompt rejected")
}

// TestRouterPreAdmissionErrorReplies covers a failure before the turn exists:
// there is nothing to recover, so the user gets the error reply immediately.
func TestRouterPreAdmissionErrorReplies(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = failingTurn{}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(76),
		envelope.Party{Kind: envelope.PartyUser, ID: "76"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 703)

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err == nil {
		t.Fatal("HandleInbound() error = nil, want the prompt error")
	}
	if len(h.outbound.events) != 1 || h.outbound.events[0].ID != replyEnvelopeID(env.ID) {
		t.Fatalf("outbound events = %+v, want one error reply on the reply identity", h.outbound.events)
	}
	if !strings.Contains(h.outbound.events[0].Content.Text, "Error:") {
		t.Errorf("reply = %q, want an error reply", h.outbound.events[0].Content.Text)
	}
}

// TestRouterPostAdmissionErrorLeavesTurnForRecovery ensures a collection error
// after admission does not consume the reply identity: the marker must stay
// eligible for reconciliation so the real reply can still be collected.
func TestRouterPostAdmissionErrorLeavesTurnForRecovery(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.turn.err = errors.New("stream died")
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(77),
		envelope.Party{Kind: envelope.PartyUser, ID: "77"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 704)

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err == nil {
		t.Fatal("HandleInbound() error = nil, want the collection error")
	}
	if len(h.outbound.events) != 1 || h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Fatalf("outbound events = %+v, want only the ack; the reply slot must stay free", h.outbound.events)
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	item, _, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		t.Fatalf("GetInboxByEnvelope() error: %v", err)
	}
	if item.OpenCodeMessageID != "msg_in" || item.OpenCodeSessionID != "ses_fake" {
		t.Errorf("marker = %q/%q, want the admitted pair", item.OpenCodeSessionID, item.OpenCodeMessageID)
	}
	if item.ProcessedAt != nil {
		t.Error("row was settled despite an admitted turn awaiting collection")
	}
	if item.PendingReply != "" {
		t.Errorf("pending reply = %q, want none (recovery owns the reply)", item.PendingReply)
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
			name:      "sibling path with a plus sign is not the media path",
			text:      "back up /tmp/photo.jpg+backup first",
			media:     []string{"/tmp/photo.jpg"},
			want:      "back up /tmp/photo.jpg+backup first\nFile located at: /tmp/photo.jpg",
			pathLines: 1,
		},
		{
			name:      "unicode sibling path is not the media path",
			text:      "back up /tmp/photo.jpgфото first",
			media:     []string{"/tmp/photo.jpg"},
			want:      "back up /tmp/photo.jpgфото first\nFile located at: /tmp/photo.jpg",
			pathLines: 1,
		},
		{
			name:      "prose path mention gets a media marker",
			text:      "please review /tmp/photo.jpg before sending",
			media:     []string{"/tmp/photo.jpg"},
			want:      "please review /tmp/photo.jpg before sending\nFile located at: /tmp/photo.jpg",
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
	if len(h.outbound.events) != 2 || !strings.Contains(h.outbound.events[1].Content.Text, "Error: boom") {
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

// markerProbeTurn records what the inbound row looks like mid-turn.
type markerProbeTurn struct {
	pool   *storepool.Pool
	ws     string
	envID  string
	marker string
	seen   string
	found  bool
}

func (f *markerProbeTurn) RunUserTurn(_ context.Context, _ string, _ string, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	if cfg.OnAdmitted == nil {
		return nil, errors.New("OnAdmitted was not configured")
	}
	if err := cfg.OnAdmitted(opencode.Admitted{SessionID: "ses_new", InputID: f.marker}); err != nil {
		return nil, err
	}
	st, release, err := f.pool.Acquire(f.ws)
	if err != nil {
		return nil, err
	}
	defer release()
	item, ok, err := st.CommTG().GetInboxByEnvelope(f.envID)
	if err != nil {
		return nil, err
	}
	f.found = ok
	f.seen = item.OpenCodeMessageID
	return &opencode.TurnResult{SessionID: "ses_new", InputID: f.marker, ResponseText: "ok"}, nil
}

// TestRouterPersistsMarkerBeforeTurnCompletes is the invariant this whole
// recovery path depends on: the marker must be in the database while the turn
// is still running, not only once it finishes.
func TestRouterPersistsMarkerBeforeTurnCompletes(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(90),
		envelope.Party{Kind: envelope.PartyUser, ID: "90"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 500)

	probe := &markerProbeTurn{pool: h.pool, ws: h.store, envID: env.ID, marker: "msg_live"}
	h.router.turn = probe

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if !probe.found {
		t.Fatal("inbound row disappeared during the turn")
	}
	if probe.seen != probe.marker {
		t.Fatalf("marker visible mid-turn = %q, want %q", probe.seen, probe.marker)
	}
}

func TestRouterTurnSurvivesAdmittedPersistenceFailure(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(91),
		envelope.Party{Kind: envelope.PartyUser, ID: "91"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})

	// No inbound row: MarkInboundAdmitted reports ErrInboundNotFound, which the
	// router tolerates so the turn is never failed for a missing bookkeeping row.
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v, want tolerated ErrInboundNotFound", err)
	}
	if len(h.outbound.events) != 2 {
		t.Fatalf("outbound events = %d, want ack and reply", len(h.outbound.events))
	}
	if h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Errorf("ack id = %q, want %q", h.outbound.events[0].ID, ackEnvelopeID(env.ID))
	}
	if h.outbound.events[1].Content.Text != "reply" {
		t.Errorf("reply text = %q, want %q", h.outbound.events[1].Content.Text, "reply")
	}
}

// seedStranded seeds an inbound that was admitted at a given instant, so the
// grace window can be exercised without touching package state.
func (h *routerHarness) seedStranded(t *testing.T, env envelope.Envelope, updateID int64, sessionID, marker string, attempts int, admittedAt time.Time) {
	t.Helper()
	h.seedInbound(t, env, updateID)
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE comm_tg_inbox SET pending_envelope = ? WHERE envelope_id = ?`, string(raw), env.ID); err != nil {
		t.Fatalf("store pending envelope: %v", err)
	}
	for i := 0; i < attempts; i++ {
		if err := st.CommTG().MarkInboundAdmitted(env.ID, sessionID, marker, admittedAt); err != nil {
			t.Fatalf("MarkInboundAdmitted() error: %v", err)
		}
		if err := st.CommTG().RecordResumeAttempt(env.ID, "prior attempt"); err != nil {
			t.Fatalf("RecordResumeAttempt() error: %v", err)
		}
	}
	if err := st.CommTG().MarkInboundAdmitted(env.ID, sessionID, marker, admittedAt); err != nil {
		t.Fatalf("MarkInboundAdmitted() error: %v", err)
	}
}

func TestRecoverWaitsForGraceWindow(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		pending: true,
		session: &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(95),
		envelope.Party{Kind: envelope.PartyUser, ID: "95"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	// Admitted a moment ago: still inside the grace window.
	h.seedStranded(t, env, 600, "ses_1", "msg_in", 0, time.Now().UTC().Add(-time.Second))
	h.seedSession(t, env.Thread, "ses_1")

	outcome, err := h.router.Recover(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if outcome != ReconcileStillPending {
		t.Fatalf("outcome = %v, want still-pending inside the grace window", outcome)
	}
	if len(prober.interrupted) != 0 {
		t.Errorf("interrupted = %v, want none inside the grace window", prober.interrupted)
	}
}

func TestRecoverAbandonsStrandedTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		pending: true,
		session: &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(96),
		envelope.Party{Kind: envelope.PartyUser, ID: "96"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 601, "ses_1", "msg_in", 0, time.Now().UTC().Add(-time.Hour))
	h.seedSession(t, env.Thread, "ses_1")

	outcome, err := h.router.Recover(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if outcome != ReconcileReAdmitted {
		t.Fatalf("outcome = %v, want re-admitted", outcome)
	}
	if len(prober.interrupted) != 1 || prober.interrupted[0] != "ses_1" {
		t.Errorf("interrupted = %v, want the stranded session", prober.interrupted)
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	if _, ok, err := st.Orch().GetSessionByThread(env.Thread); err != nil || ok {
		t.Errorf("session mapping = %v, %v; want forgotten so a fresh session is created", ok, err)
	}
	item, _, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		t.Fatalf("GetInboxByEnvelope() error: %v", err)
	}
	if item.OpenCodeMessageID != "" {
		t.Errorf("marker = %q, want cleared for a fresh admission", item.OpenCodeMessageID)
	}
	if item.ResumeAttempts != 1 {
		t.Errorf("ResumeAttempts = %d, want 1", item.ResumeAttempts)
	}
	if item.PendingEnvelope == "" {
		t.Error("pending envelope was dropped; the row can no longer be re-admitted")
	}
}

func TestRecoverSettlesAfterAttemptBudget(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		pending: true,
		session: &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(97),
		envelope.Party{Kind: envelope.PartyUser, ID: "97"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 602, "ses_1", "msg_in", maxResumeAttempts, time.Now().UTC().Add(-time.Hour))
	h.seedSession(t, env.Thread, "ses_1")

	outcome, err := h.router.Recover(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	if len(prober.interrupted) != 0 {
		t.Errorf("interrupted = %v, want no further attempts once the budget is spent", prober.interrupted)
	}

	if len(h.outbound.events) != 2 {
		t.Fatalf("outbound events = %d, want healed ack and notice", len(h.outbound.events))
	}
	if h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Errorf("first outbound = %q, want healed ack %q", h.outbound.events[0].ID, ackEnvelopeID(env.ID))
	}
	notice := h.outbound.events[1]
	if !strings.HasPrefix(notice.Content.Text, SystemNoticeMarker) {
		t.Errorf("notice = %q, want the %q marker", notice.Content.Text, SystemNoticeMarker)
	}
	if notice.ID == replyEnvelopeID(env.ID) || notice.ID == ackEnvelopeID(env.ID) {
		t.Errorf("notice id = %q, must not collide with the reply or ack identities", notice.ID)
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	item, _, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		t.Fatalf("GetInboxByEnvelope() error: %v", err)
	}
	if item.ProcessedAt == nil {
		t.Error("settled row was not marked processed; it will loop forever")
	}
}

func TestRecoverSettlesWhenPayloadIsGone(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{
		pending: true,
		session: &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(98),
		envelope.Party{Kind: envelope.PartyUser, ID: "98"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 603, "ses_1", "msg_in", 0, time.Now().UTC().Add(-time.Hour))

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	// Without the stored payload the message can no longer be re-admitted.
	if _, err := st.DB().Exec(`UPDATE comm_tg_inbox SET pending_envelope = '' WHERE envelope_id = ?`, env.ID); err != nil {
		t.Fatalf("clear payload: %v", err)
	}
	release()

	outcome, err := h.router.Recover(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	if len(h.outbound.events) != 2 || !strings.HasPrefix(h.outbound.events[1].Content.Text, SystemNoticeMarker) {
		t.Errorf("outbound = %+v, want healed ack and one explicit notice", h.outbound.events)
	}
	if h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Errorf("first outbound = %q, want healed ack %q", h.outbound.events[0].ID, ackEnvelopeID(env.ID))
	}
}

func TestSystemNoticeWording(t *testing.T) {
	t.Parallel()

	if got := SystemNotice("  retrying shortly "); got != SystemNoticeMarker+" retrying shortly" {
		t.Errorf("SystemNotice() = %q", got)
	}
}

func TestReconcileRecoversDeliveredTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		delivered: true,
		collected: &opencode.TurnResult{SessionID: "ses_1", InputID: "msg_in", ResponseText: "the answer"},
		session:   &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	h.router.turn = prober

	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(80),
		envelope.Party{Kind: envelope.PartyUser, ID: "80"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 400, "ses_1", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileRecovered {
		t.Fatalf("outcome = %v, want recovered", outcome)
	}
	if len(h.outbound.events) != 2 {
		t.Fatalf("outbound events = %d, want healed ack and reply", len(h.outbound.events))
	}
	if h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Errorf("first outbound = %q, want healed ack %q", h.outbound.events[0].ID, ackEnvelopeID(env.ID))
	}
	got := h.outbound.events[1]
	if got.ID != replyEnvelopeID(env.ID) {
		t.Errorf("outbound id = %q, want %q", got.ID, replyEnvelopeID(env.ID))
	}
	if got.Content.Text != "the answer" {
		t.Errorf("outbound text = %q, want %q", got.Content.Text, "the answer")
	}
}

// TestReconcileSettlesEmptyReply covers a turn that finished without text:
// sendReply would do nothing, so replay must not mark it processed silently.
func TestReconcileSettlesEmptyReply(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{
		delivered: true,
		collected: &opencode.TurnResult{SessionID: "ses_1", InputID: "msg_in", ResponseText: "   "},
		session:   &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(86),
		envelope.Party{Kind: envelope.PartyUser, ID: "86"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 406, "ses_1", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	assertSettledWithNotice(t, h, env)
}

// TestReconcileSettlesTruncatedTurn covers ErrTurnTruncated: retrying can
// never produce a bounded reply, so the row must be settled with a notice.
func TestReconcileSettlesTruncatedTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{
		collectErr: opencode.ErrTurnTruncated,
		session:    &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(87),
		envelope.Party{Kind: envelope.PartyUser, ID: "87"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 407, "ses_1", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want the truncated turn settled", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	assertSettledWithNotice(t, h, env)
}

// TestReconcileLeavesTransientProbeErrorPending ensures only definitive
// outcomes settle; transport, timeout, rate-limit, and 5xx errors stay pending.
func TestReconcileLeavesTransientProbeErrorPending(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "transport error", err: errors.New("service unavailable")},
		{name: "service unavailable", err: &opencode.APIError{Status: 503}},
		{name: "rate limited", err: &opencode.APIError{Status: 429}},
		{name: "request timeout", err: &opencode.APIError{Status: 408}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRouterHarness(t, 4)
			h.router.turn = &fakeProber{
				collectErr: tc.err,
				session:    &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
			}
			env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(88),
				envelope.Party{Kind: envelope.PartyUser, ID: "88"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentText, Text: "hello"})
			h.seedAdmitted(t, env, 408, "ses_1", "msg_in")

			outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
			if err == nil {
				t.Fatal("Reconcile() error = nil, want the transient error surfaced")
			}
			if outcome != ReconcileStillPending {
				t.Fatalf("outcome = %v, want still-pending", outcome)
			}
			if len(h.outbound.events) != 1 || h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
				t.Errorf("outbound events = %+v, want only the healed ack", h.outbound.events)
			}
		})
	}
}

// TestReconcileSettlesPermanentProbeError covers probes that cannot heal by
// retrying: a deleted session or a rejected request must settle with a notice.
func TestReconcileSettlesPermanentProbeError(t *testing.T) {
	cases := []struct {
		name       string
		sessionErr error
		collectErr error
		inboxErr   error
	}{
		{name: "missing session", sessionErr: &opencode.APIError{Status: 404}},
		{name: "rejected collect", collectErr: &opencode.APIError{Status: 400}},
		{name: "rejected inbox probe", inboxErr: &opencode.APIError{Status: 404}},
		{name: "method not allowed", collectErr: &opencode.APIError{Status: 405}},
		{name: "conflict", collectErr: &opencode.APIError{Status: 409}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRouterHarness(t, 4)
			h.router.turn = &fakeProber{
				sessionErr: tc.sessionErr,
				collectErr: tc.collectErr,
				inboxErr:   tc.inboxErr,
				session:    &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
			}
			env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(89),
				envelope.Party{Kind: envelope.PartyUser, ID: "89"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentText, Text: "hello"})
			h.seedAdmitted(t, env, 409, "ses_1", "msg_in")

			outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
			if err != nil {
				t.Fatalf("Reconcile() error = %v, want the permanent error settled", err)
			}
			if outcome != ReconcileUnrecoverable {
				t.Fatalf("outcome = %v, want unrecoverable", outcome)
			}
			assertSettledWithNotice(t, h, env)
		})
	}
}

// assertSettledWithNotice checks the terminal shape shared by settled turns:
// healed ack, one system notice, and a processed inbound row.
func assertSettledWithNotice(t *testing.T, h *routerHarness, env envelope.Envelope) {
	t.Helper()
	if len(h.outbound.events) != 2 || h.outbound.events[1].ID != "sys_"+env.ID {
		t.Fatalf("outbound events = %+v, want healed ack and a system notice", h.outbound.events)
	}
	if !strings.HasPrefix(h.outbound.events[1].Content.Text, SystemNoticeMarker) {
		t.Errorf("notice = %q, want the %q marker", h.outbound.events[1].Content.Text, SystemNoticeMarker)
	}
	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	item, _, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		t.Fatalf("GetInboxByEnvelope() error: %v", err)
	}
	if item.ProcessedAt == nil {
		t.Error("settled row was not marked processed; it will loop forever")
	}
}

func TestReconcileLeavesRunningTurnAlone(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{
		delivered: true,
		collected: &opencode.TurnResult{SessionID: "ses_1", InputID: "msg_in", ResponseText: "too early"},
		// Busy is newer than Idle: the service still owns the turn.
		session:   &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextFound: true,
	}

	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(81),
		envelope.Party{Kind: envelope.PartyUser, ID: "81"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 401, "ses_1", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileStillPending {
		t.Fatalf("outcome = %v, want still-pending", outcome)
	}
	if len(h.outbound.events) != 1 || h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
		t.Errorf("outbound events = %+v, want only the healed ack while the turn runs", h.outbound.events)
	}
}

// TestReconcileHealsMissingAdmissionAck covers the crash window between
// persisting the marker and enqueuing the ack. Recovery must enqueue it, and
// repeated passes must still leave exactly one row.
func TestReconcileHealsMissingAdmissionAck(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{
		session:   &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextFound: true,
	}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(85),
		envelope.Party{Kind: envelope.PartyUser, ID: "85"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 405, "ses_1", "msg_in")

	h.router.outbound = func(_ context.Context, _ registry.Agent, out envelope.Envelope) error {
		st, release, err := h.pool.Acquire(h.store)
		if err != nil {
			return err
		}
		defer release()
		_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
			EnvelopeID: out.ID,
			ChatID:     85,
			Text:       out.Content.Text,
		})
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
		if err != nil {
			t.Fatalf("Reconcile() attempt %d error: %v", attempt, err)
		}
		if outcome != ReconcileStillPending {
			t.Fatalf("attempt %d outcome = %v, want still-pending", attempt, outcome)
		}
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(85)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	if len(outbox) != 1 || outbox[0].EnvelopeID != ackEnvelopeID(env.ID) {
		t.Fatalf("outbox = %+v, want exactly one healed ack", outbox)
	}
}

// TestReconcileInterruptsBusyTurnAfterGrace closes the "busy forever" hole: a
// session that is still in flight past the grace window is interrupted and the
// message is settled as incomplete instead of being left pending forever.
func TestReconcileInterruptsBusyTurnAfterGrace(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		session:   &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextFound: true,
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(79),
		envelope.Party{Kind: envelope.PartyUser, ID: "79"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 410, "ses_busy", "msg_in", 0, time.Now().UTC().Add(-2*resumeGraceWindow))

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	if len(prober.interrupted) != 1 || prober.interrupted[0] != "ses_busy" {
		t.Errorf("interrupted = %v, want the busy session interrupted", prober.interrupted)
	}
	assertSettledWithNotice(t, h, env)
	if len(h.outbound.events) == 2 && !strings.Contains(h.outbound.events[1].Content.Text, "still running") {
		t.Errorf("notice = %q, want the busy-turn wording", h.outbound.events[1].Content.Text)
	}
}

// TestReconcileCollectsFinishedTurnWithoutInterruptingLaterTurn covers session
// ownership: if a later input already delivered, the marker's turn is done and
// recovery must collect it without touching the later turn.
func TestReconcileCollectsFinishedTurnWithoutInterruptingLaterTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		delivered: true,
		collected: &opencode.TurnResult{SessionID: "ses_busy", InputID: "msg_in", ResponseText: "the first answer"},
		session:   &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextInput: "msg_later", nextFound: true,
		pendingByID: map[string]bool{"msg_in": false, "msg_later": false},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(78),
		envelope.Party{Kind: envelope.PartyUser, ID: "78"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 411, "ses_busy", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileRecovered {
		t.Fatalf("outcome = %v, want recovered", outcome)
	}
	if len(prober.interrupted) != 0 {
		t.Errorf("interrupted = %v, want the later turn left alone", prober.interrupted)
	}
	if len(h.outbound.events) != 2 || h.outbound.events[1].Content.Text != "the first answer" {
		t.Fatalf("outbound = %+v, want healed ack and the first answer", h.outbound.events)
	}
}

// TestReconcileInterruptsWhenLaterInputIsQueued establishes the other side of
// ownership: a queued next input means the marker's turn is still the head.
func TestReconcileInterruptsWhenLaterInputIsQueued(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		session:   &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextInput: "msg_later", nextFound: true,
		pendingByID: map[string]bool{"msg_in": false, "msg_later": true},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(89),
		envelope.Party{Kind: envelope.PartyUser, ID: "89"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 412, "ses_busy", "msg_in", 0, time.Now().UTC().Add(-2*resumeGraceWindow))

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	if len(prober.interrupted) != 1 || prober.interrupted[0] != "ses_busy" {
		t.Errorf("interrupted = %v, want the head turn interrupted", prober.interrupted)
	}
	assertSettledWithNotice(t, h, env)
}

// TestReconcileLeavesQueuedMarkerAlone covers an input that has not executed:
// it must never cause an interrupt, even past the grace window.
func TestReconcileLeavesQueuedMarkerAlone(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		session:   &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextInput: "msg_later", nextFound: true,
		pendingByID: map[string]bool{"msg_in": true, "msg_later": true},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(90),
		envelope.Party{Kind: envelope.PartyUser, ID: "90"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 413, "ses_busy", "msg_in", 0, time.Now().UTC().Add(-2*resumeGraceWindow))

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileStillPending {
		t.Fatalf("outcome = %v, want still-pending", outcome)
	}
	if len(prober.interrupted) != 0 {
		t.Errorf("interrupted = %v, want no interrupt for a queued input", prober.interrupted)
	}
}

// TestReconcileBusyStopNotConfirmed checks the honest wording when an interrupt
// is rejected: the notice must not claim the turn was stopped.
func TestReconcileBusyStopNotConfirmed(t *testing.T) {
	h := newRouterHarness(t, 4)
	prober := &fakeProber{
		session:      &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
		nextFound:    true,
		interruptErr: &opencode.APIError{Status: http.StatusForbidden},
	}
	h.router.turn = prober
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(91),
		envelope.Party{Kind: envelope.PartyUser, ID: "91"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedStranded(t, env, 414, "ses_busy", "msg_in", 0, time.Now().UTC().Add(-2*resumeGraceWindow))

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
	assertSettledWithNotice(t, h, env)
	if len(h.outbound.events) == 2 {
		notice := h.outbound.events[1].Content.Text
		if !strings.Contains(notice, "could not be confirmed") {
			t.Errorf("notice = %q, want the unconfirmed-stop wording", notice)
		}
		if strings.Contains(notice, "was interrupted") {
			t.Errorf("notice = %q, must not claim an unconfirmed interruption", notice)
		}
	}
}

func TestReconcileClassifiesIdleTurn(t *testing.T) {
	cases := []struct {
		name    string
		pending bool
		want    ReconcileOutcome
	}{
		{name: "queued but never executed", pending: true, want: ReconcileStranded},
		{name: "consumed with no reply", pending: false, want: ReconcileUnrecoverable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRouterHarness(t, 4)
			h.router.turn = &fakeProber{
				delivered: false,
				pending:   tc.pending,
				session:   &opencode.SessionInfo{ID: "ses_1", Time: opencode.SessionTime{Busy: 1, Idle: 2}},
			}
			env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(82),
				envelope.Party{Kind: envelope.PartyUser, ID: "82"},
				envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
				envelope.Content{Type: envelope.ContentText, Text: "hello"})
			h.seedAdmitted(t, env, 402, "ses_1", "msg_in")

			outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
			if err != nil {
				t.Fatalf("Reconcile() error: %v", err)
			}
			if outcome != tc.want {
				t.Fatalf("outcome = %v, want %v", outcome, tc.want)
			}
			if tc.pending {
				if len(h.outbound.events) != 1 || h.outbound.events[0].ID != ackEnvelopeID(env.ID) {
					t.Errorf("outbound events = %+v, want only the healed ack", h.outbound.events)
				}
				return
			}
			// A consumed turn with nothing to collect is terminal: it must be
			// settled with an explicit notice, not retried forever.
			if len(h.outbound.events) != 2 || h.outbound.events[1].ID != "sys_"+env.ID {
				t.Fatalf("outbound events = %+v, want healed ack and a system notice", h.outbound.events)
			}
			if !strings.HasPrefix(h.outbound.events[1].Content.Text, SystemNoticeMarker) {
				t.Errorf("notice = %q, want the %q marker", h.outbound.events[1].Content.Text, SystemNoticeMarker)
			}
			st, release, err := h.pool.Acquire(h.store)
			if err != nil {
				t.Fatalf("Acquire() error: %v", err)
			}
			defer release()
			item, _, err := st.CommTG().GetInboxByEnvelope(env.ID)
			if err != nil {
				t.Fatalf("GetInboxByEnvelope() error: %v", err)
			}
			if item.ProcessedAt == nil {
				t.Error("settled row was not marked processed; it will loop forever")
			}
		})
	}
}

func TestReconcileWithoutMarkerIsPending(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.router.turn = &fakeProber{}
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(83),
		envelope.Party{Kind: envelope.PartyUser, ID: "83"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedInbound(t, env, 403)

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if outcome != ReconcileStillPending {
		t.Fatalf("outcome = %v, want still-pending", outcome)
	}
}

func TestReconcileWithoutProberIsUnrecoverable(t *testing.T) {
	h := newRouterHarness(t, 4)
	// The harness default runner does not implement TurnProber.
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(84),
		envelope.Party{Kind: envelope.PartyUser, ID: "84"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	h.seedAdmitted(t, env, 404, "ses_1", "msg_in")

	outcome, err := h.router.Reconcile(context.Background(), h.agent, env)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want unsupported error")
	}
	if outcome != ReconcileUnrecoverable {
		t.Fatalf("outcome = %v, want unrecoverable", outcome)
	}
}

func TestRouterReplyEnvelopeIDIsDeterministic(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(70),
		envelope.Party{Kind: envelope.PartyUser, ID: "70"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})

	// A telegram-style outbound that enqueues exactly as the adapter does.
	h.router.outbound = func(_ context.Context, _ registry.Agent, out envelope.Envelope) error {
		st, release, err := h.pool.Acquire(h.store)
		if err != nil {
			return err
		}
		defer release()
		_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
			EnvelopeID: out.ID,
			ChatID:     70,
			Text:       out.Content.Text,
		})
		return err
	}

	st, release, err := h.pool.Acquire(h.store)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()

	result := &opencode.TurnResult{SessionID: "ses_1", InputID: "msg_in", ResponseText: "reply"}
	for attempt := 0; attempt < 2; attempt++ {
		if err := h.router.sendReply(context.Background(), st, h.agent, env, result, "reply"); err != nil {
			t.Fatalf("sendReply() attempt %d error: %v", attempt, err)
		}
	}

	want := replyEnvelopeID(env.ID)
	stored, ok, err := st.CommTG().GetOutboxByEnvelope(want)
	if err != nil || !ok {
		t.Fatalf("GetOutboxByEnvelope(%q) = %+v, %v, %v", want, stored, ok, err)
	}
	items, err := st.CommTG().DueOutbound(time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("DueOutbound() error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("outbox rows = %d, want 1 (duplicate reply)", len(items))
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
	// The marker is now persisted at admission so the turn is recoverable; the
	// reply itself is protected by pending_reply, which replay delivers first.
	if item.OpenCodeMessageID != "msg_in" {
		t.Errorf("marker = %q, want the admitted marker msg_in", item.OpenCodeMessageID)
	}
	if item.AdmittedAt == nil {
		t.Error("admission time was not recorded")
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

func TestRouterNewCommandResetsSession(t *testing.T) {
	h := newRouterHarness(t, 4)
	thread := envelope.TelegramThread(70)
	h.seedSession(t, thread, "ses_old")

	env := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "70"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/new"})
	env.Meta = map[string]any{"telegram_message_id": float64(88)}
	h.seedInbound(t, env, 400)

	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if h.turn.calls != 0 {
		t.Errorf("turn calls = %d, want 0", h.turn.calls)
	}
	if len(h.outbound.events) != 1 {
		t.Fatalf("outbound events = %d, want 1", len(h.outbound.events))
	}
	out := h.outbound.events[0]
	if out.Content.Text != NewSessionReply {
		t.Errorf("outbound text = %q, want %q", out.Content.Text, NewSessionReply)
	}
	if out.Thread != env.Thread {
		t.Errorf("outbound thread = %q, want %q", out.Thread, env.Thread)
	}
	if out.Meta["reply_to_message_id"] != float64(88) {
		t.Errorf("outbound meta = %+v, want reply_to_message_id preserved", out.Meta)
	}
	if h.hasSession(t, thread) {
		t.Error("thread mapping still exists after /new")
	}
}

func TestRouterNewCommandWithBotSuffix(t *testing.T) {
	h := newRouterHarness(t, 4)
	thread := envelope.TelegramThread(71)
	h.seedSession(t, thread, "ses_old")

	env := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "71"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/new@acolyte_bot"})
	env.Meta = map[string]any{botUsernameMeta: "acolyte_bot"}
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if h.turn.calls != 0 {
		t.Errorf("turn calls = %d, want 0", h.turn.calls)
	}
	if h.hasSession(t, thread) {
		t.Error("thread mapping still exists after /new@acolyte_bot")
	}
}

func TestRouterNewCommandForAnotherBotRunsTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	thread := envelope.TelegramThread(74)
	h.seedSession(t, thread, "ses_old")

	env := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "74"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/new@other_bot"})
	env.Meta = map[string]any{botUsernameMeta: "acolyte_bot"}
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if h.turn.calls != 1 {
		t.Fatalf("turn calls = %d, want 1", h.turn.calls)
	}
	if len(h.turn.prompts) != 1 || h.turn.prompts[0] != "/new@other_bot" {
		t.Errorf("prompts = %v, want [/new@other_bot]", h.turn.prompts)
	}
	if !h.hasSession(t, thread) {
		t.Error("thread mapping was reset by a command aimed at another bot")
	}
}

func TestRouterNewCommandWithoutBotUsernameRunsTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	thread := envelope.TelegramThread(75)
	h.seedSession(t, thread, "ses_old")

	env := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "75"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/new@acolyte_bot"})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if h.turn.calls != 1 {
		t.Fatalf("turn calls = %d, want 1 when the bot username is unknown", h.turn.calls)
	}
	if !h.hasSession(t, thread) {
		t.Error("thread mapping was reset without a resolved bot username")
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		text    string
		name    string
		mention string
		ok      bool
	}{
		{text: "/new", name: "new", ok: true},
		{text: " /new ", name: "new", ok: true},
		{text: "/NEW", name: "new", ok: true},
		{text: "/new extra", name: "new", ok: true},
		{text: "/new@acolyte_bot", name: "new", mention: "acolyte_bot", ok: true},
		{text: "/new@Acolyte_Bot extra", name: "new", mention: "acolyte_bot", ok: true},
		{text: "/new@", ok: false},
		{text: "/", ok: false},
		{text: "hello", ok: false},
	}
	for _, tt := range tests {
		name, mention, ok := parseCommand(tt.text)
		if name != tt.name || mention != tt.mention || ok != tt.ok {
			t.Errorf("parseCommand(%q) = %q, %q, %v; want %q, %q, %v",
				tt.text, name, mention, ok, tt.name, tt.mention, tt.ok)
		}
	}
}

func TestCommandAddressedToBot(t *testing.T) {
	tests := []struct {
		name    string
		mention string
		meta    map[string]any
		want    bool
	}{
		{name: "unaddressed", want: true},
		{name: "addressed to this bot", mention: "acolyte_bot", meta: map[string]any{botUsernameMeta: "acolyte_bot"}, want: true},
		{name: "case insensitive", mention: "acolyte_bot", meta: map[string]any{botUsernameMeta: "Acolyte_Bot"}, want: true},
		{name: "another bot", mention: "other_bot", meta: map[string]any{botUsernameMeta: "acolyte_bot"}, want: false},
		{name: "unknown bot username", mention: "acolyte_bot", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandAddressedToBot(tt.mention, tt.meta); got != tt.want {
				t.Errorf("commandAddressedToBot(%q, %v) = %v, want %v", tt.mention, tt.meta, got, tt.want)
			}
		})
	}
}

func TestRouterUnknownCommandRunsTurn(t *testing.T) {
	h := newRouterHarness(t, 4)
	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(72),
		envelope.Party{Kind: envelope.PartyUser, ID: "72"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/help"})
	if err := h.router.HandleInbound(context.Background(), h.agent, env); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if h.turn.calls != 1 {
		t.Fatalf("turn calls = %d, want 1", h.turn.calls)
	}
	if len(h.turn.prompts) != 1 || h.turn.prompts[0] != "/help" {
		t.Errorf("prompts = %v, want [/help]", h.turn.prompts)
	}
}

func TestRouterNewCommandConcurrency(t *testing.T) {
	h := newRouterHarness(t, 4)
	h.turn.delay = 50 * time.Millisecond
	thread := envelope.TelegramThread(73)
	h.seedSession(t, thread, "ses_old")

	slow := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "73"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "slow work"})
	done := make(chan error, 1)
	go func() { done <- h.router.HandleInbound(context.Background(), h.agent, slow) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		h.turn.mu.Lock()
		started := h.turn.calls > 0
		h.turn.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow turn never started")
		}
		time.Sleep(time.Millisecond)
	}

	reset := envelope.New(envelope.DirectionIn, "telegram", thread,
		envelope.Party{Kind: envelope.PartyUser, ID: "73"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "/new"})
	resetDone := make(chan error, 1)
	go func() { resetDone <- h.router.HandleInbound(context.Background(), h.agent, reset) }()

	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatalf("HandleInbound(/new) error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/new blocked behind the in-flight turn")
	}
	if err := <-done; err != nil {
		t.Fatalf("HandleInbound(slow) error: %v", err)
	}

	if h.turn.maxActive != 1 {
		t.Errorf("max active turns = %d, want 1 for the same thread", h.turn.maxActive)
	}
	if len(h.outbound.events) != 3 {
		t.Fatalf("outbound events = %d, want ack, reply, and /new reply", len(h.outbound.events))
	}
	if h.outbound.events[1].Content.Text != "reply" {
		t.Errorf("slow turn reply = %q, want %q", h.outbound.events[1].Content.Text, "reply")
	}
	if h.outbound.events[2].Content.Text != NewSessionReply {
		t.Errorf("last outbound text = %q, want %q after the turn finished",
			h.outbound.events[2].Content.Text, NewSessionReply)
	}
	if h.hasSession(t, thread) {
		t.Error("thread mapping still exists after /new")
	}
}
