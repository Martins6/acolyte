package comm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
)

// StoreProvider opens a per-agent store with a release function.
type StoreProvider interface {
	Acquire(workspace string) (*store.Store, func(), error)
}

// TurnRunner drives one real user turn.
type TurnRunner interface {
	RunUserTurn(ctx context.Context, sessionID, text string, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
}

// Outbound routes an outbound envelope to the agent's Communication adapter.
type Outbound func(ctx context.Context, agent registry.Agent, env envelope.Envelope) error

// RouterOptions configures the inbound turn router.
type RouterOptions struct {
	Stores         StoreProvider
	Turn           TurnRunner
	Outbound       Outbound
	Now            func() time.Time
	MaxConcurrency int
}

// Router admits inbound envelopes into sessions and sends replies through the
// agent's Communication adapter. Turns always run and persist even when the
// outbound channel is unavailable.
type Router struct {
	stores   StoreProvider
	turn     TurnRunner
	outbound Outbound
	now      func() time.Time
	sem      chan struct{}
	locks    sync.Map
}

// NewRouter validates and builds a router.
func NewRouter(opts RouterOptions) (*Router, error) {
	if opts.Stores == nil {
		return nil, fmt.Errorf("comm: store provider is required")
	}
	if opts.Turn == nil {
		return nil, fmt.Errorf("comm: turn runner is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 4
	}
	return &Router{
		stores:   opts.Stores,
		turn:     opts.Turn,
		outbound: opts.Outbound,
		now:      opts.Now,
		sem:      make(chan struct{}, opts.MaxConcurrency),
	}, nil
}

// HandleInbound runs one inbound envelope as a user turn and routes the reply.
func (r *Router) HandleInbound(ctx context.Context, agent registry.Agent, env envelope.Envelope) error {
	if err := env.Validate(); err != nil {
		return err
	}
	if env.Direction != envelope.DirectionIn {
		return fmt.Errorf("comm: inbound envelope has direction %q", env.Direction)
	}
	if strings.TrimSpace(agent.Workspace) == "" {
		return fmt.Errorf("comm: agent workspace is required")
	}

	lock := r.threadLock(env.Thread)
	lock.Lock()
	defer lock.Unlock()

	text := strings.TrimSpace(env.Content.Text)
	if text == "" && len(env.Content.Media) > 0 {
		var b strings.Builder
		for _, path := range env.Content.Media {
			b.WriteString("File located at: ")
			b.WriteString(path)
			b.WriteByte('\n')
		}
		text = strings.TrimSpace(b.String())
	}
	if text == "" {
		return fmt.Errorf("comm: inbound envelope has no text or media")
	}

	st, release, err := r.stores.Acquire(agent.Workspace)
	if err != nil {
		return fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	sessionID := ""
	if session, ok, err := st.Orch().GetSessionByThread(env.Thread); err != nil {
		return err
	} else if ok {
		sessionID = session.OpenCodeSessionID
	}

	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.sem }()

	result, turnErr := r.turn.RunUserTurn(ctx, sessionID, text, opencode.TurnConfig{Workspace: agent.Workspace})
	if result != nil {
		if result.SessionID != "" {
			if _, err := st.Orch().UpsertSession(env.Thread, result.SessionID, r.now()); err != nil && turnErr == nil {
				turnErr = err
			}
		}
		if result.InputID != "" {
			if err := st.CommTG().LinkInbound(env.ID, result.SessionID, result.InputID); err != nil &&
				!errors.Is(err, commtg.ErrInboundNotFound) && turnErr == nil {
				turnErr = err
			}
		}
	}

	if turnErr != nil {
		r.sendOutbound(ctx, agent, env, result, fmt.Sprintf("Error: %v", turnErr))
		return turnErr
	}
	if result != nil && strings.TrimSpace(result.ResponseText) != "" {
		r.sendOutbound(ctx, agent, env, result, result.ResponseText)
	}
	return nil
}

func (r *Router) sendOutbound(ctx context.Context, agent registry.Agent, in envelope.Envelope, result *opencode.TurnResult, text string) {
	if r.outbound == nil || strings.TrimSpace(text) == "" {
		return
	}
	out := envelope.New(envelope.DirectionOut, in.Channel, in.Thread, in.To, in.From,
		envelope.Content{Type: envelope.ContentText, Text: text})
	out.Meta = map[string]any{"agent": agent.Name, "source_envelope_id": in.ID}
	if result != nil {
		if result.SessionID != "" {
			out.Meta["opencode_session_id"] = result.SessionID
		}
		if len(result.MessageIDs) > 0 {
			out.Meta["source_message_id"] = result.MessageIDs[len(result.MessageIDs)-1]
		}
	}
	if in.Meta != nil {
		if value, ok := in.Meta["telegram_message_id"]; ok {
			out.Meta["reply_to_message_id"] = value
		}
	}
	if err := r.outbound(ctx, agent, out); err != nil {
		log.Printf("comm: route outbound for %s: %v", agent.Name, err)
	}
}

func (r *Router) threadLock(thread string) *sync.Mutex {
	value, _ := r.locks.LoadOrStore(thread, &sync.Mutex{})
	return value.(*sync.Mutex)
}
