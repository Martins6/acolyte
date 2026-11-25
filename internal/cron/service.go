package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/lock"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
	"github.com/martins6/acolyte/internal/storepool"
	cronlib "github.com/robfig/cron/v3"
)

const (
	// DefaultInterval is how often due jobs are checked.
	DefaultInterval = 5 * time.Second
	// DefaultRefreshInterval is how often the registry/store cache is refreshed.
	DefaultRefreshInterval = 30 * time.Second
	// DefaultMaxConcurrency bounds simultaneous job executions.
	DefaultMaxConcurrency = 4
	// DefaultShellTimeout is used when a shell job does not set meta.timeout_seconds.
	DefaultShellTimeout = 10 * time.Minute
	maxOutputBytes      = 64 * 1024
	routeFallbackDir    = ".logs/cron-routes"
)

// TurnRunner is the OpenCode turn surface used by prompt/routine jobs.
type TurnRunner interface {
	RunSyntheticTurn(ctx context.Context, sessionID string, ev opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
}

// StoreProvider opens and retains per-agent stores. The store package allows
// one open handle per workspace per process, so cron and communication must
// share a provider.
type StoreProvider interface {
	Acquire(workspace string) (*store.Store, func(), error)
	Retain(workspace string) (func(), bool)
}

// Options configures the cron service.
type Options struct {
	Registry        *registry.Registry
	Routines        *routine.Registry
	Sink            Sink
	OpenCode        TurnRunner
	Stores          StoreProvider
	Interval        time.Duration
	RefreshInterval time.Duration
	MaxConcurrency  int
	Now             func() time.Time
	Shell           string
	// Env holds CLI --env KEY=VALUE overrides for shell jobs. They win over
	// the process environment and both .env files.
	Env map[string]string
}

type agentStore struct {
	agent   registry.Agent
	store   *store.Store
	release func()
}

// Service runs due jobs for all registered agents.
type Service struct {
	registry        *registry.Registry
	routines        *routine.Registry
	sink            Sink
	turn            TurnRunner
	shell           string
	env             map[string]string
	now             func() time.Time
	interval        time.Duration
	refreshInterval time.Duration
	maxConcurrency  int

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	provider StoreProvider
	ownPool  *storepool.Pool
	mu       sync.Mutex
	agents   map[string]*agentStore
	running  map[string]bool
	sem      chan struct{}
	wg       sync.WaitGroup

	routeMu      sync.Mutex
	memoryRoutes []memoryRoute
}

type memoryRoute struct {
	workspace string
	route     orch.CronRoute
	jobID     string
	spec      string
	timezone  string
}

// Start starts the cron service. It performs one store refresh before returning.
func Start(ctx context.Context, opts Options) (*Service, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("cron: registry is required")
	}
	if opts.Routines == nil {
		opts.Routines = routine.Default()
	}
	if opts.Sink == nil {
		return nil, fmt.Errorf("cron: sink is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = DefaultRefreshInterval
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = DefaultMaxConcurrency
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Shell == "" {
		opts.Shell = os.Getenv("SHELL")
	}
	var ownPool *storepool.Pool
	if opts.Stores == nil {
		ownPool = storepool.New()
		opts.Stores = ownPool
	}

	service := &Service{
		registry:        opts.Registry,
		routines:        opts.Routines,
		sink:            opts.Sink,
		turn:            opts.OpenCode,
		shell:           opts.Shell,
		env:             opts.Env,
		now:             opts.Now,
		interval:        opts.Interval,
		refreshInterval: opts.RefreshInterval,
		maxConcurrency:  opts.MaxConcurrency,
		done:            make(chan struct{}),
		provider:        opts.Stores,
		ownPool:         ownPool,
		agents:          map[string]*agentStore{},
		running:         map[string]bool{},
		sem:             make(chan struct{}, opts.MaxConcurrency),
	}
	service.ctx, service.cancel = context.WithCancel(ctx)
	service.refresh()

	go service.run()
	return service, nil
}

// Stop stops the tick loop and closes cached stores.
func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		log.Printf("cron: shutdown timed out waiting for running jobs")
	}
}

// Wait blocks until all dispatched jobs finish.
func (s *Service) Wait() {
	s.wg.Wait()
}

func (s *Service) run() {
	defer close(s.done)
	defer s.closeStores()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	refreshTicker := time.NewTicker(s.refreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.wg.Wait()
			return
		case <-refreshTicker.C:
			s.refresh()
		case <-ticker.C:
			s.Tick(s.ctx)
		}
	}
}

