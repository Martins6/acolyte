// Package daemon wires registered agents to their Communication adapters and
// routes inbound envelopes into OpenCode turns.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/comm/telegram"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/secrets"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/storepool"
)

const (
	// DefaultRefreshInterval is how often the registry/config is re-read.
	DefaultRefreshInterval = 30 * time.Second
)

// TurnRunner is the OpenCode surface used by the router and inbox sink.
type TurnRunner interface {
	comm.TurnRunner
	comm.SyntheticTurnRunner
}

// CommFactory builds one agent's Communication adapter.
type CommFactory func(agent registry.Agent, token string, allowed []string, st *store.Store, now func() time.Time) (comm.Communication, error)

// Options configures the multi-agent daemon.
type Options struct {
	Registry        *registry.Registry
	Stores          *storepool.Pool
	OpenCode        TurnRunner
	RefreshInterval time.Duration
	Now             func() time.Time
	CommFactory     CommFactory
}

type agentRuntime struct {
	agent       registry.Agent
	store       *store.Store
	release     func()
	comm        comm.Communication
	fingerprint string
	tokenHash   string
	tokenSource string
}

type commConfig struct {
	channels    []string
	allowed     []string
	token       string
	tokenSource string
	tokenHash   string
	fingerprint string
}

// Service runs one Communication adapter per configured agent.
type Service struct {
	registry        *registry.Registry
	stores          *storepool.Pool
	refreshInterval time.Duration
	now             func() time.Time
	factory         CommFactory
	router          *comm.Router
	sink            comm.OutboxSink

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	refreshMu sync.Mutex
	mu        sync.Mutex
	agents    map[string]*agentRuntime
}

// Start starts the daemon and performs one registry refresh.
func Start(ctx context.Context, opts Options) (*Service, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("daemon: registry is required")
	}
	if opts.Stores == nil {
		return nil, fmt.Errorf("daemon: store pool is required")
	}
	if opts.OpenCode == nil {
		return nil, fmt.Errorf("daemon: opencode turn runner is required")
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = DefaultRefreshInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CommFactory == nil {
		opts.CommFactory = defaultCommFactory
	}

	service := &Service{
		registry:        opts.Registry,
		stores:          opts.Stores,
		refreshInterval: opts.RefreshInterval,
		now:             opts.Now,
		factory:         opts.CommFactory,
		sink:            comm.OutboxSink{Stores: opts.Stores, Turn: opts.OpenCode, Now: opts.Now},
		done:            make(chan struct{}),
		agents:          map[string]*agentRuntime{},
	}
	router, err := comm.NewRouter(comm.RouterOptions{
		Stores:   opts.Stores,
		Turn:     opts.OpenCode,
		Outbound: service.sendOutbound,
		Now:      opts.Now,
	})
	if err != nil {
		return nil, err
	}
	service.router = router
	service.ctx, service.cancel = context.WithCancel(ctx)
	service.refresh()

	go service.run()
	return service, nil
}

// Stop stops every adapter and releases its store reference.
func (s *Service) Stop() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		log.Printf("daemon: shutdown timed out")
	}
}

// NotifyUser implements the cron sink.
func (s *Service) NotifyUser(ctx context.Context, agent registry.Agent, text string) error {
	return s.sink.NotifyUser(ctx, agent, text)
}

// Inbox implements the cron sink.
func (s *Service) Inbox(ctx context.Context, agent registry.Agent, sessionID, text string, meta map[string]any) (string, string, error) {
	return s.sink.Inbox(ctx, agent, sessionID, text, meta)
}

// NotifyAll enqueues a user notification for every running agent. It returns
// how many agents accepted the message.
func (s *Service) NotifyAll(ctx context.Context, text string) int {
	s.mu.Lock()
	runtimes := make([]*agentRuntime, 0, len(s.agents))
	for _, rt := range s.agents {
		runtimes = append(runtimes, rt)
	}
	s.mu.Unlock()

	count := 0
	for _, rt := range runtimes {
		if err := s.sink.NotifyUser(ctx, rt.agent, text); err == nil {
			count++
		}
	}
	return count
}

func (s *Service) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.stopAgents()
			return
		case <-ticker.C:
			s.refresh()
		}
	}
}

