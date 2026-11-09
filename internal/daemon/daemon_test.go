package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/storepool"
)

func commtgItem(env envelope.Envelope) commtg.InboxItem {
	chatID, _ := envelope.ParseTelegramThread(env.Thread)
	return commtg.InboxItem{
		TGUpdateID: 100,
		ChatID:     chatID,
		EnvelopeID: env.ID,
		ReceivedAt: time.Now().UTC(),
	}
}

type fakeComm struct {
	mu      sync.Mutex
	started bool
	stopped bool
	sink    comm.InboundSink
	sends   []envelope.Envelope
}

func (f *fakeComm) Name() string { return "telegram" }

func (f *fakeComm) Start(_ context.Context, sink comm.InboundSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	f.sink = sink
	return nil
}

func (f *fakeComm) Send(env envelope.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, env)
	return nil
}

func (f *fakeComm) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeComm) isStarted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeComm) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *fakeComm) sent() []envelope.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]envelope.Envelope(nil), f.sends...)
}

type fakeFactory struct {
	mu     sync.Mutex
	comms  map[string]*fakeComm
	tokens map[string]string
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{comms: map[string]*fakeComm{}, tokens: map[string]string{}}
}

func (f *fakeFactory) make(agent registry.Agent, token string, _ []string, _ *store.Store, _ func() time.Time) (comm.Communication, error) {
	comm := &fakeComm{}
	f.mu.Lock()
	f.comms[agent.Name] = comm
	f.tokens[agent.Name] = token
	f.mu.Unlock()
	return comm, nil
}

func (f *fakeFactory) get(name string) *fakeComm {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.comms[name]
}

func (f *fakeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.comms)
}

type fakeTurn struct {
	mu          sync.Mutex
	userCalls   int
	synthCalls  int
	userResult  *opencode.TurnResult
	synthResult *opencode.TurnResult
	userErr     error
	synthErr    error
}

func (f *fakeTurn) RunUserTurn(_ context.Context, _, _ string, _ opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls++
	if f.userResult == nil {
		f.userResult = &opencode.TurnResult{SessionID: "ses_user", InputID: "msg_in", ResponseText: "reply", MessageIDs: []string{"msg_out"}}
	}
	copy := *f.userResult
	return &copy, f.userErr
}

func (f *fakeTurn) RunSyntheticTurn(_ context.Context, _ string, _ opencode.SyntheticEvent, _ opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synthCalls++
	if f.synthResult == nil {
		f.synthResult = &opencode.TurnResult{SessionID: "ses_synth", InputID: "msg_synth_in", ResponseText: "synthetic reply", MessageIDs: []string{"msg_synth_out"}}
	}
	copy := *f.synthResult
	return &copy, f.synthErr
}

func (f *fakeTurn) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userCalls, f.synthCalls
}