// Tick dispatches currently due jobs and returns how many were dispatched. It
// does not wait for execution.
func (s *Service) Tick(ctx context.Context) int {
	now := s.now()
	s.mu.Lock()
	candidates := make([]*agentStore, 0, len(s.agents))
	for _, st := range s.agents {
		candidates = append(candidates, st)
	}
	s.mu.Unlock()

	// Pin each store while it is read; a concurrent refresh may otherwise
	// release and close it between the snapshot and the query.
	pairs := make([]*agentStore, 0, len(candidates))
	releases := make([]func(), 0, len(candidates))
	for _, pair := range candidates {
		release, ok := s.provider.Retain(pair.agent.Workspace)
		if !ok {
			continue
		}
		pairs = append(pairs, pair)
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	dispatched := 0
	for _, pair := range pairs {
		s.retryRoutes(ctx, pair)
		s.recoverPendingTurns(ctx, pair)
		jobs, err := pair.store.Orch().DueCronJobs(now)
		if err != nil {
			log.Printf("cron: list due jobs for %s: %v", pair.agent.Name, err)
			continue
		}
		for _, job := range jobs {
			if s.dispatch(ctx, pair, job, now) {
				dispatched++
			}
		}
	}
	return dispatched
}

func (s *Service) refresh() {
	agents, err := s.registry.List()
	if err != nil {
		log.Printf("cron: list agents: %v", err)
		return
	}

	wanted := make(map[string]registry.Agent, len(agents))
	for _, agent := range agents {
		if agent.Enabled {
			wanted[agent.Name] = agent
		}
	}

	var stale []*agentStore
	s.mu.Lock()
	for name, st := range s.agents {
		wantedAgent, ok := wanted[name]
		if ok && wantedAgent.Workspace == st.agent.Workspace {
			// Keep the cached agent snapshot immutable; Tick reads it
			// without holding s.mu. A workspace change replaces the entry.
			continue
		}
		delete(s.agents, name)
		stale = append(stale, st)
	}
	s.mu.Unlock()
	for _, st := range stale {
		st.release()
	}

	for name, agent := range wanted {
		s.mu.Lock()
		_, exists := s.agents[name]
		s.mu.Unlock()
		if exists {
			continue
		}

		st, release, err := s.provider.Acquire(agent.Workspace)
		if err != nil {
			log.Printf("cron: open store for %s: %v", name, err)
			continue
		}
		s.mu.Lock()
		if _, exists := s.agents[name]; exists {
			s.mu.Unlock()
			release()
			continue
		}
		s.agents[name] = &agentStore{agent: agent, store: st, release: release}
		s.mu.Unlock()
	}
}

func (s *Service) closeStores() {
	s.mu.Lock()
	agents := s.agents
	s.agents = map[string]*agentStore{}
	s.mu.Unlock()
	for _, st := range agents {
		st.release()
	}
	if s.ownPool != nil {
		_ = s.ownPool.CloseAll()
	}
}

func (s *Service) dispatch(ctx context.Context, pair *agentStore, job orch.CronJob, now time.Time) bool {
	agent := pair.agent
	key := agent.Name + "/" + job.ID

	if s.hasMemoryRouteForJob(agent.Workspace, job.ID) {
		return false
	}

	// Bound dispatched work before claiming: a large due backlog must not
	// create an unbounded number of blocked goroutines.
	select {
	case s.sem <- struct{}{}:
	default:
		return false
	}
	semHeld := true

	s.mu.Lock()
	if s.running[key] {
		s.mu.Unlock()
		<-s.sem
		return false
	}
	s.running[key] = true
	s.mu.Unlock()

	releaseStore, ok := s.provider.Retain(agent.Workspace)
	if !ok {
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
		<-s.sem
		return false
	}

	release := func() {
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
		releaseStore()
		if semHeld {
			<-s.sem
			semHeld = false
		}
	}

	location := time.Local
	if job.Timezone != "" {
		loaded, err := time.LoadLocation(job.Timezone)
		if err != nil {
			log.Printf("cron: job %s has invalid timezone %q: %v", job.Title, job.Timezone, err)
			_ = pair.store.Orch().SetCronJobEnabled(job.Title, false)
			release()
			return false
		}
		location = loaded
	}

	schedule, err := cronlib.ParseStandard(job.Spec)
	if err != nil {
		log.Printf("cron: job %s has invalid spec %q: %v", job.Title, job.Spec, err)
		_ = pair.store.Orch().SetCronJobEnabled(job.Title, false)
		release()
		return false
	}
	next := schedule.Next(now.In(location)).UTC()

	claimed, err := pair.store.Orch().ClaimCronJob(job.ID, job.NextRunAt, next)
	if err != nil {
		log.Printf("cron: claim job %s: %v", job.Title, err)
		release()
		return false
	}
	if !claimed {
		release()
		return false
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("cron: job %s panicked: %v", job.Title, recovered)
			}
		}()
		s.execute(ctx, pair, job)
	}()
	return true
}