func (s *Service) refresh() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	agents, err := s.registry.List()
	if err != nil {
		log.Printf("daemon: list agents: %v", err)
		return
	}
	wanted := make(map[string]registry.Agent, len(agents))
	for _, agent := range agents {
		if agent.Enabled {
			wanted[agent.Name] = agent
		}
	}

	var stale []*agentRuntime
	s.mu.Lock()
	for name, rt := range s.agents {
		agent, ok := wanted[name]
		if !ok || agent.Workspace != rt.agent.Workspace {
			delete(s.agents, name)
			stale = append(stale, rt)
			continue
		}
		cfg, err := s.loadConfig(agent, rt.store)
		if err != nil || cfg.fingerprint != rt.fingerprint {
			delete(s.agents, name)
			stale = append(stale, rt)
		}
	}
	existing := make(map[string]*agentRuntime, len(s.agents))
	seenTokens := map[string]string{}
	for name, rt := range s.agents {
		existing[name] = rt
		seenTokens[rt.tokenHash] = name
	}
	s.mu.Unlock()

	for _, rt := range stale {
		_ = rt.comm.Stop()
		rt.release()
	}

	for name, agent := range wanted {
		if _, ok := existing[name]; ok {
			continue
		}
		s.startAgent(agent, seenTokens)
	}
}

func (s *Service) startAgent(agent registry.Agent, seenTokens map[string]string) {
	st, release, err := s.stores.Acquire(agent.Workspace)
	if err != nil {
		log.Printf("daemon: open store for %s: %v", agent.Name, err)
		return
	}
	cfg, err := s.loadConfig(agent, st)
	if err != nil {
		log.Printf("daemon: load communication config for %s: %v", agent.Name, err)
		release()
		return
	}
	if !containsChannel(cfg.channels, "telegram") {
		log.Printf("daemon: telegram is not enabled for %s", agent.Name)
		release()
		return
	}
	if len(cfg.allowed) == 0 {
		log.Printf("daemon: %s has no Telegram allowed users; skipping channel", agent.Name)
		release()
		return
	}
	if cfg.token == "" {
		log.Printf("daemon: %s has no TELEGRAM_BOT_TOKEN; skipping channel", agent.Name)
		release()
		return
	}
	if other, ok := seenTokens[cfg.tokenHash]; ok {
		log.Printf("daemon: %s uses the same Telegram token as %s; skipping channel (bots require distinct tokens)", agent.Name, other)
		release()
		return
	}

	adapter, err := s.factory(agent, cfg.token, cfg.allowed, st, s.now)
	if err != nil {
		log.Printf("daemon: create communication for %s: %v", agent.Name, err)
		release()
		return
	}
	sink := func(ctx context.Context, env envelope.Envelope) error {
		if err := s.router.HandleInbound(ctx, agent, env); err != nil {
			log.Printf("daemon: inbound for %s: %v", agent.Name, err)
			return err
		}
		return nil
	}
	if err := adapter.Start(s.ctx, sink); err != nil {
		log.Printf("daemon: start communication for %s: %v", agent.Name, err)
		_ = adapter.Stop()
		release()
		return
	}

	rt := &agentRuntime{
		agent:       agent,
		store:       st,
		release:     release,
		comm:        adapter,
		fingerprint: cfg.fingerprint,
		tokenHash:   cfg.tokenHash,
		tokenSource: cfg.tokenSource,
	}
	s.mu.Lock()
	if _, exists := s.agents[agent.Name]; exists {
		s.mu.Unlock()
		_ = adapter.Stop()
		release()
		return
	}
	s.agents[agent.Name] = rt
	s.mu.Unlock()
	seenTokens[cfg.tokenHash] = agent.Name
	log.Printf("daemon: started telegram for %s (token from %s)", agent.Name, cfg.tokenSource)
}

func (s *Service) stopAgents() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	runtimes := s.agents
	s.agents = map[string]*agentRuntime{}
	s.mu.Unlock()
	for _, rt := range runtimes {
		_ = rt.comm.Stop()
		rt.release()
	}
}

func (s *Service) sendOutbound(_ context.Context, agent registry.Agent, env envelope.Envelope) error {
	// Durability does not depend on a live adapter: resolve the current
	// desired workspace and enqueue there. The adapter's outbox loop delivers.
	return s.enqueueOutbound(s.desiredAgent(agent), env)
}

func (s *Service) desiredAgent(agent registry.Agent) registry.Agent {
	s.mu.Lock()
	rt := s.agents[agent.Name]
	s.mu.Unlock()
	if rt != nil {
		return rt.agent
	}
	if current, err := s.registry.GetByName(agent.Name); err == nil {
		return current
	}
	return agent
}

