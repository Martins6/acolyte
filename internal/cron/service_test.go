package cron

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

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/lock"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

type fakeSink struct {
	notified    []string
	inboxed     []string
	inboxInputs []string
	notifyErr   error
	inboxErr    error
}

func (f *fakeSink) NotifyUser(_ context.Context, _ registry.Agent, text string) error {
	f.notified = append(f.notified, text)
	return f.notifyErr
}

func (f *fakeSink) Inbox(_ context.Context, _ registry.Agent, sessionID, text string, _ map[string]any) (string, string, error) {
	f.inboxed = append(f.inboxed, text)
	f.inboxInputs = append(f.inboxInputs, sessionID)
	return text, "ses_inbox", f.inboxErr
}

type fakeTurn struct {
	calls          int
	response       string
	events         []opencode.SyntheticEvent
	err            error
	emptySession   bool
	postCollectErr error
}

func (f *fakeTurn) RunSyntheticTurn(_ context.Context, sessionID string, ev opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.calls++
	f.events = append(f.events, ev)
	returnedSession := "ses_fake"
	if f.emptySession {
		returnedSession = ""
	}
	result := &opencode.TurnResult{SessionID: returnedSession, InputID: "msg_fake", ResponseText: f.response}
	// Mirror the real client: the marker is handed over at admission, before
	// the turn is collected.
	if cfg.OnAdmitted != nil {
		if err := cfg.OnAdmitted(opencode.Admitted{SessionID: result.SessionID, InputID: result.InputID}); err != nil {
			return nil, err
		}
	}
	if f.err == nil && cfg.OnCollected != nil {
		if err := cfg.OnCollected(result); err != nil {
			return result, err
		}
	}
	if f.err == nil && f.postCollectErr != nil {
		return result, f.postCollectErr
	}
	return result, f.err
}

type fakeRoutine struct {
	name string
	out  string
}

func (f fakeRoutine) Name() string        { return f.name }
func (f fakeRoutine) Owner() string       { return "test" }
func (f fakeRoutine) Description() string { return "fake routine" }
func (f fakeRoutine) Args() []routine.ArgSpec {
	return nil
}
func (f fakeRoutine) Run(context.Context, routine.Env, map[string]any) (string, error) {
	return f.out, nil
}

type failingRoutine struct {
	name string
	out  string
	err  error
}

func (f failingRoutine) Name() string        { return f.name }
func (f failingRoutine) Owner() string       { return "test" }
func (f failingRoutine) Description() string { return "failing routine" }
func (f failingRoutine) Args() []routine.ArgSpec {
	return nil
}
func (f failingRoutine) Run(context.Context, routine.Env, map[string]any) (string, error) {
	return f.out, f.err
}

type sessionProbeRoutine struct {
	captured *routine.Env
}

func (sessionProbeRoutine) Name() string        { return "session-probe" }
func (sessionProbeRoutine) Owner() string       { return "test" }
func (sessionProbeRoutine) Description() string { return "captures the routine session" }
func (sessionProbeRoutine) Args() []routine.ArgSpec {
	return nil
}
func (r sessionProbeRoutine) Run(ctx context.Context, env routine.Env, _ map[string]any) (string, error) {
	*r.captured = env
	_, _, err := env.Inbox(ctx, "ping", nil)
	return "", err
}