func (s *Service) execute(ctx context.Context, pair *agentStore, job orch.CronJob) {
	agent := pair.agent
	st := pair.store
	started := s.now()
	meta, metaErr := parseJobMeta(job.Meta)
	if metaErr != nil {
		_ = st.Orch().MarkCronJobRun(job.ID, s.now())
		s.logJob(st, job, "error", metaErr.Error())
		return
	}
	text, sessionID, admitted, routed, generation, err := s.runJob(ctx, agent, st, job, meta)
	// Turn runners consume their own marker under the session lock. Do not
	// clear here: a later run may already own a newer pending generation.

	if err != nil {
		if admitted {
			// The turn is admitted and its marker is pending. Recovery owns the
			// outcome; routing a terminal failure now would contradict the
			// output it may later recover.
			s.logJob(st, job, "warn", fmt.Sprintf("run did not finish; recovery will collect its output: %v", err))
			return
		}
		if strings.TrimSpace(text) == "" {
			text = fmt.Sprintf("cron job %q failed: %v", job.Title, err)
		}
		s.logJob(st, job, "error", fmt.Sprintf("failed after %s: %v", s.now().Sub(started).Round(time.Millisecond), err))
		if routeErr := s.route(ctx, st, agent, job, meta, text, sessionID, false); routeErr != nil {
			if _, persistErr := s.persistRoute(st, agent, job, routeErr); errors.Is(persistErr, errMemoryOnlyRoute) {
				s.requeueAfterPersistFailure(st, job)
				return
			}
		}
		_ = st.Orch().MarkCronJobRun(job.ID, s.now())
		return
	}

	s.logJob(st, job, "info", fmt.Sprintf("completed in %s", s.now().Sub(started).Round(time.Millisecond)))
	routedOK := routed
	if text != "" && !routed {
		if routeErr := s.route(ctx, st, agent, job, meta, text, sessionID, true); routeErr != nil {
			persisted, persistErr := s.persistRoute(st, agent, job, routeErr)
			if errors.Is(persistErr, errMemoryOnlyRoute) {
				s.requeueAfterPersistFailure(st, job)
				return
			}
			routedOK = persisted
		} else {
			routedOK = true
		}
	}
	if routedOK && generation != "" {
		if cleared, clearErr := st.Orch().ClearCronJobPendingIf(job.ID, generation); clearErr != nil {
			s.logJob(st, job, "warn", "completed run could not clear its pending generation: "+clearErr.Error())
		} else if !cleared {
			s.logJob(st, job, "warn", "completed run no longer owns its pending generation")
		}
	}
	_ = st.Orch().MarkCronJobRun(job.ID, s.now())
	if routed {
		// Deliver the just-persisted route intent now instead of waiting for
		// the next tick.
		s.retryRoutes(ctx, pair)
	}
}

// cronResumeGrace is how long an admitted cron turn may sit uncollected before
// recovery treats the run as interrupted. A variable so tests can shorten it.
var cronResumeGrace = 5 * time.Minute

// cronSessionLockWait bounds how long recovery waits for a live turn's session
// lock before leaving the marker for the next pass.
var cronSessionLockWait = 10 * time.Second

// routeClaimLease bounds one route delivery attempt so concurrent workers do
// not admit the same inbox intent twice.
var routeClaimLease = 2 * time.Minute

// routeClaimHeartbeat keeps a long delivery (for example a slow synthetic turn)
// from letting another worker claim the route.
var routeClaimHeartbeat = 30 * time.Second

// turnCollector is the optional recovery half of the cron turn runner.
type turnCollector interface {
	GetSession(ctx context.Context, sessionID string) (*opencode.SessionInfo, error)
	CollectDelivered(ctx context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error)
	InboxPending(ctx context.Context, sessionID, itemID string) (bool, error)
}

