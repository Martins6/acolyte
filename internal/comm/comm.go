// Package comm defines the Communication ⇄ Orchestrator contract and the
// turn router that admits inbound envelopes into OpenCode sessions.
package comm

import (
	"context"

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