func setupCron(t *testing.T) (*registry.Registry, string, time.Time) {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Register(registry.Agent{Name: "valet", Workspace: ws}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg, ws, time.Now().UTC()
}

func addJob(t *testing.T, ws string, job orch.CronJob) {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Orch().AddCronJob(job); err != nil {
		t.Fatalf("add job: %v", err)
	}
}

func cronLogMessages(t *testing.T, ws string) []string {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	messages := make([]string, 0, len(logs))
	for _, entry := range logs {
		messages = append(messages, entry.Message)
	}
	return messages
}

func startTestService(t *testing.T, reg *registry.Registry, routines *routine.Registry, sink Sink, turn TurnRunner, shell string, now time.Time) *Service {
	t.Helper()
	service, err := Start(context.Background(), Options{
		Registry:        reg,
		Routines:        routines,
		Sink:            sink,
		OpenCode:        turn,
		Shell:           shell,
		Interval:        time.Hour,
		RefreshInterval: time.Hour,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return service
}

// collectingTurn adds the optional recovery half the cron service probes for.
type collectingTurn struct {
	fakeTurn
	delivered    bool
	text         string
	collected    []string
	session      *opencode.SessionInfo
	sessionErr   error
	collectErr   error
	inboxPending bool
	inboxErr     error
}

func (f *collectingTurn) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	if f.session == nil {
		return &opencode.SessionInfo{ID: "ses_fake"}, nil
	}
	return f.session, nil
}

func (f *collectingTurn) CollectDelivered(_ context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error) {
	f.collected = append(f.collected, markerID)
	if f.collectErr != nil {
		return nil, false, f.collectErr
	}
	return &opencode.TurnResult{SessionID: sessionID, InputID: markerID, ResponseText: f.text}, f.delivered, nil
}

func (f *collectingTurn) InboxPending(context.Context, string, string) (bool, error) {
	return f.inboxPending, f.inboxErr
}

// countingCollector is safe for the concurrent recovery test.
type countingCollector struct {
	mu    sync.Mutex
	calls int
}

func (c *countingCollector) RunSyntheticTurn(context.Context, string, opencode.SyntheticEvent, opencode.TurnConfig) (*opencode.TurnResult, error) {
	return nil, errors.New("countingCollector does not run turns")
}

func (c *countingCollector) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	return &opencode.SessionInfo{ID: "ses_once"}, nil
}

func (c *countingCollector) CollectDelivered(_ context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &opencode.TurnResult{SessionID: sessionID, InputID: markerID, ResponseText: "once"}, true, nil
}

func (c *countingCollector) InboxPending(context.Context, string, string) (bool, error) {
	return false, nil
}

func (c *countingCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type countingSink struct {
	mu       sync.Mutex
	notified int
}

func (c *countingSink) NotifyUser(context.Context, registry.Agent, string) error {
	c.mu.Lock()
	c.notified++
	c.mu.Unlock()
	return nil
}

func (c *countingSink) Inbox(context.Context, registry.Agent, string, string, map[string]any) (string, string, error) {
	return "", "", nil
}

func (c *countingSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notified
}

// collectingRoutine simulates a routine that consumes one turn and then fails
// in its post-turn work.
type collectingRoutine struct{}

func (collectingRoutine) Name() string        { return "turn-taking" }
func (collectingRoutine) Owner() string       { return "test" }
func (collectingRoutine) Description() string { return "test routine" }
func (collectingRoutine) Args() []routine.ArgSpec {
	return nil
}
func (collectingRoutine) Run(_ context.Context, env routine.Env, _ map[string]any) (string, error) {
	if err := env.OnAdmitted(opencode.Admitted{SessionID: "ses_r", InputID: "msg_r"}); err != nil {
		return "", err
	}
	if err := env.OnCollected(&opencode.TurnResult{SessionID: "ses_r", InputID: "msg_r"}); err != nil {
		return "", err
	}
	return "", errors.New("post-turn failure")
}

// TestRoutineKeepsRunPendingAfterTurnCollected ensures the routine's durable
// state survives the gap between a collected turn and routine completion.
func TestRoutineKeepsRunPendingAfterTurnCollected(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "turn-taking", Kind: "routine", Spec: "@every 1h", Routine: "turn-taking",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	routines := routine.NewRegistry()
	if err := routines.Register(collectingRoutine{}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	svc := startTestService(t, reg, routines, &fakeSink{}, &fakeTurn{}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("turn-taking")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	pending, ok, err := pair.store.Orch().CronJobPending(job.ID)
	if err != nil {
		t.Fatalf("CronJobPending() error: %v", err)
	}
	if !ok {
		t.Fatal("pending run was lost after the turn was collected")
	}
	if pending.Kind != "routine-run" || pending.MarkerID == "" {
		t.Errorf("pending = %+v, want a run-level routine-run state", pending)
	}
}

// TestInboxRouteRecoversAdmittedTurn ensures a route whose synthetic input was
// already admitted is collected instead of re-admitted.
func TestInboxRouteRecoversAdmittedTurn(t *testing.T) {
	reg, _, now := setupCron(t)
	turn := &collectingTurn{delivered: true, text: "the event result"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	if err := pair.store.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}

	if err := pair.store.Orch().AddCronRoute(orch.CronRoute{
		ID:                "route_inbox",
		AgentTarget:       "valet",
		Action:            "inbox",
		Text:              "event",
		OpenCodeSessionID: "ses_x",
		SourceMessageID:   "msg_x",
		CreatedAt:         now,
	}); err != nil {
		t.Fatalf("AddCronRoute() error: %v", err)
	}

	svc.retryRoutes(context.Background(), pair)

	if len(turn.collected) != 1 || turn.collected[0] != "msg_x" {
		t.Errorf("collected = %v, want the already-admitted marker", turn.collected)
	}
	if turn.calls != 0 {
		t.Errorf("synthetic turns = %d, want 0 (never re-admit)", turn.calls)
	}
	routes, err := pair.store.Orch().ListCronRoutes()
	if err != nil || len(routes) != 1 || routes[0].DeliveredAt == nil {
		t.Errorf("routes = %+v, %v; want delivered after collection", routes, err)
	}
}

// TestInboxRouteLeavesInFlightTurnPending ensures a route is not settled while
// its admitted synthetic turn is still running.
func TestInboxRouteLeavesInFlightTurnPending(t *testing.T) {
	reg, _, now := setupCron(t)
	turn := &collectingTurn{
		delivered: false,
		session:   &opencode.SessionInfo{ID: "ses_x", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
	}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	if err := pair.store.Orch().AddCronRoute(orch.CronRoute{
		ID:                "route_inbox",
		AgentTarget:       "valet",
		Action:            "inbox",
		Text:              "event",
		OpenCodeSessionID: "ses_x",
		SourceMessageID:   "msg_x",
		CreatedAt:         now,
	}); err != nil {
		t.Fatalf("AddCronRoute() error: %v", err)
	}

	svc.retryRoutes(context.Background(), pair)

	routes, err := pair.store.Orch().ListCronRoutes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	if routes[0].DeliveredAt != nil {
		t.Error("in-flight route was marked delivered")
	}
	if routes[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", routes[0].Attempts)
	}
}

func TestRecoverPendingTurnsReportsRecoveredOutput(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})

	// Mark the run pending before the service takes the store handle.
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	// Admitted an hour ago and never collected.
	if err := st.Orch().SetCronJobPending(job.ID, "ses_a", "msg_a", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{delivered: true, text: "the recovered report"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)

	if len(turn.collected) != 1 || turn.collected[0] != "msg_a" {
		t.Fatalf("collected = %v, want the admitted marker", turn.collected)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared after collection", turns)
	}
	if !storeLogContains(t, pair.store, "recovered output from an interrupted run") {
		t.Errorf("logs = %v, want the recovery note", storeLogMessages(t, pair.store))
	}
	if turn.calls != 0 {
		t.Errorf("synthetic turns = %d, want 0 (recovery must never re-run a job)", turn.calls)
	}
}

// liveLockingTurn simulates a real turn that holds the session lock through
// admission and collection, then consumes the marker before releasing it.
type liveLockingTurn struct {
	mu            sync.Mutex
	locked        chan struct{}
	release       chan struct{}
	recoveryCalls int
}

func (t *liveLockingTurn) RunSyntheticTurn(ctx context.Context, _ string, _ opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	held, err := lock.AcquireSession(ctx, "ses_live")
	if err != nil {
		return nil, err
	}
	defer held.Release()

	result := &opencode.TurnResult{SessionID: "ses_live", InputID: "msg_live", ResponseText: "once"}
	if cfg.OnAdmitted != nil {
		if err := cfg.OnAdmitted(opencode.Admitted{SessionID: result.SessionID, InputID: result.InputID}); err != nil {
			return nil, err
		}
	}
	close(t.locked)
	<-t.release
	if cfg.OnCollected != nil {
		if err := cfg.OnCollected(result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (t *liveLockingTurn) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	return &opencode.SessionInfo{ID: "ses_live"}, nil
}

func (t *liveLockingTurn) CollectDelivered(_ context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error) {
	t.mu.Lock()
	t.recoveryCalls++
	t.mu.Unlock()
	return &opencode.TurnResult{SessionID: sessionID, InputID: markerID, ResponseText: "once"}, true, nil
}

func (t *liveLockingTurn) InboxPending(context.Context, string, string) (bool, error) {
	return false, nil
}

func (t *liveLockingTurn) recoveryCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recoveryCalls
}

// TestLiveCollectionConsumesMarkerBeforeSessionLockRelease ensures recovery
// cannot collect a live turn's output in the gap before the marker is cleared.
func TestLiveCollectionConsumesMarkerBeforeSessionLockRelease(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	turn := &liveLockingTurn{locked: make(chan struct{}), release: make(chan struct{})}
	sink := &countingSink{}
	svc := startTestService(t, reg, routine.Default(), sink, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	executeDone := make(chan struct{})
	go func() {
		svc.execute(context.Background(), pair, job)
		close(executeDone)
	}()
	<-turn.locked

	// Recovery sees the marker while the live turn holds the lock, then waits.
	recoverDone := make(chan struct{})
	go func() {
		svc.recoverPendingTurns(context.Background(), pair)
		close(recoverDone)
	}()
	time.Sleep(50 * time.Millisecond)
	close(turn.release)
	<-executeDone
	<-recoverDone

	if got := turn.recoveryCount(); got != 0 {
		t.Errorf("recovery collected %d time(s), want 0 after the live turn consumed its marker", got)
	}
	if got := sink.count(); got != 1 {
		t.Errorf("notifications = %d, want exactly one", got)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared by the live turn", turns)
	}
}

// staleCollector simulates a new run replacing the pending generation while a
// recovery pass is probing the old one.
type staleCollector struct {
	orch    *orch.Store
	jobID   string
	updated bool
}

func (c *staleCollector) RunSyntheticTurn(context.Context, string, opencode.SyntheticEvent, opencode.TurnConfig) (*opencode.TurnResult, error) {
	return nil, errors.New("staleCollector does not run turns")
}

func (c *staleCollector) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	return &opencode.SessionInfo{ID: "ses_old"}, nil
}

func (c *staleCollector) CollectDelivered(context.Context, string, string) (*opencode.TurnResult, bool, error) {
	if !c.updated {
		c.updated = true
		if err := c.orch.SetCronJobPending(c.jobID, "ses_new", "msg_new", "turn", time.Now().UTC()); err != nil {
			return nil, false, err
		}
	}
	return nil, false, nil
}

func (c *staleCollector) InboxPending(context.Context, string, string) (bool, error) {
	return false, nil
}

// TestRecoverPendingTurnsDoesNotClearNewerRun ensures a stale snapshot cannot
// clear a generation that a later run installed while recovery was probing.
func TestRecoverPendingTurnsDoesNotClearNewerRun(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_old", "msg_old", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, &fakeTurn{}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	collector := &staleCollector{orch: pair.store.Orch(), jobID: job.ID}
	svc.turn = collector

	svc.recoverPendingTurns(context.Background(), pair)

	pending, ok, err := pair.store.Orch().CronJobPending(job.ID)
	if err != nil {
		t.Fatalf("CronJobPending() error: %v", err)
	}
	if !ok || pending.MarkerID != "msg_new" || pending.SessionID != "ses_new" {
		t.Errorf("pending = %+v, ok = %v; want the newer generation intact", pending, ok)
	}
	if storeLogContains(t, pair.store, "the turn produced no output") {
		t.Errorf("logs = %v, stale pass settled a newer run", storeLogMessages(t, pair.store))
	}
}

// newerRunTurn installs a newer pending generation after its own collection,
// simulating another process starting the next run.
type newerRunTurn struct {
	orch  *orch.Store
	jobID string
}

func (t *newerRunTurn) RunSyntheticTurn(_ context.Context, _ string, _ opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error) {
	result := &opencode.TurnResult{SessionID: "ses_old", InputID: "msg_old", ResponseText: "old answer"}
	if cfg.OnAdmitted != nil {
		if err := cfg.OnAdmitted(opencode.Admitted{SessionID: result.SessionID, InputID: result.InputID}); err != nil {
			return nil, err
		}
	}
	if cfg.OnCollected != nil {
		if err := cfg.OnCollected(result); err != nil {
			return result, err
		}
	}
	if err := t.orch.SetCronJobPending(t.jobID, "ses_new", "msg_new", "turn", time.Now().UTC()); err != nil {
		return nil, err
	}
	return result, nil
}

// TestExecuteDoesNotClearNewerRun ensures a completed execution never erases a
// pending generation installed by the next run.
func TestExecuteDoesNotClearNewerRun(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, &fakeTurn{}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	svc.turn = &newerRunTurn{orch: pair.store.Orch(), jobID: job.ID}

	svc.execute(context.Background(), pair, job)

	pending, ok, err := pair.store.Orch().CronJobPending(job.ID)
	if err != nil {
		t.Fatalf("CronJobPending() error: %v", err)
	}
	if !ok || pending.MarkerID != "msg_new" || pending.SessionID != "ses_new" {
		t.Errorf("pending = %+v, ok = %v; want the newer generation intact", pending, ok)
	}
}

// TestExecuteRoutesOutputAfterPostCollectionCleanupError ensures collected text
// is delivered even when the turn runner reports a cleanup error afterwards.
func TestExecuteRoutesOutputAfterPostCollectionCleanupError(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	turn := &fakeTurn{response: "the answer", postCollectErr: errors.New("lock release failed")}
	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	if len(sink.notified) != 1 || sink.notified[0] != "the answer" {
		t.Errorf("notified = %v, want the collected answer", sink.notified)
	}
	if turns, err := pair.store.Orch().PendingCronTurns(now, 10); err != nil || len(turns) != 0 {
		t.Errorf("pending turns = %+v, %v; want cleared", turns, err)
	}
	if !storeLogContains(t, pair.store, "post-collection delivery failed") {
		t.Errorf("logs = %v, want the delivery warning", storeLogMessages(t, pair.store))
	}
}

// replacingCollector installs a newer pending generation during a successful
// collection, simulating another process starting the next run.
type replacingCollector struct {
	orch    *orch.Store
	jobID   string
	updated bool
}

func (c *replacingCollector) RunSyntheticTurn(context.Context, string, opencode.SyntheticEvent, opencode.TurnConfig) (*opencode.TurnResult, error) {
	return nil, errors.New("replacingCollector does not run turns")
}

func (c *replacingCollector) GetSession(context.Context, string) (*opencode.SessionInfo, error) {
	return &opencode.SessionInfo{ID: "ses_old"}, nil
}

func (c *replacingCollector) CollectDelivered(_ context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error) {
	if !c.updated {
		c.updated = true
		if err := c.orch.SetCronJobPending(c.jobID, "ses_new", "msg_new", "turn", time.Now().UTC()); err != nil {
			return nil, false, err
		}
	}
	return &opencode.TurnResult{SessionID: sessionID, InputID: markerID, ResponseText: "old"}, true, nil
}

func (c *replacingCollector) InboxPending(context.Context, string, string) (bool, error) {
	return false, nil
}

// TestRecoverPendingTurnsDoesNotRouteAfterNewerRun ensures a recovery pass that
// lost ownership during collection neither routes nor marks the newer run.
func TestRecoverPendingTurnsDoesNotRouteAfterNewerRun(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_old", "msg_old", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	sink := &countingSink{}
	svc := startTestService(t, reg, routine.Default(), sink, &fakeTurn{}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	svc.turn = &replacingCollector{orch: pair.store.Orch(), jobID: job.ID}

	svc.recoverPendingTurns(context.Background(), pair)

	if got := sink.count(); got != 0 {
		t.Errorf("notifications = %d, want 0 after losing ownership", got)
	}
	pending, ok, err := pair.store.Orch().CronJobPending(job.ID)
	if err != nil {
		t.Fatalf("CronJobPending() error: %v", err)
	}
	if !ok || pending.MarkerID != "msg_new" {
		t.Errorf("pending = %+v, ok = %v; want the newer generation intact", pending, ok)
	}
	if storeLogContains(t, pair.store, "recovered output from an interrupted run") {
		t.Errorf("logs = %v, stale pass routed old output", storeLogMessages(t, pair.store))
	}
}

// TestPromptOutputIsDurableBeforeMarkerClear ensures the outbox row exists with
// a deterministic ID before recovery state is consumed, and that recovery
// re-uses that ID instead of delivering twice.
func TestPromptOutputIsDurableBeforeMarkerClear(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, &fakeTurn{response: "durable answer"}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	outbox, err := pair.store.CommTG().ListOutbox(10)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	wantID := cronOutputID(job.ID, "msg_fake")
	if len(outbox) != 1 || outbox[0].EnvelopeID != wantID || outbox[0].Text != "durable answer" {
		t.Fatalf("outbox = %+v, want one row %q", outbox, wantID)
	}
	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want direct outbox delivery only", sink.notified)
	}

	// Simulate a crash before the marker clear: recovery must re-use the ID.
	if err := pair.store.Orch().SetCronJobPending(job.ID, "ses_fake", "msg_fake", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	svc.turn = &collectingTurn{delivered: true, text: "durable answer"}
	svc.recoverPendingTurns(context.Background(), pair)
	outbox, err = pair.store.CommTG().ListOutbox(10)
	if err != nil {
		t.Fatalf("ListOutbox() error: %v", err)
	}
	if len(outbox) != 1 || outbox[0].EnvelopeID != wantID {
		t.Errorf("outbox after recovery = %+v, want the original row only", outbox)
	}
	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want no duplicate route", sink.notified)
	}
}

// TestPromptInboxRouteKeepsMarkerUntilRouted ensures non-notify policies use
// the pending marker as their durable intent until routing succeeds.
func TestPromptInboxRouteKeepsMarkerUntilRouted(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: `{"on_success":"inbox"}`,
	})
	turn := &fakeTurn{response: "the answer"}
	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	if err := pair.store.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	if len(turn.events) != 2 || turn.events[1].Text != "the answer" {
		t.Errorf("events = %+v, want the prompt and the inbox event", turn.events)
	}
	routes, err := pair.store.Orch().ListCronRoutes()
	if err != nil || len(routes) != 1 || routes[0].DeliveredAt == nil || routes[0].Action != "inbox" {
		t.Errorf("routes = %+v, %v; want one delivered inbox route", routes, err)
	}
	if turns, err := pair.store.Orch().PendingCronTurns(now, 10); err != nil || len(turns) != 0 {
		t.Errorf("pending turns = %+v, %v; want cleared after routing", turns, err)
	}
}

// TestPromptWithoutChatKeepsDurableRouteIntent ensures the output survives as
// a durable route intent even when no notification chat exists yet.
func TestPromptWithoutChatKeepsDurableRouteIntent(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	sink := &fakeSink{notifyErr: errors.New("no private chat")}
	svc := startTestService(t, reg, routine.Default(), sink, &fakeTurn{response: "the answer"}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	if _, ok, err := pair.store.Orch().CronJobPending(job.ID); err != nil || ok {
		t.Errorf("pending marker = ok %v, err %v; want it consumed after the route intent", ok, err)
	}
	routes, err := pair.store.Orch().ListCronRoutes()
	if err != nil || len(routes) != 1 || routes[0].Action != "notify-user" || routes[0].Text != "the answer" || routes[0].DeliveredAt != nil {
		t.Errorf("routes = %+v, %v; want one pending notify-user route intent", routes, err)
	}
}

// TestExecuteRoutesCollectedOutputWhenSessionMappingFails ensures a collected
// turn is still delivered when only persisting the thread mapping fails.
func TestExecuteRoutesCollectedOutputWhenSessionMappingFails(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	turn := &fakeTurn{response: "the answer", emptySession: true}
	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	if len(sink.notified) != 1 || sink.notified[0] != "the answer" {
		t.Errorf("notified = %v, want the collected answer", sink.notified)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared after collection", turns)
	}
	if !storeLogContains(t, pair.store, "session mapping could not be saved") {
		t.Errorf("logs = %v, want the mapping warning", storeLogMessages(t, pair.store))
	}
}

// TestAdmittedPendingRetriesTransientWrite ensures a transient store failure
// during admission does not discard the marker and the output remains
// recoverable.
func TestAdmittedPendingRetriesTransientWrite(t *testing.T) {
	oldSet := setCronPending
	oldRetries := cronPendingRetries
	oldDelay := cronPendingRetryDelay
	oldGrace := cronResumeGrace
	t.Cleanup(func() {
		setCronPending = oldSet
		cronPendingRetries = oldRetries
		cronPendingRetryDelay = oldDelay
		cronResumeGrace = oldGrace
	})
	failures := 1
	setCronPending = func(st *store.Store, jobID, sessionID, markerID, kind string, at time.Time) error {
		if failures > 0 {
			failures--
			return errors.New("database busy")
		}
		return st.Orch().SetCronJobPending(jobID, sessionID, markerID, kind, at)
	}
	cronPendingRetries = 3
	cronPendingRetryDelay = time.Millisecond
	cronResumeGrace = 0

	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, &fakeTurn{response: "partial", err: errors.New("collection failed")}, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)
	pending, ok, err := pair.store.Orch().CronJobPending(job.ID)
	if err != nil || !ok || pending.MarkerID != "msg_fake" {
		t.Fatalf("pending = %+v, ok = %v, err = %v; want the admitted marker", pending, ok, err)
	}

	svc.turn = &collectingTurn{delivered: true, text: "recovered"}
	svc.recoverPendingTurns(context.Background(), pair)
	svc.retryRoutes(context.Background(), pair)
	if len(sink.notified) != 1 || sink.notified[0] != "recovered" {
		t.Errorf("notified = %v, want the recovered output", sink.notified)
	}
	if turns, err := pair.store.Orch().PendingCronTurns(now, 10); err != nil || len(turns) != 0 {
		t.Errorf("pending turns = %+v, %v; want cleared", turns, err)
	}
}

// TestRecoverPendingTurnsSettlesIndeterminateRun covers an admitted turn whose
// marker could not be persisted: recovery settles it instead of probing.
func TestRecoverPendingTurnsSettlesIndeterminateRun(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "", "run_indeterminate", "indeterminate", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{delivered: true, text: "ignored"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)

	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want settled", turns)
	}
	if len(turn.collected) != 0 {
		t.Errorf("collected = %v, want no probe for an indeterminate run", turn.collected)
	}
	if !storeLogContains(t, pair.store, "could not be tracked reliably") {
		t.Errorf("logs = %v, want the indeterminate notice", storeLogMessages(t, pair.store))
	}
}

// TestRecoverPendingTurnsSettlesUnfinishedRoutine covers a routine that
// consumed its last turn but stopped before the body finished: recovery settles
// it as incomplete and never replays the body.
func TestRecoverPendingTurnsSettlesUnfinishedRoutine(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "dream", Kind: "routine", Spec: "@every 72h", Routine: "dream",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "", "run_routine", "routine-run", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{delivered: true, text: "ignored"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)

	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want settled", turns)
	}
	if len(turn.collected) != 0 {
		t.Errorf("collected = %v, want no replay of the routine body", turn.collected)
	}
	if !storeLogContains(t, pair.store, "the routine body did not finish") {
		t.Errorf("logs = %v, want the incomplete-routine note", storeLogMessages(t, pair.store))
	}
}

// TestRecoverPendingTurnsConcurrentPassesRouteOnce ensures the session lock plus
// marker revalidation makes recovery idempotent under concurrent passes.
func TestRecoverPendingTurnsConcurrentPassesRouteOnce(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_once", "msg_once", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	collector := &countingCollector{}
	sink := &countingSink{}
	svc := startTestService(t, reg, routine.Default(), sink, collector, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.recoverPendingTurns(context.Background(), pair)
		}()
	}
	wg.Wait()
	svc.retryRoutes(context.Background(), pair)

	if got := collector.count(); got != 1 {
		t.Errorf("collect calls = %d, want 1", got)
	}
	if got := sink.count(); got != 1 {
		t.Errorf("notifications = %d, want 1", got)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared once", turns)
	}
}