// recoverPendingTurns collects the output of runs that were admitted before a
// restart. It never re-runs a job: a routine body that did not finish cannot be
// resumed without repeating its side effects, so recovery reports exactly what
// it recovered and records the run as attempted.
func (s *Service) recoverPendingTurns(ctx context.Context, pair *agentStore) {
	collector, ok := s.turn.(turnCollector)
	if !ok || collector == nil {
		return
	}
	turns, err := pair.store.Orch().PendingCronTurns(s.now().Add(-cronResumeGrace), 20)
	if err != nil {
		log.Printf("cron: list pending turns for %s: %v", pair.agent.Name, err)
		return
	}
	for _, pending := range turns {
		if s.jobRunning(pair.agent.Name, pending.JobID) {
			// A live run owns this turn; leave it alone.
			continue
		}
		job, err := pair.store.Orch().GetCronJobByID(pending.JobID)
		if err != nil {
			log.Printf("cron: load pending job %s: %v", pending.JobID, err)
			_ = pair.store.Orch().ClearCronJobPending(pending.JobID)
			continue
		}
		if pending.Kind == "indeterminate" || pending.Kind == "routine-run" || strings.TrimSpace(pending.MarkerID) == "" {
			// Admission was confirmed but no collectable marker exists (or a
			// routine body stopped after consuming its last turn). There is
			// nothing to collect; settle the run honestly. Re-check first so a
			// concurrent pass cannot settle it twice.
			reason := "the run could not be tracked reliably"
			if pending.Kind == "routine-run" {
				reason = "the routine body did not finish"
			}
			current, ok, checkErr := pair.store.Orch().CronJobPending(job.ID)
			if checkErr == nil && ok && current.Kind == pending.Kind {
				s.settleInterruptedRun(pair.store, job, pending.MarkerID, reason)
			}
			continue
		}
		// Take the shared session lock around the state recheck and collection.
		// A router, OutboxSink, or another cron turn can otherwise start
		// between the in-flight check and CollectDelivered, making a busy turn
		// look completed and routing partial output.
		releaseLock, lockErr := acquireCronSessionLock(ctx, pending.SessionID)
		if lockErr != nil {
			log.Printf("cron: wait for session lock for %s: %v", job.Title, lockErr)
			continue
		}
		// A concurrent recovery pass may have consumed this marker while we
		// waited for the lock. Re-read the database state before probing.
		current, ok, checkErr := pair.store.Orch().CronJobPending(job.ID)
		if checkErr != nil {
			releaseLock()
			log.Printf("cron: recheck pending turn for %s: %v", job.Title, checkErr)
			continue
		}
		if !ok || current.MarkerID != pending.MarkerID || current.SessionID != pending.SessionID || current.Kind != pending.Kind {
			releaseLock()
			continue
		}
		info, err := collector.GetSession(ctx, pending.SessionID)
		if err != nil {
			if opencode.IsPermanentError(err) {
				s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the session is no longer available")
				releaseLock()
				continue
			}
			releaseLock()
			log.Printf("cron: inspect pending session for %s: %v", job.Title, err)
			continue
		}
		if info != nil && info.Time.InFlight() {
			releaseLock()
			continue
		}
		result, delivered, err := collector.CollectDelivered(ctx, pending.SessionID, pending.MarkerID)
		if err != nil {
			if errors.Is(err, opencode.ErrTurnTruncated) {
				s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the turn output exceeded the collection window")
				releaseLock()
				continue
			}
			if opencode.IsPermanentError(err) {
				s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the turn could no longer be collected")
				releaseLock()
				continue
			}
			releaseLock()
			log.Printf("cron: collect pending turn for %s: %v", job.Title, err)
			continue
		}
		if !delivered {
			// Not finding the marker in the timeline does not mean the input
			// was consumed: it may still be queued on an idle session and
			// execute later. Only settle once the inbox says it is gone.
			queued, probeErr := collector.InboxPending(ctx, pending.SessionID, pending.MarkerID)
			if probeErr != nil {
				if opencode.IsPermanentError(probeErr) {
					s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the turn could no longer be inspected")
					releaseLock()
					continue
				}
				releaseLock()
				log.Printf("cron: inspect pending input for %s: %v", job.Title, probeErr)
				continue
			}
			if queued {
				releaseLock()
				continue
			}
			s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the turn produced no output")
			releaseLock()
			continue
		}
		if result == nil || strings.TrimSpace(result.ResponseText) == "" {
			// The marker was delivered but the turn produced no text: there is
			// nothing to route, so settle it as incomplete instead of logging
			// a recovered-output note for empty content.
			s.settleInterruptedRun(pair.store, job, pending.MarkerID, "the turn produced no output")
			releaseLock()
			continue
		}
		// Revalidate ownership before routing. Collection happened under the
		// session lock, but a producer that does not use that lock may have
		// installed a newer generation.
		current, ok, checkErr = pair.store.Orch().CronJobPending(job.ID)
		if checkErr != nil {
			releaseLock()
			log.Printf("cron: recheck pending turn for %s: %v", job.Title, checkErr)
			continue
		}
		if !ok || current.MarkerID != pending.MarkerID {
			releaseLock()
			continue
		}
		if pending.Kind == "turn" {
			// Persist the route intent (notify-user, inbox, or log) before
			// consuming the marker. Recovery then only needs the durable
			// intent, and a crash cannot reroute the output.
			if err := s.addPromptRouteIntent(pair.store, pair.agent, job, pending.MarkerID, result); err != nil {
				releaseLock()
				log.Printf("cron: persist recovered route for %s: %v", job.Title, err)
				continue
			}
			cleared, clearErr := pair.store.Orch().ClearCronJobPendingIf(job.ID, pending.MarkerID)
			if clearErr != nil {
				releaseLock()
				log.Printf("cron: consume pending turn for %s: %v", job.Title, clearErr)
				continue
			}
			if !cleared {
				releaseLock()
				continue
			}
			releaseLock()
			s.logJob(pair.store, job, "info", "recovered output from an interrupted run")
			_ = pair.store.Orch().MarkCronJobRun(job.ID, s.now())
			continue
		}

		// Routine output has no user-facing route. Keep the marker until the
		// configured route (normally a log) succeeds.
		releaseLock()
		note := "recovered turn output from an interrupted routine run; the routine body did not finish"
		s.logJob(pair.store, job, "info", note)
		if text := strings.TrimSpace(result.ResponseText); text != "" {
			meta, metaErr := parseJobMeta(job.Meta)
			if metaErr != nil {
				log.Printf("cron: parse recovered routine meta for %s: %v", job.Title, metaErr)
				continue
			}
			routed := true
			if routeErr := s.route(ctx, pair.store, pair.agent, job, meta, text, result.SessionID, true); routeErr != nil {
				routed, _ = s.persistRoute(pair.store, pair.agent, job, routeErr)
				if !routed {
					s.logJob(pair.store, job, "error", "routing recovered output failed: "+routeErr.Error())
					continue
				}
			}
		}
		cleared, clearErr := pair.store.Orch().ClearCronJobPendingIf(job.ID, pending.MarkerID)
		if clearErr != nil {
			log.Printf("cron: consume pending turn for %s: %v", job.Title, clearErr)
			continue
		}
		if !cleared {
			continue
		}
		_ = pair.store.Orch().MarkCronJobRun(job.ID, s.now())
	}
}

// acquireCronSessionLock takes the shared per-session turn lock. The returned
// function releases it.
func acquireCronSessionLock(ctx context.Context, sessionID string) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, cronSessionLockWait)
	defer cancel()
	held, err := lock.AcquireSession(lockCtx, sessionID)
	if err != nil {
		return nil, err
	}
	return func() { _ = held.Release() }, nil
}

// cronOutputID is the deterministic outbox identity for a prompt job's
// collected output. Live execution and recovery use the same ID, so delivery is
// idempotent across a restart.
func cronOutputID(jobID, markerID string) string {
	return "cron_out_" + strings.TrimSpace(jobID) + "_" + strings.TrimSpace(markerID)
}

