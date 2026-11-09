// Package comm defines the Communication ⇄ Orchestrator contract and the
// turn router that admits inbound envelopes into OpenCode sessions.
package comm

import (
	"context"

	"github.com/martins6/acolyte/internal/envelope"
)

// InboundSink is called by a Communication adapter for each parsed inbound
// envelope.
type InboundSink func(envelope.Envelope)

// Communication is one channel adapter.
type Communication interface {
	Name() string
	Start(ctx context.Context, sink InboundSink) error
	Send(envelope.Envelope) error
	Stop() error
}