func TestRecoverPendingTurnsReportsInterruptedRoutineHonestly(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "dream", Kind: "routine", Spec: "@every 72h", Routine: "dream",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_d", "msg_d", "routine", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{delivered: true, text: "analysis text"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)

	// The routine body cannot be resumed, so the log must say so rather than
	// presenting the recovered text as a completed run.
	if !storeLogContains(t, pair.store, "the routine body did not finish") {
		t.Errorf("logs = %v, want an honest incomplete-run note", storeLogMessages(t, pair.store))
	}
	if turn.calls != 0 {
		t.Errorf("synthetic turns = %d, want 0 (a routine must never be replayed)", turn.calls)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared", turns)
	}
}

func TestRecoverPendingTurnsLeavesInFlightRunAlone(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_a", "msg_a", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{delivered: true, text: "too early"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	// Simulate a live run owning the job.
	svc.running["valet/"+job.ID] = true

	svc.recoverPendingTurns(context.Background(), pair)

	if len(turn.collected) != 0 {
		t.Errorf("collected = %v, want none while a run is in flight", turn.collected)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("pending turns = %+v, want the row untouched", turns)
	}
}

// TestExecutePreservesPendingMarkerAfterTurnError is the cron half of the
// recovery invariant: once a turn was admitted, a collection error must leave
// the marker for the next recovery pass instead of clearing it.
func TestExecutePreservesPendingMarkerAfterTurnError(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	turn := &fakeTurn{response: "partial", err: errors.New("collection failed")}
	sink := &fakeSink{}
	svc := startTestService(t, reg, routine.Default(), sink, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	turns, err := pair.store.Orch().PendingCronTurns(now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("pending turns = %+v, want the admitted marker preserved", turns)
	}
	if turns[0].JobID != job.ID || turns[0].MarkerID != "msg_fake" || turns[0].SessionID != "ses_fake" {
		t.Errorf("pending turn = %+v, want job %q with the admitted marker", turns[0], job.ID)
	}
	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want no terminal failure while recovery owns the output", sink.notified)
	}
	if !storeLogContains(t, pair.store, "recovery will collect its output") {
		t.Errorf("logs = %v, want the transient-error note", storeLogMessages(t, pair.store))
	}
}