// enqueuePromptOutput durably queues a prompt result before its recovery marker
// is consumed. It returns false when there is no allowed chat to deliver to, so
// the caller falls back to its normal route.
// addPromptRouteIntent durably records what should happen to a collected
// prompt result before its recovery marker is consumed. Every route policy is
// represented, so a crash at any point can be recovered idempotently.
func (s *Service) addPromptRouteIntent(st *store.Store, agent registry.Agent, job orch.CronJob, markerID string, result *opencode.TurnResult) error {
	markerID = strings.TrimSpace(markerID)
	if markerID == "" {
		return fmt.Errorf("cron: prompt marker is required")
	}
	text := ""
	sessionID := ""
	sourceID := ""
	if result != nil {
		text = strings.TrimSpace(result.ResponseText)
		sessionID = result.SessionID
		if len(result.MessageIDs) > 0 {
			sourceID = result.MessageIDs[len(result.MessageIDs)-1]
		}
	}
	if text == "" {
		return nil
	}
	meta, metaErr := parseJobMeta(job.Meta)
	if metaErr != nil {
		return metaErr
	}
	action := meta.OnSuccess
	if action == "" {
		action = "notify-user"
	}
	switch action {
	case "notify-user", "inbox", "log":
	default:
		action = "notify-user"
	}
	chatID := int64(0)
	if action == "notify-user" {
		chat, ok, err := st.CommTG().LatestAllowedChat()
		if err != nil {
			return err
		}
		if ok {
			chatID = chat.ChatID
		}
	}
	id := cronOutputID(job.ID, markerID)
	routeMeta, _ := json.Marshal(map[string]any{"cron_job": job.Title, "cron_route": id})
	route := orch.CronRoute{
		ID:                id,
		AgentTarget:       agent.Name,
		Action:            action,
		JobID:             job.ID,
		ChatID:            chatID,
		Text:              text,
		OpenCodeSessionID: sessionID,
		Meta:              string(routeMeta),
		CreatedAt:         s.now(),
	}
	if action == "notify-user" {
		route.SourceMessageID = sourceID
	}
	return st.Orch().AddCronRoute(route)
}

// cronPendingToken returns a unique generation for a non-collectable pending
// state (indeterminate admission or routine-run).
func cronPendingToken() string {
	return envelope.NewID()
}

// setCronPending is the durable writer used by admission callbacks. It is a
// variable so tests can inject a transient failure.
var setCronPending = func(st *store.Store, jobID, sessionID, markerID, kind string, at time.Time) error {
	return st.Orch().SetCronJobPending(jobID, sessionID, markerID, kind, at)
}

var (
	cronPendingRetries    = 3
	cronPendingRetryDelay = 50 * time.Millisecond
)

// persistAdmittedPending retries the admitted pair before callers fall back to
// an indeterminate state. A transient store failure must not discard a marker
// the callback already has.
func persistAdmittedPending(st *store.Store, jobID, sessionID, markerID, kind string, at time.Time) error {
	var err error
	for attempt := 0; attempt < cronPendingRetries; attempt++ {
		if err = setCronPending(st, jobID, sessionID, markerID, kind, at); err == nil {
			return nil
		}
		if attempt+1 < cronPendingRetries {
			time.Sleep(cronPendingRetryDelay)
		}
	}
	return err
}

// settleInterruptedRun stops a marker that can never produce output from being
// probed forever. It clears the pending turn only if the generation still
// matches, records an honest incomplete note, and marks the run as attempted.
func (s *Service) settleInterruptedRun(st *store.Store, job orch.CronJob, generation, reason string) {
	cleared, err := st.Orch().ClearCronJobPendingIf(job.ID, generation)
	if err != nil || !cleared {
		// A newer run owns the pending state now; leave it alone.
		return
	}
	s.logJob(st, job, "error", "run interrupted: "+reason)
	_ = st.Orch().MarkCronJobRun(job.ID, s.now())
}

// jobRunning reports whether a run of the job is in flight in this process.
func (s *Service) jobRunning(agentName, jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[agentName+"/"+jobID]
}

func (s *Service) runJob(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob, meta jobMeta) (string, string, bool, bool, string, error) {
	switch job.Kind {
	case "shell":
		out, runErr := s.runShell(ctx, agent, job, meta)
		sessionID := job.SessionID
		if sessionID == "" {
			if session, ok, err := st.Orch().LatestSession(); err == nil && ok {
				sessionID = session.OpenCodeSessionID
			}
		}
		return out, sessionID, false, false, "", runErr
	case "prompt":
		return s.runPrompt(ctx, agent, st, job)
	case "routine":
		return s.runRoutine(ctx, agent, st, job)
	default:
		return "", "", false, false, "", fmt.Errorf("cron: unknown job kind %q", job.Kind)
	}
}

func (s *Service) route(ctx context.Context, st *store.Store, agent registry.Agent, job orch.CronJob, meta jobMeta, text, sessionID string, success bool) error {
	if text == "" {
		return nil
	}
	if len(text) > maxOutputBytes {
		text = text[:maxOutputBytes] + "\n[output truncated]"
	}
	action := meta.OnSuccess
	if !success {
		action = meta.OnFailure
	}
	if action == "" {
		switch {
		case job.Kind == "routine":
			// Routine output is internal by default. Routines that need to
			// reach the user deliver through the sink themselves (heartbeat),
			// and an explicit on_success/on_failure can still opt in.
			action = "log"
		case success && job.Kind == "shell":
			action = "inbox"
		default:
			action = "notify-user"
		}
	}
	switch action {
	case "inbox":
		_, inboxSession, err := s.sink.Inbox(ctx, agent, sessionID, text, map[string]any{"cron_job": job.Title})
		if inboxSession != "" {
			if _, persistErr := st.Orch().UpsertSession(cronThread(job), inboxSession, s.now()); persistErr != nil {
				log.Printf("cron: persist inbox session for %s: %v", job.Title, persistErr)
			}
		}
		if err != nil {
			return err
		}
		return nil
	case "notify-user":
		return s.sink.NotifyUser(ctx, agent, text)
	case "log":
		s.logJob(st, job, "info", "output: "+text)
		return nil
	default:
		log.Printf("cron: job %s has invalid routing action %q; falling back to notify-user", job.Title, action)
		return s.sink.NotifyUser(ctx, agent, text)
	}
}

