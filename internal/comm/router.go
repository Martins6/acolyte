package comm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

	lockMu sync.Mutex
	locks  map[string]*threadLock
}

type threadLock struct {
	mu   sync.Mutex
	refs int
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
		locks:    map[string]*threadLock{},
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

	unlock := r.lockThread(env.Thread)
	defer unlock()

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
		if sendErr := r.sendReply(ctx, st, agent, env, result, fmt.Sprintf("Error: %v", turnErr)); sendErr != nil {
			return fmt.Errorf("%v: %w", turnErr, sendErr)
		}
		return turnErr
	}
	if result != nil && strings.TrimSpace(result.ResponseText) != "" {
		if err := r.sendReply(ctx, st, agent, env, result, result.ResponseText); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) sendReply(ctx context.Context, st *store.Store, agent registry.Agent, in envelope.Envelope, result *opencode.TurnResult, text string) error {
	if r.outbound == nil || strings.TrimSpace(text) == "" {
		return nil
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

	pending := PendingReply{
		ID:                out.ID,
		Text:              text,
		ReplyTo:           routerMetaInt64(out.Meta["reply_to_message_id"]),
		OpenCodeSessionID: routerMetaString(out.Meta["opencode_session_id"]),
		SourceMessageID:   routerMetaString(out.Meta["source_message_id"]),
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := st.CommTG().SetInboundReply(in.ID, string(raw)); err != nil {
		if !errors.Is(err, commtg.ErrInboundNotFound) {
			return err
		}
	}
	if err := r.outbound(ctx, agent, out); err != nil {
		return err
	}
	_ = st.CommTG().ClearInboundReply(in.ID)
	return nil
}

func routerMetaInt64(value any) *int64 {
	switch typed := value.(type) {
	case int:
		out := int64(typed)
		return &out
	case int64:
		out := typed
		return &out
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return nil
		}
		out := int64(typed)
		return &out
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil
		}
		return &parsed
	default:
		return nil
	}
}

func routerMetaString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func (r *Router) lockThread(thread string) func() {
	r.lockMu.Lock()
	entry := r.locks[thread]
	if entry == nil {
		entry = &threadLock{}
		r.locks[thread] = entry
	}
	entry.refs++
	r.lockMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.lockMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(r.locks, thread)
		}
		r.lockMu.Unlock()
	}
}