func setupDaemon(t *testing.T, names ...string) (*registry.Registry, *storepool.Pool, map[string]string) {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	clearToken := func() {
		old, had := os.LookupEnv("TELEGRAM_BOT_TOKEN")
		_ = os.Unsetenv("TELEGRAM_BOT_TOKEN")
		t.Cleanup(func() {
			if had {
				_ = os.Setenv("TELEGRAM_BOT_TOKEN", old)
			} else {
				_ = os.Unsetenv("TELEGRAM_BOT_TOKEN")
			}
		})
	}
	clearToken()
	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	pool := storepool.New()
	t.Cleanup(func() { _ = pool.CloseAll() })

	workspaces := map[string]string{}
	for _, name := range names {
		ws := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := reg.Register(registry.Agent{Name: name, Workspace: ws}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		st, err := store.Open(ws)
		if err != nil {
			t.Fatalf("open store %s: %v", name, err)
		}
		if err := st.CommTG().SetSetting("channels", `["telegram"]`); err != nil {
			t.Fatalf("channels setting: %v", err)
		}
		if err := st.CommTG().SetSetting("telegram.allowed_users", `["@alice"]`); err != nil {
			t.Fatalf("allowed users setting: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("TELEGRAM_BOT_TOKEN=token-"+name+"\n"), 0600); err != nil {
			t.Fatalf("write .env %s: %v", name, err)
		}
		workspaces[name] = ws
	}
	return reg, pool, workspaces
}

func startDaemon(t *testing.T, reg *registry.Registry, pool *storepool.Pool, turn TurnRunner, factory CommFactory) *Service {
	t.Helper()
	now := time.Now().UTC()
	svc, err := Start(context.Background(), Options{
		Registry:        reg,
		Stores:          pool,
		OpenCode:        turn,
		RefreshInterval: time.Hour,
		Now:             func() time.Time { return now },
		CommFactory:     factory,
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func TestDaemonStartsAndRoutesInbound(t *testing.T) {
	reg, pool, workspaces := setupDaemon(t, "valet")
	factory := newFakeFactory()
	turn := &fakeTurn{}
	svc := startDaemon(t, reg, pool, turn, factory.make)

	comm := factory.get("valet")
	if comm == nil || !comm.isStarted() {
		t.Fatalf("valet adapter not started: %+v", comm)
	}

	env := envelope.New(envelope.DirectionIn, "telegram", envelope.TelegramThread(10),
		envelope.Party{Kind: envelope.PartyUser, ID: "10", Name: "alice"},
		envelope.Party{Kind: envelope.PartyAgent, ID: "valet"},
		envelope.Content{Type: envelope.ContentText, Text: "hello"})
	st, release, err := pool.Acquire(workspaces["valet"])
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtgItem(env)); err != nil {
		t.Fatalf("RecordInbound() error: %v", err)
	}
	release()

	if err := comm.sink(context.Background(), env); err != nil {
		t.Fatalf("inbound sink error: %v", err)
	}

	sends := comm.sent()
	if len(sends) != 1 || sends[0].Content.Text != "reply" {
		t.Fatalf("sends = %+v, want reply", sends)
	}
	st, release, err = pool.Acquire(workspaces["valet"])
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	session, ok, err := st.Orch().GetSessionByThread(env.Thread)
	if err != nil || !ok || session.OpenCodeSessionID != "ses_user" {
		t.Errorf("session = %+v, %v, %v", session, ok, err)
	}
	inbox, ok, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil || !ok || inbox.OpenCodeMessageID != "msg_in" {
		t.Errorf("inbox = %+v, %v, %v", inbox, ok, err)
	}

	svc.Stop()
	if !comm.isStopped() {
		t.Error("adapter was not stopped")
	}
}

func TestDaemonSkipsDuplicateTokens(t *testing.T) {
	reg, pool, workspaces := setupDaemon(t, "one", "two")
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(workspaces[name], ".env"), []byte("TELEGRAM_BOT_TOKEN=shared\n"), 0600); err != nil {
			t.Fatalf("write .env: %v", err)
		}
	}
	factory := newFakeFactory()
	svc := startDaemon(t, reg, pool, &fakeTurn{}, factory.make)

	if factory.count() != 1 {
		t.Fatalf("adapters = %d, want 1 for a shared token", factory.count())
	}
	svc.Stop()
}

func TestDaemonNotifyUserAndInbox(t *testing.T) {
	reg, pool, workspaces := setupDaemon(t, "valet")
	factory := newFakeFactory()
	turn := &fakeTurn{}
	svc := startDaemon(t, reg, pool, turn, factory.make)
	agent := registry.Agent{Name: "valet", Workspace: workspaces["valet"]}

	if err := svc.NotifyUser(context.Background(), agent, "hello"); err == nil {
		t.Fatal("NotifyUser(no chat) error = nil, want error")
	}
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, time.Now().UTC()); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	release()
	if err := svc.NotifyUser(context.Background(), agent, "hello"); err != nil {
		t.Fatalf("NotifyUser() error: %v", err)
	}
	if count := svc.NotifyAll(context.Background(), "broadcast"); count != 1 {
		t.Errorf("NotifyAll() = %d, want 1", count)
	}

	reply, sessionID, err := svc.Inbox(context.Background(), agent, "", "ping", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if reply != "synthetic reply" || sessionID != "ses_synth" {
		t.Errorf("Inbox() = %q, %q", reply, sessionID)
	}
	if userCalls, synthCalls := turn.counts(); userCalls != 0 || synthCalls != 1 {
		t.Errorf("turn calls = user:%d synth:%d", userCalls, synthCalls)
	}
	if len(factory.get("valet").sent()) != 0 {
		t.Errorf("inbox should not send directly; outbox rows are delivered by the adapter")
	}

	st, release, err = pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	if len(outbox) != 3 {
		t.Errorf("outbox rows = %d, want 3 (notify, broadcast, inbox)", len(outbox))
	}
}

func TestDaemonInboxWithoutChatStillRuns(t *testing.T) {
	reg, pool, workspaces := setupDaemon(t, "valet")
	factory := newFakeFactory()
	turn := &fakeTurn{}
	svc := startDaemon(t, reg, pool, turn, factory.make)
	agent := registry.Agent{Name: "valet", Workspace: workspaces["valet"]}

	reply, sessionID, err := svc.Inbox(context.Background(), agent, "", "ping", nil)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if reply != "synthetic reply" || sessionID != "ses_synth" {
		t.Errorf("Inbox() = %q, %q", reply, sessionID)
	}
	if _, synthCalls := turn.counts(); synthCalls != 1 {
		t.Errorf("synth calls = %d, want 1", synthCalls)
	}
	st, release, err := pool.Acquire(agent.Workspace)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	outbox, err := st.CommTG().ListOutbox(0)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	if len(outbox) != 0 {
		t.Errorf("outbox = %+v, want none without a chat", outbox)
	}
	var message string
	if err := st.DB().QueryRow(`SELECT message FROM orch_logs ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(message, "no Telegram chat") {
		t.Errorf("log = %q, want no-channel warning", message)
	}
}

func TestDaemonRefreshAddsAndRemovesAgents(t *testing.T) {
	reg, pool, _ := setupDaemon(t, "one")
	factory := newFakeFactory()
	svc := startDaemon(t, reg, pool, &fakeTurn{}, factory.make)
	if factory.get("one") == nil {
		t.Fatal("one adapter missing")
	}

	ws := filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir two: %v", err)
	}
	if err := reg.Register(registry.Agent{Name: "two", Workspace: ws}); err != nil {
		t.Fatalf("register two: %v", err)
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open two store: %v", err)
	}
	_ = st.CommTG().SetSetting("channels", `["telegram"]`)
	_ = st.CommTG().SetSetting("telegram.allowed_users", `["@alice"]`)
	_ = st.Close()
	if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("TELEGRAM_BOT_TOKEN=token-two\n"), 0600); err != nil {
		t.Fatalf("write two .env: %v", err)
	}
	svc.refresh()
	if factory.get("two") == nil {
		t.Fatal("two adapter missing after refresh")
	}

	if _, err := reg.Delete("one"); err != nil {
		t.Fatalf("delete one: %v", err)
	}
	svc.refresh()
	if !factory.get("one").isStopped() {
		t.Error("one adapter was not stopped after delete")
	}
}
