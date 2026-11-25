package comm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// TurnProber is the optional recovery half of a turn runner. A runner that
// implements it can re-collect a turn that was admitted before a restart
// without waiting and without admitting it again.
type TurnProber interface {
	GetSession(ctx context.Context, sessionID string) (*opencode.SessionInfo, error)
	CollectDelivered(ctx context.Context, sessionID, markerID string) (*opencode.TurnResult, bool, error)
	InboxPending(ctx context.Context, sessionID, itemID string) (bool, error)
	Interrupt(ctx context.Context, sessionID string) error
}

// resumeGraceWindow is how long a stranded turn may sit before recovery abandons
// it. It is a variable so tests can shorten it.
var resumeGraceWindow = 5 * time.Minute

// maxResumeAttempts bounds how many times recovery re-admits one message before
// settling it with an explicit notice.
const maxResumeAttempts = 2

// ReconcileOutcome reports what recovery found for an admitted inbound.
type ReconcileOutcome int

const (
	// ReconcileStillPending means the turn is queued or running and should be
	// left alone; a later pass will look again.
	ReconcileStillPending ReconcileOutcome = iota
	// ReconcileRecovered means the turn's reply was collected and queued.
	ReconcileRecovered
	// ReconcileStranded means the input was admitted but never executed, so
	// recovery may re-admit it.
	ReconcileStranded
	// ReconcileUnrecoverable means the marker cannot produce a reply and the
	// inbound must be settled explicitly instead of retried forever.
	ReconcileUnrecoverable
	// ReconcileReAdmitted means recovery abandoned the stranded turn and reset
	// the row for a fresh admission on the next pass.
	ReconcileReAdmitted
)

// String renders the outcome for logs.
func (o ReconcileOutcome) String() string {
	switch o {
	case ReconcileRecovered:
		return "recovered"
	case ReconcileStranded:
		return "stranded"
	case ReconcileUnrecoverable:
		return "unrecoverable"
	case ReconcileReAdmitted:
		return "re-admitted"
	default:
		return "still-pending"
	}
}

// Outbound routes an outbound envelope to the agent's Communication adapter.
type Outbound func(ctx context.Context, agent registry.Agent, env envelope.Envelope) error

// EventRecorder builds the per-turn event handler for an agent. A nil factory
// leaves TurnConfig.OnEvent unset.
type EventRecorder func(agent registry.Agent, st *store.Store) opencode.EventHandler