// TestExecuteClearsPendingMarkerAfterSuccessfulTurn guards the other side: a
// collected turn must not be recovered and delivered twice.
func TestExecuteClearsPendingMarkerAfterSuccessfulTurn(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "summarize",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	turn := &fakeTurn{response: "done"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")
	job, err := pair.store.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}

	svc.execute(context.Background(), pair, job)

	turns, err := pair.store.Orch().PendingCronTurns(now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("pending turns = %+v, want cleared after collection", turns)
	}
}

// TestRecoverPendingTurnsLeavesBusySessionAlone ensures recovery checks the
// session, not just this process's running map: a busy service still owns the
// turn and its output is not final yet.
func TestRecoverPendingTurnsLeavesBusySessionAlone(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_busy", "msg_busy", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	turn := &collectingTurn{
		delivered: true,
		text:      "partial",
		session:   &opencode.SessionInfo{ID: "ses_busy", Time: opencode.SessionTime{Busy: 2, Idle: 1}},
	}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)

	if len(turn.collected) != 0 {
		t.Errorf("collected = %v, want none while the session is busy", turn.collected)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("pending turns = %+v, want the row untouched", turns)
	}
}

// TestRecoverPendingTurnsSettlesDefinitiveErrors covers probes that can never
// heal: a deleted session or truncated output clears the marker and records an
// incomplete run, while a transient error leaves the marker for the next pass.
func TestRecoverPendingTurnsSettlesDefinitiveErrors(t *testing.T) {
	cases := []struct {
		name        string
		turn        *collectingTurn
		wantPending int
		wantLog     string
	}{
		{
			name:        "missing session",
			turn:        &collectingTurn{sessionErr: &opencode.APIError{Status: 404}},
			wantPending: 0,
			wantLog:     "the session is no longer available",
		},
		{
			name:        "truncated output",
			turn:        &collectingTurn{collectErr: opencode.ErrTurnTruncated},
			wantPending: 0,
			wantLog:     "exceeded the collection window",
		},
		{
			name:        "empty delivered output",
			turn:        &collectingTurn{delivered: true, text: "   "},
			wantPending: 0,
			wantLog:     "the turn produced no output",
		},
		{
			name:        "queued marker",
			turn:        &collectingTurn{delivered: false, inboxPending: true},
			wantPending: 1,
		},
		{
			name:        "consumed with no output",
			turn:        &collectingTurn{delivered: false, inboxPending: false},
			wantPending: 0,
			wantLog:     "the turn produced no output",
		},
		{
			name:        "transient service error",
			turn:        &collectingTurn{sessionErr: &opencode.APIError{Status: 503}},
			wantPending: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, ws, now := setupCron(t)
			addJob(t, ws, orch.CronJob{
				Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
				Enabled: true, CreatedAt: now, Meta: "{}",
			})

			st, err := store.Open(ws)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			job, err := st.Orch().GetCronJobByTitle("report")
			if err != nil {
				t.Fatalf("GetCronJobByTitle() error: %v", err)
			}
			if err := st.Orch().SetCronJobPending(job.ID, "ses_a", "msg_a", "turn", now.Add(-time.Hour)); err != nil {
				t.Fatalf("SetCronJobPending() error: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			svc := startTestService(t, reg, routine.Default(), &fakeSink{}, tc.turn, "", now)
			defer svc.Stop()
			pair := servicePair(t, svc, "valet")

			svc.recoverPendingTurns(context.Background(), pair)

			turns, err := pair.store.Orch().PendingCronTurns(now, 10)
			if err != nil {
				t.Fatalf("PendingCronTurns() error: %v", err)
			}
			if len(turns) != tc.wantPending {
				t.Errorf("pending turns = %+v, want %d", turns, tc.wantPending)
			}
			if tc.wantLog != "" && !storeLogContains(t, pair.store, tc.wantLog) {
				t.Errorf("logs = %v, want %q", storeLogMessages(t, pair.store), tc.wantLog)
			}
			if tc.wantPending == 0 {
				updated, err := pair.store.Orch().GetCronJobByID(job.ID)
				if err != nil {
					t.Fatalf("GetCronJobByID() error: %v", err)
				}
				if updated.LastRunAt == nil {
					t.Error("settled run was not recorded as attempted")
				}
			}
		})
	}
}

// TestRecoverPendingTurnsWaitsForSessionLock ensures recovery never collects
// while another producer owns the session, and retries after the lock frees.
func TestRecoverPendingTurnsWaitsForSessionLock(t *testing.T) {
	reg, ws, now := setupCron(t)
	addJob(t, ws, orch.CronJob{
		Title: "report", Kind: "prompt", Spec: "@every 1h", Prompt: "x",
		Enabled: true, CreatedAt: now, Meta: "{}",
	})
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("report")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if err := st.Orch().SetCronJobPending(job.ID, "ses_locked", "msg_locked", "turn", now.Add(-time.Hour)); err != nil {
		t.Fatalf("SetCronJobPending() error: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	held, err := lock.AcquireSession(context.Background(), "ses_locked")
	if err != nil {
		t.Fatalf("AcquireSession() error: %v", err)
	}
	oldWait := cronSessionLockWait
	cronSessionLockWait = 25 * time.Millisecond
	t.Cleanup(func() { cronSessionLockWait = oldWait })

	turn := &collectingTurn{delivered: true, text: "collected"}
	svc := startTestService(t, reg, routine.Default(), &fakeSink{}, turn, "", now)
	defer svc.Stop()
	pair := servicePair(t, svc, "valet")

	svc.recoverPendingTurns(context.Background(), pair)
	if len(turn.collected) != 0 {
		t.Fatalf("collected = %v, want none while the session lock is held", turn.collected)
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release() error: %v", err)
	}
	svc.recoverPendingTurns(context.Background(), pair)
	if len(turn.collected) != 1 || turn.collected[0] != "msg_locked" {
		t.Errorf("collected = %v, want the marker after the lock freed", turn.collected)
	}
	turns, err := pair.store.Orch().PendingCronTurns(now, 10)
	if err != nil {
		t.Fatalf("PendingCronTurns() error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("pending turns = %+v, want cleared after collection", turns)
	}
}

// servicePair returns the service's own handle for an agent, so tests never open
// a second connection to the same database.
func servicePair(t *testing.T, svc *Service, name string) *agentStore {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	pair, ok := svc.agents[name]
	if !ok {
		t.Fatalf("service has no store for agent %q", name)
	}
	return pair
}

func storeLogMessages(t *testing.T, st *store.Store) []string {
	t.Helper()
	logs, err := st.Orch().ListLogs(0)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	messages := make([]string, 0, len(logs))
	for _, entry := range logs {
		messages = append(messages, entry.Message)
	}
	return messages
}

func storeLogContains(t *testing.T, st *store.Store, want string) bool {
	t.Helper()
	for _, message := range storeLogMessages(t, st) {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

func TestParseJobMetaValidation(t *testing.T) {
	if _, err := parseJobMeta(`{`); err == nil {
		t.Fatal("parseJobMeta(invalid) error = nil, want error")
	}
	meta, err := parseJobMeta(`{"timeout_seconds":9223372036854775807}`)
	if err != nil {
		t.Fatalf("parseJobMeta(large timeout) error: %v", err)
	}
	if meta.TimeoutSeconds != int(MaxShellTimeout/time.Second) {
		t.Errorf("timeout = %d, want cap %d", meta.TimeoutSeconds, int(MaxShellTimeout/time.Second))
	}
}

func TestInvalidMetaDoesNotRunJob(t *testing.T) {
	reg, ws, now := setupCron(t)
	marker := filepath.Join(t.TempDir(), "marker")
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran > "+marker+"\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "bad-meta", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past, Meta: `{`,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("job ran despite malformed metadata")
	}
}

func TestCronDeliveryErrorPersistsRouteAndRetries(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho output\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "route", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})
	sink := &fakeSink{notifyErr: &comm.DeliveryError{EnvelopeID: "env_route", ChatID: 10, Text: "output"}}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	routes, err := st.Orch().DueCronRoutes(now, 10)
	if err != nil || len(routes) != 1 {
		st.Close()
		t.Fatalf("routes = %+v, %v; want one retry intent", routes, err)
	}
	st.Close()

	// Retry the route with a now-working sink.
	sink.notifyErr = nil
	service = startTestService(t, reg, routine.NewRegistry(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err = store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || !strings.Contains(outbox[0].Text, "output") {
		t.Errorf("outbox = %+v, %v; want output", outbox, err)
	}
	if len(outbox) == 1 && outbox[0].EnvelopeID != "env_route" {
		t.Errorf("outbox envelope = %q, want env_route", outbox[0].EnvelopeID)
	}
	if routes, _ := st.Orch().DueCronRoutes(now.Add(time.Hour), 10); len(routes) != 0 {
		t.Errorf("routes after retry = %+v, want none", routes)
	}
}

func TestRouteFallbackFile(t *testing.T) {
	workspace := t.TempDir()
	route := orch.CronRoute{
		ID: "env_fallback", AgentTarget: "valet", ChatID: 10, Text: "output",
		OpenCodeSessionID: "ses_x", SourceMessageID: "msg_x", CreatedAt: time.Now().UTC(),
	}
	if err := writeRouteFallback(workspace, route); err != nil {
		t.Fatalf("writeRouteFallback() error: %v", err)
	}
	routes, err := readRouteFallbacks(workspace)
	if err != nil || len(routes) != 1 || routes[0].ID != route.ID || routes[0].Text != "output" {
		t.Fatalf("readRouteFallbacks() = %+v, %v", routes, err)
	}
	if err := removeRouteFallback(workspace, route.ID); err != nil {
		t.Fatalf("removeRouteFallback() error: %v", err)
	}
	if routes, err := readRouteFallbacks(workspace); err != nil || len(routes) != 0 {
		t.Errorf("readRouteFallbacks(after remove) = %+v, %v", routes, err)
	}
}

func TestRouteFallbackRejectsUnsafeID(t *testing.T) {
	workspace := t.TempDir()
	if err := writeRouteFallback(workspace, orch.CronRoute{ID: "../evil", ChatID: 10, Text: "x"}); err == nil {
		t.Fatal("writeRouteFallback(unsafe id) error = nil, want error")
	}
	if err := removeRouteFallback(workspace, "../../outside"); err == nil {
		t.Fatal("removeRouteFallback(unsafe id) error = nil, want error")
	}
	dir := filepath.Join(workspace, routeFallbackDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir fallback dir: %v", err)
	}
	raw, _ := json.Marshal(orch.CronRoute{ID: "../evil", ChatID: 10, Text: "x"})
	if err := os.WriteFile(filepath.Join(dir, "evil.json"), raw, 0600); err != nil {
		t.Fatalf("write crafted fallback: %v", err)
	}
	if routes, err := readRouteFallbacks(workspace); err != nil || len(routes) != 0 {
		t.Errorf("readRouteFallbacks(crafted) = %+v, %v; want none", routes, err)
	}
}

func TestMemoryRouteFallback(t *testing.T) {
	service := &Service{}
	routeA := orch.CronRoute{ID: "env_a", AgentTarget: "a", ChatID: 10, Text: "a"}
	routeB := orch.CronRoute{ID: "env_b", AgentTarget: "b", ChatID: 20, Text: "b"}
	service.addMemoryRoute("/ws/a", routeA, orch.CronJob{ID: "job_a", Spec: "@every 1h"})
	service.addMemoryRoute("/ws/a", routeA, orch.CronJob{ID: "job_a", Spec: "@every 1h"})
	service.addMemoryRoute("/ws/b", routeB, orch.CronJob{ID: "job_b", Spec: "@every 1h"})
	routesA := service.memoryRoutesFor("/ws/a")
	if len(routesA) != 1 || routesA[0].route.ID != routeA.ID || routesA[0].jobID != "job_a" {
		t.Fatalf("memory routes A = %+v, want one with job_a", routesA)
	}
	routesB := service.memoryRoutesFor("/ws/b")
	if len(routesB) != 1 || routesB[0].route.ID != routeB.ID {
		t.Fatalf("memory routes B = %+v, want one", routesB)
	}
	service.removeMemoryRoute("/ws/a", routeA.ID)
	if remaining := service.memoryRoutesFor("/ws/a"); len(remaining) != 0 {
		t.Errorf("memory routes after remove = %+v, want empty", remaining)
	}
}

func TestPersistRouteMemoryFallback(t *testing.T) {
	ws := t.TempDir()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0600); err != nil {
		t.Fatalf("write not-a-dir: %v", err)
	}
	now := time.Now().UTC()
	service := &Service{now: func() time.Time { return now }}
	_, err = service.persistRoute(st, registry.Agent{Name: "valet", Workspace: notDir}, orch.CronJob{Title: "route"},
		&comm.DeliveryError{EnvelopeID: "env_mem", ChatID: 10, Text: "output"})
	if !errors.Is(err, errMemoryOnlyRoute) {
		t.Fatalf("persistRoute() error = %v, want errMemoryOnlyRoute", err)
	}
	if routes := service.memoryRoutesFor(notDir); len(routes) != 1 || routes[0].route.ID != "env_mem" {
		t.Errorf("memory routes = %+v, want env_mem", routes)
	}
}

func TestHasMemoryRouteForJob(t *testing.T) {
	service := &Service{}
	service.addMemoryRoute("/ws/a", orch.CronRoute{ID: "env", ChatID: 10, Text: "x"}, orch.CronJob{ID: "job_a", Spec: "@every 1h"})
	if !service.hasMemoryRouteForJob("/ws/a", "job_a") {
		t.Error("hasMemoryRouteForJob() = false, want true")
	}
	if service.hasMemoryRouteForJob("/ws/b", "job_a") {
		t.Error("hasMemoryRouteForJob(other workspace) = true, want false")
	}
	if service.hasMemoryRouteForJob("/ws/a", "job_b") {
		t.Error("hasMemoryRouteForJob(other job) = true, want false")
	}
}

type envRoutine struct {
	captured *routine.Env
}

func (envRoutine) Name() string        { return "capture-env" }
func (envRoutine) Owner() string       { return "test" }
func (envRoutine) Description() string { return "capture env" }
func (envRoutine) Args() []routine.ArgSpec {
	return nil
}
func (r envRoutine) Run(_ context.Context, env routine.Env, _ map[string]any) (string, error) {
	*r.captured = env
	return "ok", nil
}

func TestRoutineEnvPlumbing(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "capture", Kind: "routine", Spec: "@every 1h", Routine: "capture-env",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log","dry_run":true}`,
	})
	var captured routine.Env
	routines := routine.NewRegistry()
	if err := routines.Register(envRoutine{captured: &captured}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	service := startTestService(t, reg, routines, &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	job, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer job.Close()
	stored, err := job.Orch().GetCronJobByTitle("capture")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if captured.Thread != "cron:"+stored.ID {
		t.Errorf("thread = %q, want cron:%s", captured.Thread, stored.ID)
	}
	if captured.Meta["dry_run"] != true {
		t.Errorf("meta = %+v, want dry_run true", captured.Meta)
	}
	if captured.Now == nil {
		t.Error("Now is nil")
	}
	if captured.InboxSession == nil {
		t.Error("InboxSession is nil")
	}
}

func TestShellJobUsesConfiguredShell(t *testing.T) {
	reg, ws, now := setupCron(t)
	dir := t.TempDir()
	shellUsed := filepath.Join(dir, "shell-used")
	marker := filepath.Join(dir, "marker")
	wrapper := filepath.Join(dir, "shell.sh")
	jobScript := filepath.Join(dir, "job.sh")

	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s' \"$0\" > "+shellUsed+"\nexec /bin/sh \"$1\"\n"), 0755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if err := os.WriteFile(jobScript, []byte("#!/bin/sh\necho ran > "+marker+"\n"), 0644); err != nil {
		t.Fatalf("write job: %v", err)
	}

	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "shell", Kind: "shell", Spec: "@every 1h", ShellPath: jobScript,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, wrapper, now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "ran" {
		t.Errorf("marker = %q, want ran", raw)
	}
	raw, err = os.ReadFile(shellUsed)
	if err != nil {
		t.Fatalf("read shell marker: %v", err)
	}
	if string(raw) != wrapper {
		t.Errorf("shell used = %q, want %q", raw, wrapper)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("shell")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.LastRunAt == nil {
		t.Error("last_run_at was not set")
	}
	if job.NextRunAt == nil || !job.NextRunAt.After(now) {
		t.Errorf("next_run_at = %v, want future", job.NextRunAt)
	}
}

func TestShellOutputIsBounded(t *testing.T) {
	_, ws, _ := setupCron(t)
	script := filepath.Join(t.TempDir(), "noisy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' x\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	service := &Service{shell: "/bin/sh"}
	out, err := service.runShell(context.Background(), registry.Agent{Name: "valet", Workspace: ws}, orch.CronJob{
		Title: "noisy", ShellPath: script,
	}, jobMeta{TimeoutSeconds: 10})
	if err != nil {
		t.Fatalf("runShell() error: %v", err)
	}
	if len(out) > maxOutputBytes+len("\n[output truncated]") {
		t.Errorf("output length = %d, want bounded", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Error("output was not marked truncated")
	}
}

func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	_, ws, _ := setupCron(t)
	script := filepath.Join(t.TempDir(), "hang.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n(sleep 5; echo late) &\nsleep 5\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	service := &Service{shell: "/bin/sh"}
	start := time.Now()
	out, err := service.runShell(context.Background(), registry.Agent{Name: "valet", Workspace: ws}, orch.CronJob{
		Title: "hang", ShellPath: script,
	}, jobMeta{TimeoutSeconds: 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("runShell() error = nil, want timeout error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("runShell() took %s, process group was not killed", elapsed)
	}
	if strings.Contains(out, "late") {
		t.Errorf("output = %q, descendant survived the timeout", out)
	}
}

func TestShellInboxUsesJobSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "shell-inbox", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", SessionID: "ses_owned", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"inbox"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.inboxInputs) != 1 || sink.inboxInputs[0] != "ses_owned" {
		t.Errorf("inbox session inputs = %v, want [ses_owned]", sink.inboxInputs)
	}
}

func TestShellFailureRoutesOutput(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boom\nexit 3\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "fail", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_failure":"notify-user"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || !strings.Contains(sink.notified[0], "boom") {
		t.Errorf("notified = %v, want failure output", sink.notified)
	}
}

func TestRefreshRetiresStoreWithoutClosingRunningJob(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.25\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "slow", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	time.Sleep(20 * time.Millisecond)
	if _, err := reg.Delete("valet"); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	service.refresh()
	service.Wait()

	// The retired store must be closed as soon as its last job releases it.
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("retired store was not released: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	service.Stop()

	st, err = store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("slow")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.LastRunAt == nil {
		t.Error("running job lost its store before completion")
	}
}

func TestRefreshSwitchesWorkspaceForSameAgentName(t *testing.T) {
	reg, firstWS, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrue\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, firstWS, orch.CronJob{
		Title: "old", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	defer service.Stop()

	if _, err := reg.Delete("valet"); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	secondWS := filepath.Join(t.TempDir(), "agent-two")
	if err := os.MkdirAll(secondWS, 0755); err != nil {
		t.Fatalf("mkdir second: %v", err)
	}
	st, err := store.Open(secondWS)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	if err := st.Orch().AddCronJob(orch.CronJob{
		Title: "new", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	}); err != nil {
		t.Fatalf("add new job: %v", err)
	}
	st.Close()
	if err := reg.Register(registry.Agent{Name: "valet", Workspace: secondWS}); err != nil {
		t.Fatalf("register new workspace: %v", err)
	}

	service.refresh()
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	oldStore, err := store.Open(firstWS)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	oldJob, err := oldStore.Orch().GetCronJobByTitle("old")
	oldStore.Close()
	if err != nil {
		t.Fatalf("get old job: %v", err)
	}
	if oldJob.LastRunAt != nil {
		t.Error("old workspace job ran after the agent moved")
	}

	newStore, err := store.Open(secondWS)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer newStore.Close()
	newJob, err := newStore.Orch().GetCronJobByTitle("new")
	if err != nil {
		t.Fatalf("get new job: %v", err)
	}
	if newJob.LastRunAt == nil {
		t.Error("new workspace job did not run")
	}
}

func TestDispatchIsBoundedByConcurrency(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.2\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	for _, title := range []string{"one", "two"} {
		addJob(t, ws, orch.CronJob{
			Title: title, Kind: "shell", Spec: "@every 1h", ShellPath: script,
			AgentTarget: "valet", Enabled: true, NextRunAt: &past,
			Meta: `{"on_success":"log"}`,
		})
	}

	service, err := Start(context.Background(), Options{
		Registry:        reg,
		Routines:        routine.NewRegistry(),
		Sink:            &fakeSink{},
		Shell:           "/bin/sh",
		MaxConcurrency:  1,
		Interval:        time.Hour,
		RefreshInterval: time.Hour,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if dispatched := service.Tick(context.Background()); dispatched != 1 {
		t.Errorf("first tick dispatched = %d, want 1", dispatched)
	}
	service.Wait()
	if dispatched := service.Tick(context.Background()); dispatched != 1 {
		t.Errorf("second tick dispatched = %d, want 1", dispatched)
	}
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	ran := 0
	for _, job := range jobs {
		if job.LastRunAt != nil {
			ran++
		}
	}
	if ran != 2 {
		t.Errorf("ran jobs = %d, want 2 with drained batches", ran)
	}
}

func TestPromptJobRoutesOutputAndStoresSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "prompt", Kind: "prompt", Spec: "@every 1h", Prompt: "hello",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	sink := &fakeSink{}
	turn := &fakeTurn{response: "reply"}
	service := startTestService(t, reg, routine.NewRegistry(), sink, turn, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if turn.calls != 1 {
		t.Fatalf("turn calls = %d, want 1", turn.calls)
	}
	if len(turn.events) != 1 || turn.events[0].Text != "hello" {
		t.Errorf("events = %+v", turn.events)
	}
	if len(sink.notified) != 1 || sink.notified[0] != "reply" {
		t.Errorf("notified = %v, want reply", sink.notified)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("prompt")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:" + job.ID)
	if err != nil || !ok {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	if session.OpenCodeSessionID != "ses_fake" {
		t.Errorf("session id = %q, want ses_fake", session.OpenCodeSessionID)
	}
}

func TestPromptErrorPersistsCreatedSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "prompt", Kind: "prompt", Spec: "@every 1h", Prompt: "hello",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_failure":"log"}`,
	})

	turn := &fakeTurn{err: errors.New("admit failed")}
	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, turn, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("prompt")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:" + job.ID)
	if err != nil || !ok {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	if session.OpenCodeSessionID != "ses_fake" {
		t.Errorf("session id = %q, want ses_fake", session.OpenCodeSessionID)
	}
}

func TestInvalidRoutingFallsBackToNotifyUser(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "bad-route", Kind: "prompt", Spec: "@every 1h", Prompt: "hello",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify_user"}`,
	})

	sink := &fakeSink{}
	turn := &fakeTurn{response: "reply"}
	service := startTestService(t, reg, routine.NewRegistry(), sink, turn, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || sink.notified[0] != "reply" {
		t.Errorf("notified = %v, want fallback notify-user", sink.notified)
	}
}

func TestRoutineJobRoutesOutput(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "routine", Kind: "routine", Spec: "@every 1h", Routine: "fake",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(fakeRoutine{name: "fake", out: "routine-output"}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || sink.notified[0] != "routine-output" {
		t.Errorf("notified = %v, want routine-output", sink.notified)
	}
}

func TestRoutineDefaultsToLogRouting(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "routine", Kind: "routine", Spec: "@every 1h", Routine: "fake",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(fakeRoutine{name: "fake", out: "routine-output"}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want routine output kept internal", sink.notified)
	}
	if len(sink.inboxed) != 0 {
		t.Errorf("inboxed = %v, want routine output kept internal", sink.inboxed)
	}
	if messages := cronLogMessages(t, ws); !containsMessage(messages, "output: routine-output") {
		t.Errorf("logs = %v, want routine output log", messages)
	}
}

func TestRoutineFailureDefaultsToLogRouting(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "routine-fail", Kind: "routine", Spec: "@every 1h", Routine: "fail",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(failingRoutine{name: "fail", out: "routine-boom", err: errors.New("routine failed")}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want routine failure kept internal", sink.notified)
	}
	if messages := cronLogMessages(t, ws); !containsMessage(messages, "output: routine-boom") {
		t.Errorf("logs = %v, want routine failure output log", messages)
	}
}

func TestRoutineExplicitFailureRoutingStillNotifies(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "routine-fail", Kind: "routine", Spec: "@every 1h", Routine: "fail",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user","on_failure":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(failingRoutine{name: "fail", out: "routine-boom", err: errors.New("routine failed")}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || sink.notified[0] != "routine-boom" {
		t.Errorf("notified = %v, want explicit failure routing to notify-user", sink.notified)
	}
}

func TestRoutineIgnoresStoredSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "probe", Kind: "routine", Spec: "@every 1h", Routine: "session-probe",
		RoutineArgs: `{}`, AgentTarget: "valet", SessionID: "ses_user", Enabled: true, NextRunAt: &past,
	})

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("probe")
	if err != nil {
		st.Close()
		t.Fatalf("get job: %v", err)
	}
	if _, err := st.Orch().UpsertSession("cron:"+job.ID, "ses_thread", now); err != nil {
		st.Close()
		t.Fatalf("upsert session: %v", err)
	}
	st.Close()

	var captured routine.Env
	routines := routine.NewRegistry()
	if err := routines.Register(sessionProbeRoutine{captured: &captured}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if captured.Thread != "cron:"+job.ID {
		t.Errorf("thread = %q, want cron:%s", captured.Thread, job.ID)
	}
	if captured.SessionID != "ses_thread" {
		t.Errorf("session = %q, want the cron thread session", captured.SessionID)
	}
	if len(sink.inboxInputs) != 1 || sink.inboxInputs[0] != "ses_thread" {
		t.Errorf("synthetic turn sessions = %v, want [ses_thread]", sink.inboxInputs)
	}
}

func containsMessage(messages []string, want string) bool {
	for _, message := range messages {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

type inboxRoutine struct{}

func (inboxRoutine) Name() string        { return "inbox" }
func (inboxRoutine) Owner() string       { return "test" }
func (inboxRoutine) Description() string { return "inbox routine" }
func (inboxRoutine) Args() []routine.ArgSpec {
	return nil
}
func (inboxRoutine) Run(ctx context.Context, env routine.Env, _ map[string]any) (string, error) {
	_, _, err := env.Inbox(ctx, "ping", nil)
	return "", err
}

func TestRoutineInboxPersistsSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "inbox", Kind: "routine", Spec: "@every 1h", Routine: "inbox",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(inboxRoutine{}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	service := startTestService(t, reg, routines, &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("inbox")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:" + job.ID)
	if err != nil || !ok {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	if session.OpenCodeSessionID != "ses_inbox" {
		t.Errorf("session id = %q, want ses_inbox", session.OpenCodeSessionID)
	}
}

type doubleInboxRoutine struct{}

func (doubleInboxRoutine) Name() string        { return "double" }
func (doubleInboxRoutine) Owner() string       { return "test" }
func (doubleInboxRoutine) Description() string { return "double inbox routine" }
func (doubleInboxRoutine) Args() []routine.ArgSpec {
	return nil
}
func (doubleInboxRoutine) Run(ctx context.Context, env routine.Env, _ map[string]any) (string, error) {
	if _, _, err := env.Inbox(ctx, "one", nil); err != nil {
		return "", err
	}
	_, _, err := env.Inbox(ctx, "two", nil)
	return "", err
}

func TestRoutineInboxUpdatesSessionForNextCall(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "double", Kind: "routine", Spec: "@every 1h", Routine: "double",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(doubleInboxRoutine{}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.inboxInputs) != 2 || sink.inboxInputs[0] != "" || sink.inboxInputs[1] != "ses_inbox" {
		t.Errorf("inbox session inputs = %v, want ['', 'ses_inbox']", sink.inboxInputs)
	}
}

func TestRoutineInboxPersistsSessionOnSinkError(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "inbox", Kind: "routine", Spec: "@every 1h", Routine: "inbox",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(inboxRoutine{}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{inboxErr: errors.New("notification failed")}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("inbox")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:" + job.ID)
	if err != nil || !ok {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	if session.OpenCodeSessionID != "ses_inbox" {
		t.Errorf("session id = %q, want ses_inbox", session.OpenCodeSessionID)
	}
}

func TestInvalidSpecDisablesJob(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "bad", Kind: "shell", Spec: "not a cron", ShellPath: "/bin/true",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("bad")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Enabled {
		t.Error("job enabled = true, want disabled after invalid spec")
	}
}

func TestShellWithoutSHELLFails(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "shell", Kind: "shell", Spec: "@every 1h", ShellPath: "/bin/true",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	t.Setenv("SHELL", "")
	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	var message string
	if err := st.DB().QueryRow(`SELECT message FROM orch_logs ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(message, "$SHELL is not set") {
		t.Errorf("log message = %q, want $SHELL error", message)
	}
}

func TestShellEnvPrecedence(t *testing.T) {
	_, ws, _ := setupCron(t)
	if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("ACOLYTE_TEST_SHELL_ENV=workspace\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("ACOLYTE_TEST_SHELL_ENV", "process")
	script := filepath.Join(t.TempDir(), "env.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$ACOLYTE_TEST_SHELL_ENV\"\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	job := orch.CronJob{Title: "env", ShellPath: script}
	agent := registry.Agent{Name: "valet", Workspace: ws}

	// The process environment must beat the workspace .env file.
	plain := &Service{shell: "/bin/sh"}
	out, err := plain.runShell(context.Background(), agent, job, jobMeta{TimeoutSeconds: 10})
	if err != nil {
		t.Fatalf("runShell() error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "process" {
		t.Errorf("shell env = %q, want process (exported value must beat .env)", got)
	}

	// An explicit --env override must beat both.
	service := &Service{shell: "/bin/sh", env: map[string]string{"ACOLYTE_TEST_SHELL_ENV": "cli"}}
	out, err = service.runShell(context.Background(), agent, job, jobMeta{TimeoutSeconds: 10})
	if err != nil {
		t.Fatalf("runShell(override) error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "cli" {
		t.Errorf("shell env = %q, want cli override", got)
	}
}
