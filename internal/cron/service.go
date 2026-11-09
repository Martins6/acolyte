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

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
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

// Options configures the cron service.
type Options struct {
	Registry        *registry.Registry
	Routines        *routine.Registry
	Sink            Sink
	OpenCode        TurnRunner
	Interval        time.Duration
	RefreshInterval time.Duration
	MaxConcurrency  int
	Now             func() time.Time
	Shell           string
}

type agentStore struct {
	agent registry.Agent
	store *store.Store
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

	mu      sync.Mutex
	stores  map[string]*agentStore
	retired []*agentStore
	running map[string]bool
	sem     chan struct{}
	wg      sync.WaitGroup
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
		stores:          map[string]*agentStore{},
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

// Tick dispatches every currently due job. It does not wait for execution.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	pairs := make([]*agentStore, 0, len(s.stores))
	for _, st := range s.stores {
		pairs = append(pairs, st)
	}
	s.mu.Unlock()

	for _, pair := range pairs {
		jobs, err := pair.store.Orch().DueCronJobs(now)
		if err != nil {
			log.Printf("cron: list due jobs for %s: %v", pair.agent.Name, err)
			continue
		}
		for _, job := range jobs {
			s.dispatch(ctx, pair.agent, pair.store, job, now)
		}
	}
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

	s.mu.Lock()
	for name, st := range s.stores {
		if _, ok := wanted[name]; !ok {
			// Keep the store open: a dispatched job may still hold it.
			s.retired = append(s.retired, st)
			delete(s.stores, name)
		}
	}
	s.mu.Unlock()

	for name, agent := range wanted {
		s.mu.Lock()
		if _, exists := s.stores[name]; exists {
			s.mu.Unlock()
			continue
		}
		reused := false
		for index, retired := range s.retired {
			if retired.agent.Name == name && retired.agent.Workspace == agent.Workspace {
				s.stores[name] = retired
				s.retired = append(s.retired[:index], s.retired[index+1:]...)
				reused = true
				break
			}
		}
		s.mu.Unlock()
		if reused {
			continue
		}

		st, err := store.Open(agent.Workspace)
		if err != nil {
			log.Printf("cron: open store for %s: %v", name, err)
			continue
		}
		s.mu.Lock()
		s.stores[name] = &agentStore{agent: agent, store: st}
		s.mu.Unlock()
	}
}

func (s *Service) closeStores() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, st := range s.stores {
		_ = st.store.Close()
		delete(s.stores, name)
	}
	for _, st := range s.retired {
		_ = st.store.Close()
	}
	s.retired = nil
}

func (s *Service) dispatch(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob, now time.Time) {
	key := agent.Name + "/" + job.ID
	s.mu.Lock()
	if s.running[key] {
		s.mu.Unlock()
		return
	}
	s.running[key] = true
	s.mu.Unlock()

	release := func() {
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
	}

	location := time.Local
	if job.Timezone != "" {
		loaded, err := time.LoadLocation(job.Timezone)
		if err != nil {
			log.Printf("cron: job %s has invalid timezone %q: %v", job.Title, job.Timezone, err)
			_ = st.Orch().SetCronJobEnabled(job.Title, false)
			release()
			return
		}
		location = loaded
	}

	schedule, err := cronlib.ParseStandard(job.Spec)
	if err != nil {
		log.Printf("cron: job %s has invalid spec %q: %v", job.Title, job.Spec, err)
		_ = st.Orch().SetCronJobEnabled(job.Title, false)
		release()
		return
	}
	next := schedule.Next(now.In(location)).UTC()

	claimed, err := st.Orch().ClaimCronJob(job.ID, job.NextRunAt, next)
	if err != nil {
		log.Printf("cron: claim job %s: %v", job.Title, err)
		release()
		return
	}
	if !claimed {
		release()
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		defer release()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("cron: job %s panicked: %v", job.Title, recovered)
			}
		}()
		s.execute(ctx, agent, st, job)
	}()
}

func (s *Service) execute(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) {
	started := s.now()
	meta := parseJobMeta(job.Meta)
	text, sessionID, err := s.runJob(ctx, agent, st, job, meta)

	if markErr := st.Orch().MarkCronJobRun(job.ID, s.now()); markErr != nil {
		log.Printf("cron: mark job %s: %v", job.Title, markErr)
	}

	if errors.Is(err, routine.ErrPendingPhase4) {
		s.logJob(st, job, "info", fmt.Sprintf("pending Phase 4: %v", err))
		return
	}
	if err != nil {
		if strings.TrimSpace(text) == "" {
			text = fmt.Sprintf("cron job %q failed: %v", job.Title, err)
		}
		s.logJob(st, job, "error", fmt.Sprintf("failed after %s: %v", s.now().Sub(started).Round(time.Millisecond), err))
		s.route(ctx, agent, job, meta, text, sessionID, false)
		return
	}

	s.logJob(st, job, "info", fmt.Sprintf("completed in %s", s.now().Sub(started).Round(time.Millisecond)))
	if text != "" {
		s.route(ctx, agent, job, meta, text, sessionID, true)
	}
}

func (s *Service) runJob(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob, meta jobMeta) (string, string, error) {
	switch job.Kind {
	case "shell":
		out, err := s.runShell(ctx, agent, job, meta)
		return out, "", err
	case "prompt":
		return s.runPrompt(ctx, agent, st, job)
	case "routine":
		return s.runRoutine(ctx, agent, st, job)
	default:
		return "", "", fmt.Errorf("cron: unknown job kind %q", job.Kind)
	}
}

func (s *Service) route(ctx context.Context, agent registry.Agent, job orch.CronJob, meta jobMeta, text, sessionID string, success bool) {
	if text == "" {
		return
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
		if _, err := s.sink.Inbox(ctx, agent, sessionID, text, map[string]any{"cron_job": job.Title}); err != nil {
			log.Printf("cron: route job %s to inbox: %v", job.Title, err)
		}
	case "notify-user":
		if err := s.sink.NotifyUser(ctx, agent, text); err != nil {
			log.Printf("cron: route job %s to user: %v", job.Title, err)
		}
	case "log":
	}
}

func (s *Service) logJob(st *store.Store, job orch.CronJob, level, message string) {
	if err := st.Orch().AppendLog(level, "cron:"+job.Title, message, job.Meta, s.now()); err != nil {
		log.Printf("cron: append log for %s: %v", job.Title, err)
	}
}
