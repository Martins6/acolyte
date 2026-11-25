// Package comm defines the Communication ⇄ Orchestrator contract and the
// turn router that admits inbound envelopes into OpenCode sessions.
package comm

import (
	"context"
	"strings"

	"github.com/martins6/acolyte/internal/envelope"
)

// InboundSink is called by a Communication adapter for each parsed inbound
// envelope. Returning an error leaves the inbound row pending for replay.
type InboundSink func(ctx context.Context, env envelope.Envelope) error

// Communication is one channel adapter.
type Communication interface {
	Name() string
	Start(ctx context.Context, sink InboundSink) error
	Send(envelope.Envelope) error
	Stop() error
}

// Reconciler settles an inbound whose turn was admitted before a restart. It
// never admits a new turn; it inspects the marker already recorded.
type Reconciler func(ctx context.Context, env envelope.Envelope) (ReconcileOutcome, error)

// ReconcilerSetter is implemented by channel adapters that can route admitted
// turns through the reconciler.
type ReconcilerSetter interface {
	SetReconciler(Reconciler)
}

// SystemNoticeMarker prefixes text that Acolyte generated about its own
// operation, so a reader can always tell it apart from what the agent said.
const SystemNoticeMarker = "ACOLYTE SYSTEM MESSAGE:"

// SystemNotice formats an operator-facing notice. Callers must keep the wording
// factual: a retry is reported as a retry, never as a completed request.
func SystemNotice(text string) string {
	return SystemNoticeMarker + " " + strings.TrimSpace(text)
}

// PendingReply is the durable reply payload stored on an inbound row when
// outbox enqueue fails. Replay uses the same ID, making enqueue idempotent.
type PendingReply struct {
	ID                string `json:"id"`
	Text              string `json:"text"`
	ReplyTo           *int64 `json:"reply_to,omitempty"`
	OpenCodeSessionID string `json:"opencode_session_id,omitempty"`
	InputID           string `json:"input_id,omitempty"`
	SourceMessageID   string `json:"source_message_id,omitempty"`
}
