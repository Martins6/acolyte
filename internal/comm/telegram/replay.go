package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/store/commtg"
)

type replayPayload struct {
	Envelope envelope.Envelope `json:"envelope"`
	Update   *models.Update    `json:"update,omitempty"`
}

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
	item, ok, getErr := a.store.CommTG().GetInboxByEnvelope(env.ID)
	if getErr != nil {
		_ = a.store.CommTG().RecordInboundFailure(env.ID, token, getErr.Error())
		return
	}
	if ok && item.OpenCodeMessageID != "" {
		if item.PendingReply != "" {
			if deliverErr := a.deliverPendingReply(item); deliverErr != nil {
				_ = a.store.CommTG().RecordInboundFailure(env.ID, token, deliverErr.Error())
				return
			}
		}
		if item.PendingReply == "" && a.reconciler() != nil {
			// A marker appeared after this pass decided to admit, so the reply
			// belongs to recovery. Hand the row back instead of settling it
			// without collecting that reply.
			_ = a.store.CommTG().ReleaseInboundClaim(env.ID, token)
			return
		}
		if markErr := a.store.CommTG().MarkInboundProcessed(env.ID, token, a.now()); markErr != nil {
			log.Printf("telegram: mark linked inbound %s processed for %s: %v", env.ID, a.agent.Name, markErr)
		}
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
	// collection failed. Recover any pending reply first.
	if item, ok, getErr := a.store.CommTG().GetInboxByEnvelope(env.ID); getErr == nil && ok && item.OpenCodeMessageID != "" {
		if item.PendingReply != "" {
			if deliverErr := a.deliverPendingReply(item); deliverErr != nil {
				if recordErr := a.store.CommTG().RecordInboundFailure(env.ID, token, deliverErr.Error()); recordErr != nil {
					log.Printf("telegram: record inbound failure %s for %s: %v", env.ID, a.agent.Name, recordErr)
				}
				return
			}
		}
		if markErr := a.store.CommTG().MarkInboundProcessed(env.ID, token, a.now()); markErr != nil {
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
		if item.PendingReply != "" {
			token := envelope.NewID()
			now := a.now()
			claimed, err := a.store.CommTG().ClaimInbound(item.EnvelopeID, token, now, now.Add(claimLease))
			if err != nil || !claimed {
				continue
			}
			if deliverErr := a.deliverPendingReply(item); deliverErr != nil {
				_ = a.store.CommTG().RecordInboundFailure(item.EnvelopeID, token, deliverErr.Error())
				continue
			}
			_ = a.store.CommTG().MarkInboundProcessed(item.EnvelopeID, token, a.now())
			continue
		}
		if item.OpenCodeMessageID != "" {
			a.reconcileItem(ctx, item)
			continue
		}
		if strings.TrimSpace(item.PendingEnvelope) == "" {
			// Pre-replay rows have no payload and were already admitted.
			_ = a.store.CommTG().MarkInboundProcessedUnconditional(item.EnvelopeID, a.now())
			continue
		}
		var env envelope.Envelope
		var payload replayPayload
		if err := json.Unmarshal([]byte(item.PendingEnvelope), &payload); err == nil && payload.Envelope.ID != "" {
			if payload.Update != nil && payload.Update.Message != nil {
				a.processAndAdmit(ctx, payload.Envelope, payload.Update.Message)
			} else {
				a.admit(ctx, payload.Envelope)
			}
			continue
		}
		if err := json.Unmarshal([]byte(item.PendingEnvelope), &env); err != nil {
			_ = a.store.CommTG().RecordInboundFailureUnclaimed(item.EnvelopeID, "unmarshal pending envelope: "+err.Error())
			continue
		}
		a.admit(ctx, env)
	}
}

// reconcileItem settles an inbound whose turn was already admitted. The marker
// on the row is the recovery token: a finished turn is re-collected and its
// reply queued, a running turn is left alone, and a turn that never executed is
// reported as stranded so escalation can decide what to do.
func (a *Adapter) reconcileItem(ctx context.Context, item commtg.InboxItem) {
	reconcile := a.reconciler()

	if reconcile == nil || strings.TrimSpace(item.PendingEnvelope) == "" {
		// No recovery wiring, or a legacy row with no payload: there is no
		// envelope to reply to, so settle it exactly as before.
		_ = a.store.CommTG().MarkInboundProcessedUnconditional(item.EnvelopeID, a.now())
		return
	}

	env, ok := decodeReplayEnvelope(item.PendingEnvelope)
	if !ok {
		_ = a.store.CommTG().RecordInboundFailureUnclaimed(item.EnvelopeID, "decode pending envelope for recovery")
		return
	}

	token := envelope.NewID()
	now := a.now()
	claimed, err := a.store.CommTG().ClaimInbound(item.EnvelopeID, token, now, now.Add(claimLease))
	if err != nil || !claimed {
		return
	}

	outcome, err := reconcile(ctx, env)
	if err != nil {
		_ = a.store.CommTG().RecordInboundFailure(item.EnvelopeID, token, err.Error())
		return
	}
	if outcome == comm.ReconcileRecovered {
		_ = a.store.CommTG().MarkInboundProcessed(item.EnvelopeID, token, a.now())
		return
	}
	// Still pending, stranded, or unrecoverable: hand the row straight back so
	// the next pass can look again. Counting these as failures would inflate the
	// admission counter for work that has not failed to be admitted.
	_ = a.store.CommTG().ReleaseInboundClaim(item.EnvelopeID, token)
}

// reconciler returns the installed recovery hook, if any.
func (a *Adapter) reconciler() comm.Reconciler {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reconcile
}

// decodeReplayEnvelope accepts both stored payload shapes: the replayPayload
// wrapper written since v4, and the bare envelope written by earlier versions.
func decodeReplayEnvelope(raw string) (envelope.Envelope, bool) {
	var payload replayPayload
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.Envelope.ID != "" {
		return payload.Envelope, true
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err == nil && env.ID != "" {
		return env, true
	}
	return envelope.Envelope{}, false
}

func (a *Adapter) deliverPendingReply(item commtg.InboxItem) error {
	var reply comm.PendingReply
	if err := json.Unmarshal([]byte(item.PendingReply), &reply); err != nil {
		return err
	}
	if reply.InputID != "" {
		if err := a.store.CommTG().LinkInbound(item.EnvelopeID, reply.OpenCodeSessionID, reply.InputID); err != nil &&
			!errors.Is(err, commtg.ErrInboundNotFound) {
			return err
		}
	}
	if strings.TrimSpace(reply.Text) == "" {
		return nil
	}
	_, err := a.store.CommTG().EnqueueOutbound(commtg.OutboxItem{
		ID:                reply.ID,
		EnvelopeID:        reply.ID,
		ChatID:            item.ChatID,
		Text:              reply.Text,
		ReplyTo:           reply.ReplyTo,
		OpenCodeSessionID: reply.OpenCodeSessionID,
		SourceMessageID:   reply.SourceMessageID,
		CreatedAt:         a.now(),
	})
	return err
}