// errMemoryOnlyRoute means the payload was kept only in memory.
var errMemoryOnlyRoute = errors.New("cron: route persisted only in memory")

// persistRoute stores a delivery intent when the outbox enqueue failed.
func (s *Service) persistRoute(st *store.Store, agent registry.Agent, job orch.CronJob, routeErr error) (bool, error) {
	var delivery *comm.DeliveryError
	if !errors.As(routeErr, &delivery) {
		s.logJob(st, job, "error", "route failed: "+routeErr.Error())
		return false, nil
	}
	route := orch.CronRoute{
		ID:                delivery.EnvelopeID,
		AgentTarget:       agent.Name,
		ChatID:            delivery.ChatID,
		Text:              delivery.Text,
		OpenCodeSessionID: delivery.OpenCodeSessionID,
		SourceMessageID:   delivery.SourceMessageID,
		CreatedAt:         s.now(),
	}
	if route.ID == "" {
		route.ID = fmt.Sprintf("cron_route_%d", s.now().UnixNano())
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = st.Orch().AddCronRoute(route); lastErr == nil {
			s.logJob(st, job, "warn", "queued undelivered output for retry: "+routeErr.Error())
			return true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := writeRouteFallback(agent.Workspace, route); err == nil {
		s.logJob(st, job, "warn", "queued undelivered output in fallback file: "+routeErr.Error())
		return true, nil
	}
	// Last resort: keep the payload in memory, but tell the caller to requeue
	// the job so a restart does not silently lose it.
	s.addMemoryRoute(agent.Workspace, route, job)
	s.logJob(st, job, "error", "queued undelivered output in memory (DB and file unavailable): "+routeErr.Error())
	return false, errMemoryOnlyRoute
}

// requeueAfterPersistFailure retries moving the job's next run so a failed
// route persistence is not treated as a completed run.
func (s *Service) requeueAfterPersistFailure(st *store.Store, job orch.CronJob) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := st.Orch().RequeueCronJob(job.ID, s.now().Add(30*time.Second)); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	log.Printf("cron: CRITICAL: could not requeue %s after route persistence failure; output may be lost", job.Title)
}

func (s *Service) addMemoryRoute(workspace string, route orch.CronRoute, job orch.CronJob) {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	for _, existing := range s.memoryRoutes {
		if existing.workspace == workspace && existing.route.ID == route.ID {
			return
		}
	}
	s.memoryRoutes = append(s.memoryRoutes, memoryRoute{
		workspace: workspace,
		route:     route,
		jobID:     job.ID,
		spec:      job.Spec,
		timezone:  job.Timezone,
	})
}

func (s *Service) memoryRoutesFor(workspace string) []memoryRoute {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	var routes []memoryRoute
	for _, item := range s.memoryRoutes {
		if item.workspace == workspace {
			routes = append(routes, item)
		}
	}
	return routes
}

func (s *Service) removeMemoryRoute(workspace, id string) {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	for index, item := range s.memoryRoutes {
		if item.workspace == workspace && item.route.ID == id {
			s.memoryRoutes = append(s.memoryRoutes[:index], s.memoryRoutes[index+1:]...)
			return
		}
	}
}

// errRouteStillPending means a route's admitted input is still queued; retry
// later without re-admitting it.
var errRouteStillPending = errors.New("cron: route input still pending")

// errRouteHeld means an admission marker could not be persisted and the route
// was parked; do not clear the hold with a normal retry.
var errRouteHeld = errors.New("cron: route held for manual recovery")

// deliverRoute performs one durable route intent according to its action.
func (s *Service) deliverRoute(ctx context.Context, pair *agentStore, route orch.CronRoute) error {
	switch route.Action {
	case "inbox":
		return s.deliverInboxRoute(ctx, pair, route)
	case "log":
		job, err := pair.store.Orch().GetCronJobByID(route.JobID)
		if err != nil {
			job = orch.CronJob{ID: route.JobID, Title: route.JobID}
		}
		s.logJob(pair.store, job, "info", "output: "+route.Text)
		return nil
	default:
		chatID := route.ChatID
		if chatID == 0 {
			chat, ok, err := pair.store.CommTG().LatestAllowedChat()
			if err != nil {
				return err
			}
			if !ok {
				// No registered Telegram chat: let the configured sink try
				// (tests and alternate sinks), then retry if it fails.
				return s.sink.NotifyUser(ctx, pair.agent, route.Text)
			}
			chatID = chat.ChatID
		}
		_, err := pair.store.CommTG().EnqueueOutbound(commtg.OutboxItem{
			ID:                route.ID,
			EnvelopeID:        route.ID,
			ChatID:            chatID,
			Text:              route.Text,
			OpenCodeSessionID: route.OpenCodeSessionID,
			SourceMessageID:   route.SourceMessageID,
			CreatedAt:         s.now(),
		})
		return err
	}
}

// cronReplyChat resolves the chat that receives an inbox turn's response.
func cronReplyChat(st *store.Store, sessionID string) (int64, bool) {
	if session, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID); err == nil && ok {
		if chatID, parsed := envelope.ParseTelegramThread(session.Thread); parsed && chatID != 0 {
			return chatID, true
		}
	}
	chat, ok, err := st.CommTG().LatestAllowedChat()
	if err != nil || !ok {
		return 0, false
	}
	return chat.ChatID, true
}

