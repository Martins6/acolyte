// Package telegram implements the Telegram Communication adapter.
package telegram

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/media"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

const (
	// Channel is the envelope channel name.
	Channel = "telegram"
	// DefaultPollInterval is how often the outbox is drained.
	DefaultPollInterval = 5 * time.Second
	// DefaultMaxAttempts is the retry limit before a row is marked failed.
	DefaultMaxAttempts = 5
	// DefaultMaxMessageChars keeps chunks below Telegram's 4096 UTF-16-unit
	// limit (the constant name is retained for compatibility).
	DefaultMaxMessageChars = 4000
)

// API is the Telegram surface the adapter uses. *bot.Bot implements it.
type API interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	GetFile(ctx context.Context, params *bot.GetFileParams) (*models.File, error)
	Token() string
}

// Options configures one agent's Telegram adapter.
type Options struct {
	Agent           registry.Agent
	Store           *store.Store
	Token           string
	AllowedUsers    []string
	Now             func() time.Time
	PollInterval    time.Duration
	MaxAttempts     int
	MaxMessageChars int
}

// Adapter is the Communication implementation for one Telegram bot.
type Adapter struct {
	agent           registry.Agent
	store           *store.Store
	token           string
	allowed         []string
	now             func() time.Time
	pollInterval    time.Duration
	maxAttempts     int
	maxMessageChars int

	api  API
	bot  *bot.Bot
	sink comm.InboundSink

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	deliverMu sync.Mutex
	inflight  sync.Map
}

// New validates options and builds an adapter. Start performs the network
// polling; Send works without Start (useful for one-shot CLI sinks).
func New(opts Options) (*Adapter, error) {
	if strings.TrimSpace(opts.Agent.Workspace) == "" {
		return nil, fmt.Errorf("telegram: agent workspace is required")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("telegram: store is required")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.MaxMessageChars <= 0 {
		opts.MaxMessageChars = DefaultMaxMessageChars
	}
	return &Adapter{
		agent:           opts.Agent,
		store:           opts.Store,
		token:           strings.TrimSpace(opts.Token),
		allowed:         append([]string(nil), opts.AllowedUsers...),
		now:             opts.Now,
		pollInterval:    opts.PollInterval,
		maxAttempts:     opts.MaxAttempts,
		maxMessageChars: opts.MaxMessageChars,
	}, nil
}

// Name returns the channel name.
func (a *Adapter) Name() string { return Channel }

// Start begins Telegram polling and the outbox retry loop.
func (a *Adapter) Start(ctx context.Context, sink comm.InboundSink) error {
	a.mu.Lock()
	if a.ctx != nil {
		a.mu.Unlock()
		return fmt.Errorf("telegram: adapter already started")
	}
	a.sink = sink
	a.mu.Unlock()

	b, err := bot.New(a.token, bot.WithDefaultHandler(a.handleUpdate))
	if err != nil {
		return fmt.Errorf("telegram: create bot: %w", err)
	}
	a.api = b
	a.bot = b

	a.mu.Lock()
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.done = make(chan struct{})
	a.wg.Add(3)
	a.mu.Unlock()

	go func() {
		defer a.wg.Done()
		b.Start(a.ctx)
	}()
	go func() {
		defer a.wg.Done()
		a.outboxLoop()
	}()
	go func() {
		defer a.wg.Done()
		a.replayLoop()
	}()
	go func() {
		a.wg.Wait()
		close(a.done)
	}()
	return nil
}

// Stop terminates polling and the outbox loop.
func (a *Adapter) Stop() error {
	a.mu.Lock()
	cancel := a.cancel
	done := a.done
	a.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if done == nil {
		return nil
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Printf("telegram: shutdown timed out for agent %s", a.agent.Name)
	}
	return nil
}

func (a *Adapter) outboxLoop() {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.deliverDue(a.ctx); err != nil {
				log.Printf("telegram: deliver outbox for %s: %v", a.agent.Name, err)
			}
		}
	}
}

// Send enqueues an outbound envelope and attempts one delivery immediately.
// Enqueue success is sufficient; transient delivery failures are retried.
func (a *Adapter) Send(env envelope.Envelope) error {
	if env.Direction != envelope.DirectionOut {
		return fmt.Errorf("telegram: outbound envelope has direction %q", env.Direction)
	}
	chatID, err := chatIDFor(env)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(env.Content.Text)
	if text == "" {
		return fmt.Errorf("telegram: outbound text is required")
	}

	// Hold the delivery lock across enqueue and the inline attempt so the
	// retry loop cannot deliver the new row between those steps.
	a.deliverMu.Lock()
	defer a.deliverMu.Unlock()

	item, err := a.store.CommTG().EnqueueOutbound(commItem(env, chatID, text, a.now()))
	if err != nil {
		return err
	}
	if err := a.deliverItem(context.Background(), item); err != nil {
		log.Printf("telegram: immediate delivery for %s: %v", a.agent.Name, media.RedactToken(err, a.token))
	}
	return nil
}
