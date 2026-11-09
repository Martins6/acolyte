package cron

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/comm"
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
	text, sessionID, err := s.runJob(ctx, agent, st, job, meta)

	if errors.Is(err, routine.ErrPendingPhase4) {
		_ = st.Orch().MarkCronJobRun(job.ID, s.now())
		s.logJob(st, job, "info", fmt.Sprintf("pending Phase 4: %v", err))
		return
	}
	if err != nil {
		if strings.TrimSpace(text) == "" {
			text = fmt.Sprintf("cron job %q failed: %v", job.Title, err)
		}
		s.logJob(st, job, "error", fmt.Sprintf("failed after %s: %v", s.now().Sub(started).Round(time.Millisecond), err))
		if routeErr := s.route(ctx, st, agent, job, meta, text, sessionID, false); routeErr != nil {
			if persistErr := s.persistRoute(st, agent, job, routeErr); persistErr != nil {
				s.logJob(st, job, "error", "persist route: "+persistErr.Error())
				_ = st.Orch().RequeueCronJob(job.ID, s.now().Add(30*time.Second))
				return
			}
		}
		_ = st.Orch().MarkCronJobRun(job.ID, s.now())
		return
	}

	s.logJob(st, job, "info", fmt.Sprintf("completed in %s", s.now().Sub(started).Round(time.Millisecond)))
	if text != "" {
		if routeErr := s.route(ctx, st, agent, job, meta, text, sessionID, true); routeErr != nil {
			if persistErr := s.persistRoute(st, agent, job, routeErr); persistErr != nil {
				s.logJob(st, job, "error", "persist route: "+persistErr.Error())
				_ = st.Orch().RequeueCronJob(job.ID, s.now().Add(30*time.Second))
				return
			}
		}
	}
	_ = st.Orch().MarkCronJobRun(job.ID, s.now())
}

func (s *Service) runJob(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob, meta jobMeta) (string, string, error) {
	switch job.Kind {
	case "shell":
		out, runErr := s.runShell(ctx, agent, job, meta)
		sessionID := job.SessionID
		if sessionID == "" {
			if session, ok, err := st.Orch().LatestSession(); err == nil && ok {
				sessionID = session.OpenCodeSessionID
			}
		}
		return out, sessionID, runErr
	case "prompt":
		return s.runPrompt(ctx, agent, st, job)
	case "routine":
		return s.runRoutine(ctx, agent, st, job)
	default:
		return "", "", fmt.Errorf("cron: unknown job kind %q", job.Kind)
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
		if success && job.Kind == "shell" {
			action = "inbox"
		} else {
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

// persistRoute stores a delivery intent when the outbox enqueue failed.
func (s *Service) persistRoute(st *store.Store, agent registry.Agent, job orch.CronJob, routeErr error) error {
	var delivery *comm.DeliveryError
	if !errors.As(routeErr, &delivery) {
		s.logJob(st, job, "error", "route failed: "+routeErr.Error())
		return nil
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
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = st.Orch().AddCronRoute(route); lastErr == nil {
			s.logJob(st, job, "warn", "queued undelivered output for retry: "+routeErr.Error())
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return lastErr
}

func (s *Service) retryRoutes(_ context.Context, pair *agentStore) {
	routes, err := pair.store.Orch().DueCronRoutes(s.now(), 20)
	if err != nil {
		log.Printf("cron: list due routes for %s: %v", pair.agent.Name, err)
		return
	}
	for _, route := range routes {
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
			attempts := route.Attempts + 1
			_ = pair.store.Orch().MarkCronRouteRetry(route.ID, attempts, s.now().Add(routeBackoff(attempts)), enqueueErr.Error())
			continue
		}
		_ = pair.store.Orch().MarkCronRouteDelivered(route.ID, s.now())
	}
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