// queueInboxResponse enqueues the agent's reaction to an inbox event under a
// deterministic ID before the route is marked delivered.
func (s *Service) queueInboxResponse(pair *agentStore, route orch.CronRoute, result *opencode.TurnResult) error {
	if result == nil {
		return nil
	}
	text := strings.TrimSpace(result.ResponseText)
	if text == "" {
		return nil
	}
	chatID, ok := cronReplyChat(pair.store, result.SessionID)
	if !ok {
		return fmt.Errorf("cron: no chat for inbox response route %s", route.ID)
	}
	sourceID := ""
	if len(result.MessageIDs) > 0 {
		sourceID = result.MessageIDs[len(result.MessageIDs)-1]
	}
	id := "cron_inbox_" + route.ID
	_, err := pair.store.CommTG().EnqueueOutbound(commtg.OutboxItem{
		ID:                id,
		EnvelopeID:        id,
		ChatID:            chatID,
		Text:              text,
		OpenCodeSessionID: result.SessionID,
		SourceMessageID:   sourceID,
		CreatedAt:         s.now(),
	})
	return err
}

// persistRouteAdmitted retries storing an inbox route's admission marker. A
// transient write failure must not leave the route eligible for re-admission.
func persistRouteAdmitted(orchStore *orch.Store, id, sessionID, markerID string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = orchStore.SetCronRouteAdmitted(id, sessionID, markerID); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
}

// deliverInboxRoute admits the output as a synthetic input, or collects the
// input a previous attempt already admitted. The route row stores that marker,
// so a crash never re-admits the event.
func (s *Service) deliverInboxRoute(ctx context.Context, pair *agentStore, route orch.CronRoute) error {
	prober, supportsRecovery := s.turn.(turnCollector)
	if route.SourceMessageID != "" {
		if !supportsRecovery {
			return fmt.Errorf("cron: turn runner does not support inbox route recovery")
		}
		result, delivered, err := prober.CollectDelivered(ctx, route.OpenCodeSessionID, route.SourceMessageID)
		if err != nil {
			if errors.Is(err, opencode.ErrTurnTruncated) || opencode.IsPermanentError(err) {
				// The admitted turn cannot produce bounded output; settle the
				// route instead of re-admitting it.
				return nil
			}
			return err
		}
		if delivered {
			if err := s.queueInboxResponse(pair, route, result); err != nil {
				return err
			}
			return nil
		}
		pending, err := prober.InboxPending(ctx, route.OpenCodeSessionID, route.SourceMessageID)
		if err != nil {
			return err
		}
		if pending {
			return errRouteStillPending
		}
		return nil
	}
	if s.turn == nil {
		return fmt.Errorf("cron: turn runner is not configured")
	}
	sessionID := route.OpenCodeSessionID
	if sessionID == "" {
		if session, ok, err := pair.store.Orch().LatestSession(); err == nil && ok {
			sessionID = session.OpenCodeSessionID
		}
	}
	if sessionID == "" {
		return fmt.Errorf("cron: inbox route has no session")
	}
	meta := map[string]any{}
	if route.Meta != "" {
		_ = json.Unmarshal([]byte(route.Meta), &meta)
	}
	result, runErr := s.turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        route.Text,
		Description: "Cron inbox event",
		Metadata:    meta,
		Delivery:    "queue",
	}, opencode.TurnConfig{
		Workspace: pair.agent.Workspace,
		OnAdmitted: func(admitted opencode.Admitted) error {
			return persistRouteAdmitted(pair.store.Orch(), route.ID, admitted.SessionID, admitted.InputID)
		},
		OnCollected: func(result *opencode.TurnResult) error {
			if err := s.queueInboxResponse(pair, route, result); err != nil {
				return err
			}
			return pair.store.Orch().MarkCronRouteDelivered(route.ID, s.now())
		},
	})
	if runErr != nil && result != nil && result.InputID != "" && route.SourceMessageID == "" {
		// OpenCode accepted the input but the callback's marker write failed.
		// Persist it from the returned result; if that also fails, park the
		// route so a retry cannot admit the same event again.
		if saveErr := persistRouteAdmitted(pair.store.Orch(), route.ID, result.SessionID, result.InputID); saveErr != nil {
			_ = pair.store.Orch().HoldCronRoute(route.ID, s.now().Add(24*time.Hour))
			log.Printf("cron: hold inbox route %s: admission marker could not be persisted: %v", route.ID, saveErr)
			return errRouteHeld
		}
	}
	return runErr
}