func (s *Service) enqueueOutbound(agent registry.Agent, env envelope.Envelope) error {
	chatID, err := outboundChatID(env)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(env.Content.Text)
	if text == "" {
		return fmt.Errorf("daemon: outbound text is required")
	}
	st, release, err := s.stores.Acquire(agent.Workspace)
	if err != nil {
		return fmt.Errorf("daemon: open agent store: %w", err)
	}
	defer release()
	_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID:        env.ID,
		ChatID:            chatID,
		Text:              text,
		ReplyTo:           daemonMetaInt64(env.Meta["reply_to_message_id"]),
		OpenCodeSessionID: daemonMetaString(env.Meta["opencode_session_id"]),
		SourceMessageID:   daemonMetaString(env.Meta["source_message_id"]),
		CreatedAt:         s.now(),
	})
	if err != nil {
		return fmt.Errorf("daemon: queue outbound reply: %w", err)
	}
	return nil
}

func outboundChatID(env envelope.Envelope) (int64, error) {
	if chatID, ok := envelope.ParseTelegramThread(env.Thread); ok && chatID != 0 {
		return chatID, nil
	}
	if chatID, err := strconv.ParseInt(strings.TrimSpace(env.To.ID), 10, 64); err == nil && chatID != 0 {
		return chatID, nil
	}
	return 0, fmt.Errorf("daemon: outbound envelope has no chat id")
}

func daemonMetaInt64(value any) *int64 {
	switch typed := value.(type) {
	case int:
		out := int64(typed)
		return &out
	case int64:
		out := typed
		return &out
	case float64:
		out := int64(typed)
		if float64(out) != typed {
			return nil
		}
		return &out
	case json.Number:
		out, err := typed.Int64()
		if err != nil {
			return nil
		}
		return &out
	case string:
		out, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		return &out
	default:
		return nil
	}
}

func daemonMetaString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func (s *Service) loadConfig(agent registry.Agent, st *store.Store) (commConfig, error) {
	channels, err := loadStringList(st, "channels", []string{"telegram"})
	if err != nil {
		return commConfig{}, err
	}
	allowed, err := loadStringList(st, "telegram.allowed_users", nil)
	if err != nil {
		return commConfig{}, err
	}
	loaded, err := secrets.Load(agent.Workspace)
	if err != nil {
		return commConfig{}, err
	}
	token, _ := loaded.Lookup("TELEGRAM_BOT_TOKEN")
	source := tokenSource(loaded)
	tokenHash := hashToken(token)

	sortedChannels := append([]string(nil), channels...)
	sort.Strings(sortedChannels)
	sortedAllowed := append([]string(nil), allowed...)
	sort.Strings(sortedAllowed)
	sum := sha256.Sum256([]byte(agent.Workspace + "\x00" + token + "\x00" +
		strings.Join(sortedChannels, ",") + "\x00" + strings.Join(sortedAllowed, ",")))

	return commConfig{
		channels:    channels,
		allowed:     allowed,
		token:       token,
		tokenSource: source,
		tokenHash:   tokenHash,
		fingerprint: fmt.Sprintf("%x", sum),
	}, nil
}

func loadStringList(st *store.Store, key string, fallback []string) ([]string, error) {
	raw, ok, err := st.CommTG().GetSetting(key)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return append([]string(nil), fallback...), nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse %s setting: %w", key, err)
	}
	return values, nil
}

func tokenSource(loaded *secrets.Secrets) string {
	if _, ok := os.LookupEnv("TELEGRAM_BOT_TOKEN"); ok {
		return "process"
	}
	if _, ok := loaded.Workspace()["TELEGRAM_BOT_TOKEN"]; ok {
		return "workspace"
	}
	return "global"
}

func hashToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

func containsChannel(channels []string, want string) bool {
	for _, channel := range channels {
		if strings.EqualFold(strings.TrimSpace(channel), want) {
			return true
		}
	}
	return false
}

func defaultCommFactory(agent registry.Agent, token string, allowed []string, st *store.Store, now func() time.Time) (comm.Communication, error) {
	return telegram.New(telegram.Options{
		Agent:        agent,
		Store:        st,
		Token:        token,
		AllowedUsers: allowed,
		Now:          now,
	})
}