// RouterOptions configures the inbound turn router.
type RouterOptions struct {
	Stores         StoreProvider
	Turn           TurnRunner
	Outbound       Outbound
	EventRecorder  EventRecorder
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
	events   EventRecorder
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
		events:   opts.EventRecorder,
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

	text := buildInboundPrompt(env.Content.Text, env.Content.Media)
	if text == "" {
		return fmt.Errorf("comm: inbound envelope has no text or media")
	}

	st, release, err := r.stores.Acquire(agent.Workspace)
	if err != nil {
		return fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	if name, mention, ok := parseCommand(env.Content.Text); ok && name == CommandNew && commandAddressedToBot(mention, env.Meta) {
		if _, err := st.Orch().DeleteSession(env.Thread); err != nil {
			return fmt.Errorf("comm: reset session for thread %q: %w", env.Thread, err)
		}
		if err := r.sendReply(ctx, st, agent, env, nil, NewSessionReply); err != nil {
			return err
		}
		return nil
	}

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

	cfg := opencode.TurnConfig{Workspace: agent.Workspace}
	if r.events != nil {
		cfg.OnEvent = r.events(agent, st)
	}
	// Persist the marker the moment the input is admitted. Recovery re-collects
	// by this pair instead of admitting the message a second time. The same
	// hook enqueues the durable acknowledgement, so even a turn that never
	// finishes leaves the user with a retryable outbox row instead of silence.
	cfg.OnAdmitted = func(admitted opencode.Admitted) error {
		if err := st.CommTG().MarkInboundAdmitted(env.ID, admitted.SessionID, admitted.InputID, r.now()); err != nil &&
			!errors.Is(err, commtg.ErrInboundNotFound) {
			return err
		}
		if err := r.sendAck(ctx, agent, env); err != nil {
			// The marker is already durable, so the turn is recoverable.
			// Aborting here would throw away a running turn's reply over a
			// best-effort status message, so log it and keep going.
			log.Printf("comm: enqueue acknowledgment for %s: %v", env.ID, err)
		}
		return nil
	}
	result, turnErr := r.turn.RunUserTurn(ctx, sessionID, text, cfg)
	if result != nil && result.SessionID != "" {
		if _, err := st.Orch().UpsertSession(env.Thread, result.SessionID, r.now()); err != nil && turnErr == nil {
			turnErr = err
		}
	}

	if turnErr != nil {
		if r.inboundAdmitted(st, env.ID) {
			// The marker is durable, so recovery can still collect the real
			// reply. Do not send an error on the reply identity: it would both
			// settle the row and make the outbox suppress the recovered reply.
			return turnErr
		}
		if sendErr := r.sendReply(ctx, st, agent, env, result, fmt.Sprintf("Error: %v", turnErr)); sendErr != nil {
			return fmt.Errorf("%v: %w", turnErr, sendErr)
		}
		if err := r.linkInbound(st, env, result); err != nil {
			return err
		}
		_ = st.CommTG().ClearInboundReply(env.ID)
		return turnErr
	}
	sent := false
	if result != nil && strings.TrimSpace(result.ResponseText) != "" {
		if err := r.sendReply(ctx, st, agent, env, result, result.ResponseText); err != nil {
			return err
		}
		sent = true
	}
	if err := r.linkInbound(st, env, result); err != nil {
		return err
	}
	if sent {
		_ = st.CommTG().ClearInboundReply(env.ID)
	}
	return nil
}

// Reconcile settles an inbound whose turn was admitted earlier. It is the
// recovery counterpart to HandleInbound: it never admits a new turn, it only
// looks at the marker already recorded on the inbound row.
//
// Callers must hold the inbound claim. That is what makes recovery safe: the
// row is excluded from PendingInbound while a live turn owns it, so this can
// never race the turn it is inspecting.
func (r *Router) Reconcile(ctx context.Context, agent registry.Agent, env envelope.Envelope) (ReconcileOutcome, error) {
	prober, ok := r.turn.(TurnProber)
	if !ok {
		return ReconcileUnrecoverable, fmt.Errorf("comm: turn runner does not support recovery")
	}

	st, release, err := r.stores.Acquire(agent.Workspace)
	if err != nil {
		return ReconcileStillPending, fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	item, found, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		return ReconcileStillPending, err
	}
	if !found {
		return ReconcileUnrecoverable, fmt.Errorf("comm: inbound %q not found", env.ID)
	}
	marker := strings.TrimSpace(item.OpenCodeMessageID)
	sessionID := strings.TrimSpace(item.OpenCodeSessionID)
	if marker == "" || sessionID == "" {
		// Nothing was admitted, so there is nothing to collect. The caller
		// falls back to normal admission.
		return ReconcileStillPending, nil
	}

	// OnAdmitted enqueues the acknowledgement, but a crash between persisting
	// the marker and that enqueue would leave an admitted message silent.
	// Reconcile heals that window; the stable `ack_<inbound>` identity makes
	// this a no-op once the row exists.
	if err := r.sendAck(ctx, agent, env); err != nil {
		log.Printf("comm: heal acknowledgment for %s: %v", env.ID, err)
	}

	info, err := prober.GetSession(ctx, sessionID)
	if err != nil {
		if opencode.IsPermanentError(err) {
			return r.settleProbeFailure(ctx, st, agent, env)
		}
		// A transient service error must not settle the message; look again.
		return ReconcileStillPending, err
	}
	if info.Time.InFlight() {
		if !resumeGraceElapsed(item, r.now()) {
			// The service still owns this turn. Recovery must not interfere
			// while it is inside the grace window.
			return ReconcileStillPending, nil
		}
		// The session has been busy past the grace window. Bounded recovery
		// matters more than waiting forever: interrupt it and tell the user the
		// turn did not finish instead of letting the message sit pending.
		if err := prober.Interrupt(ctx, sessionID); err != nil && !opencode.IsPermanentError(err) {
			return ReconcileStillPending, err
		}
		notice := SystemNotice("your message was still running after the recovery grace period, so it was interrupted and no reply was delivered. Send it again to retry.")
		if err := r.settleNotice(ctx, st, agent, env, notice); err != nil {
			return ReconcileUnrecoverable, err
		}
		return ReconcileUnrecoverable, nil
	}

	result, delivered, err := prober.CollectDelivered(ctx, sessionID, marker)
	if err != nil {
		if errors.Is(err, opencode.ErrTurnTruncated) {
			// The marker is older than the collection window and its output
			// cannot be bounded. Retrying will never change that, so settle
			// with an explicit notice instead of looping forever.
			notice := SystemNotice("your message was processed, but its output exceeded the collection window and could not be recovered. Send it again to retry.")
			if settleErr := r.settleNotice(ctx, st, agent, env, notice); settleErr != nil {
				return ReconcileUnrecoverable, settleErr
			}
			return ReconcileUnrecoverable, nil
		}
		if opencode.IsPermanentError(err) {
			return r.settleProbeFailure(ctx, st, agent, env)
		}
		return ReconcileStillPending, err
	}
	if delivered {
		if result == nil || strings.TrimSpace(result.ResponseText) == "" {
			// A finished turn with no text cannot be delivered as a reply.
			// Settle it with a notice instead of marking it processed silently.
			notice := SystemNotice("your message was processed, but the turn finished without a reply. Send it again to retry.")
			if err := r.settleNotice(ctx, st, agent, env, notice); err != nil {
				return ReconcileUnrecoverable, err
			}
			return ReconcileUnrecoverable, nil
		}
		if err := r.sendReply(ctx, st, agent, env, result, result.ResponseText); err != nil {
			return ReconcileRecovered, err
		}
		_ = st.CommTG().ClearInboundReply(env.ID)
		return ReconcileRecovered, nil
	}

	// Idle with no reply. Either the input is still queued (it never executed)
	// or it was consumed and produced nothing.
	pending, err := prober.InboxPending(ctx, sessionID, marker)
	if err != nil {
		if opencode.IsPermanentError(err) {
			return r.settleProbeFailure(ctx, st, agent, env)
		}
		return ReconcileStillPending, err
	}
	if pending {
		return ReconcileStranded, nil
	}
	// Consumed with nothing to collect: terminal. Settle it with an explicit
	// notice so replay cannot retry it forever.
	notice := SystemNotice("your message was processed, but no reply could be recovered. Send it again to retry.")
	if err := r.settleNotice(ctx, st, agent, env, notice); err != nil {
		return ReconcileUnrecoverable, err
	}
	return ReconcileUnrecoverable, nil
}

// Recover applies the escalation policy on top of Reconcile. A stranded turn is
// left alone until the grace window passes; then it is abandoned, and the
// message is re-admitted into a fresh session. When the attempt budget runs out
// the message is settled with an explicit notice instead of retrying forever.
func (r *Router) Recover(ctx context.Context, agent registry.Agent, env envelope.Envelope) (ReconcileOutcome, error) {
	outcome, err := r.Reconcile(ctx, agent, env)
	if err != nil || outcome != ReconcileStranded {
		return outcome, err
	}

	st, release, err := r.stores.Acquire(agent.Workspace)
	if err != nil {
		return ReconcileStillPending, fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	item, found, err := st.CommTG().GetInboxByEnvelope(env.ID)
	if err != nil {
		return ReconcileStillPending, err
	}
	if !found {
		return ReconcileStillPending, nil
	}
	if !resumeGraceElapsed(item, r.now()) {
		return ReconcileStillPending, nil
	}
	if strings.TrimSpace(item.PendingEnvelope) == "" || item.ResumeAttempts >= maxResumeAttempts {
		// Either the payload needed to re-admit is gone, or the budget is spent.
		// Retrying is pointless, so settle the message honestly instead of
		// dropping it silently.
		if err := r.settleUnrecoverable(ctx, st, agent, env, item); err != nil {
			return ReconcileUnrecoverable, err
		}
		return ReconcileUnrecoverable, nil
	}

	// Abandon the stranded turn: settle the wedged session, forget the thread's
	// session mapping, and clear the marker so the next pass admits the message
	// into a fresh session exactly like a new one. Nothing ran, so nothing that
	// follows is duplicated.
	if prober, ok := r.turn.(TurnProber); ok && strings.TrimSpace(item.OpenCodeSessionID) != "" {
		if err := prober.Interrupt(ctx, item.OpenCodeSessionID); err != nil {
			return ReconcileStillPending, err
		}
	}
	if _, err := st.Orch().DeleteSession(env.Thread); err != nil {
		return ReconcileStillPending, err
	}
	if err := st.CommTG().RecordResumeAttempt(env.ID, "abandoned a turn that never executed"); err != nil {
		return ReconcileStillPending, err
	}
	return ReconcileReAdmitted, nil
}

// resumeGraceElapsed reports whether a stranded turn has waited long enough to
// be considered dead rather than slow.
func resumeGraceElapsed(item commtg.InboxItem, now time.Time) bool {
	start := item.ReceivedAt
	if item.AdmittedAt != nil {
		start = *item.AdmittedAt
	}
	return now.Sub(start) >= resumeGraceWindow
}

// settleProbeFailure turns a permanent probe failure into a terminal notice.
// The session or marker is gone, so retrying can never produce a reply.
func (r *Router) settleProbeFailure(ctx context.Context, st *store.Store, agent registry.Agent, env envelope.Envelope) (ReconcileOutcome, error) {
	notice := SystemNotice("your message could not be recovered because its session can no longer be read. Send it again to retry.")
	if err := r.settleNotice(ctx, st, agent, env, notice); err != nil {
		return ReconcileUnrecoverable, err
	}
	return ReconcileUnrecoverable, nil
}

// settleNotice sends a terminal operator notice and marks the inbound
// processed, so replay cannot retry a turn that can never produce a reply. The
// notice identity (sys_<inbound>) is stable, so a failed mark can safely be
// retried without duplicating the message.
func (r *Router) settleNotice(ctx context.Context, st *store.Store, agent registry.Agent, env envelope.Envelope, notice string) error {
	if err := r.sendSystemNotice(ctx, st, agent, env, notice); err != nil {
		return err
	}
	return st.CommTG().MarkInboundProcessedUnconditional(env.ID, r.now())
}

// settleUnrecoverable stops a message from looping forever and tells the user
// the truth: the request was not completed and nothing was delivered for it.
func (r *Router) settleUnrecoverable(ctx context.Context, st *store.Store, agent registry.Agent, env envelope.Envelope, item commtg.InboxItem) error {
	notice := SystemNotice(fmt.Sprintf(
		"your message from %s could not be completed after %d recovery attempts, and no reply was delivered. Send it again to retry.",
		item.ReceivedAt.UTC().Format("15:04 UTC"), item.ResumeAttempts))
	return r.settleNotice(ctx, st, agent, env, notice)
}

// acknowledgementText is the durable admission acknowledgement. It states only
// that work started: the reply, an error, or a later operator notice reports
// what actually happened.
const acknowledgementText = "working on your message."

// sendAck enqueues the user-facing acknowledgement for an admitted inbound. The
// ID is stable (`ack_<inbound>`) and the outbox is unique on it, so a repeated
// admission (for example a recovery retry) collapses to the same row instead of
// sending a second ack. It never uses the reply identity, so it cannot suppress
// or replace the final reply.
func (r *Router) sendAck(ctx context.Context, agent registry.Agent, in envelope.Envelope) error {
	if r.outbound == nil {
		return nil
	}
	out := envelope.New(envelope.DirectionOut, in.Channel, in.Thread, in.To, in.From,
		envelope.Content{Type: envelope.ContentText, Text: SystemNotice(acknowledgementText)})
	if id := ackEnvelopeID(in.ID); id != "" {
		out.ID = id
	}
	out.Meta = map[string]any{
		"agent":              agent.Name,
		"source_envelope_id": in.ID,
		"kind":               "ack",
	}
	if in.Meta != nil {
		if value, ok := in.Meta["telegram_message_id"]; ok {
			out.Meta["reply_to_message_id"] = value
		}
	}
	return r.outbound(ctx, agent, out)
}

// sendSystemNotice queues an operator notice on its own outbound identity. It
// never reuses the reply identity, so a notice can never be mistaken for, or
// collide with, recovered agent text.
func (r *Router) sendSystemNotice(ctx context.Context, st *store.Store, agent registry.Agent, in envelope.Envelope, text string) error {
	if r.outbound == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	out := envelope.New(envelope.DirectionOut, in.Channel, in.Thread, in.To, in.From,
		envelope.Content{Type: envelope.ContentText, Text: text})
	out.ID = "sys_" + in.ID
	out.Meta = map[string]any{
		"agent":              agent.Name,
		"source_envelope_id": in.ID,
		"kind":               "system-notice",
	}
	if in.Meta != nil {
		if value, ok := in.Meta["telegram_message_id"]; ok {
			out.Meta["reply_to_message_id"] = value
		}
	}
	pending := PendingReply{
		ID:      out.ID,
		Text:    text,
		ReplyTo: routerMetaInt64(out.Meta["reply_to_message_id"]),
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := st.CommTG().SetInboundReply(in.ID, string(raw)); err != nil &&
		!errors.Is(err, commtg.ErrInboundNotFound) {
		return err
	}
	return r.outbound(ctx, agent, out)
}

const mediaPathLinePrefix = "File located at: "

// buildInboundPrompt combines envelope text with resolved media paths. A path
// already present as an exact marker line (for example the richer prompt built
// by the Telegram adapter) is not repeated.
func buildInboundPrompt(text string, media []string) string {
	text = strings.TrimSpace(text)
	for _, path := range media {
		if path == "" || hasMediaPathLine(text, path) {
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += mediaPathLinePrefix + path
	}
	return text
}

// hasMediaPathLine reports whether text contains an exact
// "File located at: <path>" line for path.
func hasMediaPathLine(text, path string) bool {
	if path == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), mediaPathLinePrefix); ok && rest == path {
			return true
		}
	}
	return false
}

// inboundAdmitted reports whether the inbound already carries a durable turn
// marker. Recovery can re-collect such a turn, so it must not be settled by the
// admission error path.
func (r *Router) inboundAdmitted(st *store.Store, envelopeID string) bool {
	item, found, err := st.CommTG().GetInboxByEnvelope(envelopeID)
	if err != nil || !found {
		return false
	}
	return strings.TrimSpace(item.OpenCodeMessageID) != "" && strings.TrimSpace(item.OpenCodeSessionID) != ""
}

func (r *Router) linkInbound(st *store.Store, env envelope.Envelope, result *opencode.TurnResult) error {
	if result == nil || result.InputID == "" {
		return nil
	}
	if err := st.CommTG().LinkInbound(env.ID, result.SessionID, result.InputID); err != nil &&
		!errors.Is(err, commtg.ErrInboundNotFound) {
		return err
	}
	return nil
}

func (r *Router) sendReply(ctx context.Context, st *store.Store, agent registry.Agent, in envelope.Envelope, result *opencode.TurnResult, text string) error {
	if r.outbound == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	out := envelope.New(envelope.DirectionOut, in.Channel, in.Thread, in.To, in.From,
		envelope.Content{Type: envelope.ContentText, Text: text})
	// Derive a stable ID from the inbound envelope. The outbox is unique on
	// envelope_id, so repeating or racing a reply for one inbound collapses to
	// a single row instead of double-sending.
	if id := replyEnvelopeID(in.ID); id != "" {
		out.ID = id
	}
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
		InputID:           routerResultInputID(result),
		SourceMessageID:   routerMetaString(out.Meta["source_message_id"]),
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := st.CommTG().SetInboundReply(in.ID, string(raw)); err != nil &&
		!errors.Is(err, commtg.ErrInboundNotFound) {
		return err
	}
	return r.outbound(ctx, agent, out)
}

// replyEnvelopeID returns the stable outbox identity for the reply to an
// inbound envelope. An empty inbound ID yields no override so a malformed
// inbound cannot make unrelated replies collide.
func replyEnvelopeID(inboundID string) string {
	inboundID = strings.TrimSpace(inboundID)
	if inboundID == "" {
		return ""
	}
	return "out_" + inboundID
}

// ackEnvelopeID returns the stable outbox identity for the admission
// acknowledgement. It is deliberately distinct from the reply identity so an
// ack can never occupy the slot the final reply needs.
func ackEnvelopeID(inboundID string) string {
	inboundID = strings.TrimSpace(inboundID)
	if inboundID == "" {
		return ""
	}
	return "ack_" + inboundID
}

func routerResultInputID(result *opencode.TurnResult) string {
	if result == nil {
		return ""
	}
	return result.InputID
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
