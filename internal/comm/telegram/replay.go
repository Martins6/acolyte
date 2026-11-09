package telegram

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
)

const (
	claimLease     = 2 * time.Minute
	claimHeartbeat = 30 * time.Second
)

func (a *Adapter) admit(ctx context.Context, env envelope.Envelope) {
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return
	}
	if item, ok, err := a.store.CommTG().GetInboxByEnvelope(env.ID); err == nil && ok && item.OpenCodeMessageID != "" {
		_ = a.store.CommTG().MarkInboundProcessedUnconditional(env.ID, a.now())
		return
	}
	if _, busy := a.inflight.LoadOrStore(env.ID, struct{}{}); busy {
		return
	}
	defer a.inflight.Delete(env.ID)

	token := envelope.NewID()
	now := a.now()
	claimed, err := a.store.CommTG().ClaimInbound(env.ID, token, now, now.Add(claimLease))
	if err != nil || !claimed {
		return
	}

	admitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(claimHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-admitCtx.Done():
				return
			case <-ticker.C:
				ok, err := a.store.CommTG().RenewInboundClaim(env.ID, token, a.now().Add(claimLease))
				if err != nil || !ok {
					// The lease is no longer ours; stop this admission so it
					// cannot race a takeover.
					cancel()
					return
				}
			}
		}
	}()

	sinkErr := sink(admitCtx, env)
	cancel()
	<-heartbeatDone

	if sinkErr == nil {
		if markErr := a.store.CommTG().MarkInboundProcessed(env.ID, token, a.now()); markErr != nil {
			log.Printf("telegram: mark inbound %s processed for %s: %v", env.ID, a.agent.Name, markErr)
		}
		return
	}

	// If the router already admitted the input, the turn exists even though
	// collection failed. Mark it processed instead of replaying it.
	if item, ok, getErr := a.store.CommTG().GetInboxByEnvelope(env.ID); getErr == nil && ok && item.OpenCodeMessageID != "" {
		if markErr := a.store.CommTG().MarkInboundProcessedUnconditional(env.ID, a.now()); markErr != nil {
			log.Printf("telegram: mark linked inbound %s processed for %s: %v", env.ID, a.agent.Name, markErr)
		}
		return
	}
	if recordErr := a.store.CommTG().RecordInboundFailure(env.ID, token, sinkErr.Error()); recordErr != nil {
		log.Printf("telegram: record inbound failure %s for %s: %v", env.ID, a.agent.Name, recordErr)
	}
}

func (a *Adapter) replayLoop() {
	a.replayPending(a.ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.replayPending(a.ctx)
		}
	}
}

func (a *Adapter) replayPending(ctx context.Context) {
	items, err := a.store.CommTG().PendingInbound(a.now(), 20)
	if err != nil {
		log.Printf("telegram: list pending inbound for %s: %v", a.agent.Name, err)
		return
	}
	for _, item := range items {
		if _, busy := a.inflight.Load(item.EnvelopeID); busy {
			continue
		}
		if item.OpenCodeMessageID != "" {
			_ = a.store.CommTG().MarkInboundProcessedUnconditional(item.EnvelopeID, a.now())
			continue
		}
		if strings.TrimSpace(item.PendingEnvelope) == "" {
			// Pre-replay rows have no payload and were already admitted.
			_ = a.store.CommTG().MarkInboundProcessedUnconditional(item.EnvelopeID, a.now())
			continue
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(item.PendingEnvelope), &env); err != nil {
			_ = a.store.CommTG().RecordInboundFailure(item.EnvelopeID, "", "unmarshal pending envelope: "+err.Error())
			continue
		}
		a.admit(ctx, env)
	}
}