func (s *Service) retryRoutes(ctx context.Context, pair *agentStore) {
	if routes, err := pair.store.Orch().DueCronRoutes(s.now(), 20); err != nil {
		log.Printf("cron: list due routes for %s: %v", pair.agent.Name, err)
	} else {
		for _, snapshot := range routes {
			lease := s.now().Add(routeClaimLease)
			claimed, claimErr := pair.store.Orch().ClaimCronRoute(snapshot.ID, s.now(), lease)
			if claimErr != nil || !claimed {
				continue
			}
			// Re-read after claiming: an earlier worker may have admitted the
			// input before its lease expired.
			route, ok, getErr := pair.store.Orch().GetCronRoute(snapshot.ID)
			if getErr != nil || !ok || route.DeliveredAt != nil {
				continue
			}
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(routeClaimHeartbeat)
				defer ticker.Stop()
				expected := lease
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						next := s.now().Add(routeClaimLease)
						renewed, renewErr := pair.store.Orch().RenewCronRouteClaim(route.ID, expected, next)
						if renewErr != nil || !renewed {
							return
						}
						expected = next
					}
				}
			}()
			deliverErr := s.deliverRoute(ctx, pair, route)
			close(stop)
			<-done
			if deliverErr != nil {
				if errors.Is(deliverErr, errRouteHeld) {
					continue
				}
				attempts := route.Attempts + 1
				_ = pair.store.Orch().MarkCronRouteRetry(route.ID, attempts, s.now().Add(routeBackoff(attempts)), deliverErr.Error())
				continue
			}
			_ = pair.store.Orch().MarkCronRouteDelivered(route.ID, s.now())
		}
	}

	fallbacks, err := readRouteFallbacks(pair.agent.Workspace)
	if err != nil {
		log.Printf("cron: read route fallbacks for %s: %v", pair.agent.Name, err)
		return
	}
	for _, route := range fallbacks {
		_, enqueueErr := pair.store.CommTG().EnqueueOutbound(commtg.OutboxItem{
			ID:                route.ID,
			EnvelopeID:        route.ID,
			ChatID:            route.ChatID,
			Text:              route.Text,
			OpenCodeSessionID: route.OpenCodeSessionID,
			SourceMessageID:   route.SourceMessageID,
			CreatedAt:         s.now(),
		})
		if enqueueErr != nil {
			continue
		}
		_ = removeRouteFallback(pair.agent.Workspace, route.ID)
	}

	for _, pending := range s.memoryRoutesFor(pair.agent.Workspace) {
		route := pending.route
		_, enqueueErr := pair.store.CommTG().EnqueueOutbound(commtg.OutboxItem{
			ID:                route.ID,
			EnvelopeID:        route.ID,
			ChatID:            route.ChatID,
			Text:              route.Text,
			OpenCodeSessionID: route.OpenCodeSessionID,
			SourceMessageID:   route.SourceMessageID,
			CreatedAt:         s.now(),
		})
		if enqueueErr != nil {
			continue
		}
		if err := s.restoreRequeuedJob(pair, pending); err != nil {
			log.Printf("cron: restore schedule for %s: %v", route.ID, err)
			continue
		}
		s.removeMemoryRoute(pair.agent.Workspace, route.ID)
	}
}

func (s *Service) hasMemoryRouteForJob(workspace, jobID string) bool {
	if jobID == "" {
		return false
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	for _, pending := range s.memoryRoutes {
		if pending.workspace == workspace && pending.jobID == jobID {
			return true
		}
	}
	return false
}

func (s *Service) restoreRequeuedJob(pair *agentStore, pending memoryRoute) error {
	if pending.jobID == "" || pending.spec == "" {
		return nil
	}
	location := time.Local
	if pending.timezone != "" {
		if loaded, err := time.LoadLocation(pending.timezone); err == nil {
			location = loaded
		}
	}
	schedule, err := cronlib.ParseStandard(pending.spec)
	if err != nil {
		return err
	}
	next := schedule.Next(s.now().In(location)).UTC()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = pair.store.Orch().RequeueCronJob(pending.jobID, next); lastErr != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if lastErr = pair.store.Orch().MarkCronJobRun(pending.jobID, s.now()); lastErr != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func writeRouteFallback(workspace string, route orch.CronRoute) error {
	path, err := routeFallbackPath(workspace, route.ID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(route)
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func readRouteFallbacks(workspace string) ([]orch.CronRoute, error) {
	dir := filepath.Join(workspace, routeFallbackDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var routes []orch.CronRoute
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := routeFallbackPath(workspace, id); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var route orch.CronRoute
		if err := json.Unmarshal(raw, &route); err != nil || route.ID != id || route.ChatID == 0 || route.Text == "" {
			continue
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func removeRouteFallback(workspace, id string) error {
	path, err := routeFallbackPath(workspace, id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func routeFallbackPath(workspace, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("cron: unsafe route fallback id %q", id)
	}
	dir := filepath.Clean(filepath.Join(workspace, routeFallbackDir))
	path := filepath.Join(dir, id+".json")
	if filepath.Dir(path) != dir {
		return "", fmt.Errorf("cron: route fallback path escapes %s", dir)
	}
	return path, nil
}

func routeBackoff(attempts int) time.Duration {
	delay := 5 * time.Second
	for index := 1; index < attempts; index++ {
		delay *= 3
		if delay >= 15*time.Minute {
			return 15 * time.Minute
		}
	}
	return delay
}

func (s *Service) logJob(st *store.Store, job orch.CronJob, level, message string) {
	if err := st.Orch().AppendLog(level, "cron:"+job.Title, message, job.Meta, s.now()); err != nil {
		log.Printf("cron: append log for %s: %v", job.Title, err)
	}
}
