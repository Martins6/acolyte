package comm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/media"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
)

// SyntheticTurnRunner admits an external event and collects the reaction.
type SyntheticTurnRunner interface {
	RunSyntheticTurn(ctx context.Context, sessionID string, ev opencode.SyntheticEvent, cfg opencode.TurnConfig) (*opencode.TurnResult, error)
}

// OutboxSink routes cron/routine user notifications and inbox events through
// the agent's durable communication outbox. It never requires a running
// adapter: a live daemon drains pending rows, and turns always run.
type OutboxSink struct {
	Stores StoreProvider
	Turn   SyntheticTurnRunner
	Now    func() time.Time
	// Activity, when non-nil, records tool activity for synthetic turns. An
	// empty Agent is filled from the agent passed to Inbox, so one sink can
	// serve every registered agent.
	Activity *ActivityRecorder
}

// DeliveryError carries the final delivery payload when an outbox enqueue
// fails, so cron can persist a retryable route.
type DeliveryError struct {
	EnvelopeID        string
	ChatID            int64
	Text              string
	OpenCodeSessionID string
	SourceMessageID   string
	Err               error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("comm: queue delivery: %v", e.Err)
	}
	return "comm: queue delivery"
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NotifyUser enqueues a user-only message to the agent's latest allowed chat.
func (s OutboxSink) NotifyUser(ctx context.Context, agent registry.Agent, text string) error {
	return s.notifyUser(ctx, agent, text, "", "")
}

// NotifyUserMedia enqueues one user-only photo. The caption is optional; the
// media path must already be validated/canonicalized by the caller. sessionID
// is attribution only and never injects a message into that session.
func (s OutboxSink) NotifyUserMedia(ctx context.Context, agent registry.Agent, text, mediaPath, sessionID string) error {
	text = strings.TrimSpace(text)
	mediaPath = strings.TrimSpace(mediaPath)
	if mediaPath == "" {
		return fmt.Errorf("comm: media path is required")
	}
	if err := media.ValidateOutboundCaption(text); err != nil {
		return err
	}
	return s.notifyUser(ctx, agent, text, mediaPath, strings.TrimSpace(sessionID))
}

func (s OutboxSink) notifyUser(_ context.Context, agent registry.Agent, text, mediaPath, sessionID string) error {
	text = strings.TrimSpace(text)
	if text == "" && mediaPath == "" {
		return fmt.Errorf("comm: notification text or media is required")
	}
	st, release, err := s.Stores.Acquire(agent.Workspace)
	if err != nil {
		return fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	chat, ok, err := st.CommTG().LatestAllowedChat()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("comm: agent %q has no private Telegram chat; message the bot first", agent.Name)
	}
	contentType := envelope.ContentText
	if mediaPath != "" {
		contentType = envelope.ContentMedia
	}
	env := envelope.New(envelope.DirectionOut, "telegram", envelope.TelegramThread(chat.ChatID),
		envelope.Party{Kind: envelope.PartyAgent, ID: agent.Name, Name: agent.Name},
		envelope.Party{Kind: envelope.PartyUser, ID: fmt.Sprintf("%d", chat.ChatID), Name: chat.Username},
		envelope.Content{Type: contentType, Text: text})
	_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID:        env.ID,
		ChatID:            chat.ChatID,
		Text:              text,
		MediaPath:         mediaPath,
		OpenCodeSessionID: sessionID,
		CreatedAt:         s.now(),
	})
	if err != nil {
		return &DeliveryError{EnvelopeID: env.ID, ChatID: chat.ChatID, Text: text, OpenCodeSessionID: sessionID, Err: err}
	}
	return nil
}

// Inbox always runs the synthetic turn. The reply is queued when a chat is
// resolvable; otherwise it is logged as undelivered and the turn still
// succeeds.
func (s OutboxSink) Inbox(ctx context.Context, agent registry.Agent, sessionID, text string, meta map[string]any) (string, string, error) {
	if s.Turn == nil {
		return "", "", fmt.Errorf("comm: synthetic turn runner is required")
	}
	st, release, err := s.Stores.Acquire(agent.Workspace)
	if err != nil {
		return "", "", fmt.Errorf("comm: open agent store: %w", err)
	}
	defer release()

	if strings.TrimSpace(sessionID) == "" {
		if session, ok, err := st.Orch().LatestSession(); err != nil {
			return "", "", err
		} else if ok {
			sessionID = session.OpenCodeSessionID
		}
	}
	cfg := opencode.TurnConfig{Workspace: agent.Workspace}
	if s.Activity != nil {
		recorder := *s.Activity
		if strings.TrimSpace(recorder.Agent) == "" {
			recorder.Agent = agent.Name
		}
		cfg.OnEvent = recorder.Handler(st)
	}
	result, err := s.Turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        text,
		Description: "Inbox event",
		Metadata:    meta,
		Delivery:    "queue",
	}, cfg)
	if err != nil {
		if result == nil {
			return "", "", err
		}
		return result.ResponseText, result.SessionID, err
	}
	if strings.TrimSpace(result.ResponseText) == "" {
		return result.ResponseText, result.SessionID, nil
	}

	chatID, ok := replyChat(st, result.SessionID)
	if !ok {
		raw, _ := json.Marshal(meta)
		_ = st.Orch().AppendLog("warn", "comm:"+agent.Name,
			"inbox reply not delivered: no Telegram chat is registered", string(raw), s.now())
		return result.ResponseText, result.SessionID, nil
	}
	sourceMessageID := ""
	if len(result.MessageIDs) > 0 {
		sourceMessageID = result.MessageIDs[len(result.MessageIDs)-1]
	}
	outboundID := envelope.NewID()
	_, err = st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID:        outboundID,
		ChatID:            chatID,
		Text:              result.ResponseText,
		OpenCodeSessionID: result.SessionID,
		SourceMessageID:   sourceMessageID,
		CreatedAt:         s.now(),
	})
	if err != nil {
		return result.ResponseText, result.SessionID, &DeliveryError{
			EnvelopeID:        outboundID,
			ChatID:            chatID,
			Text:              result.ResponseText,
			OpenCodeSessionID: result.SessionID,
			SourceMessageID:   sourceMessageID,
			Err:               err,
		}
	}
	return result.ResponseText, result.SessionID, nil
}

func (s OutboxSink) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

func replyChat(st *store.Store, sessionID string) (int64, bool) {
	if session, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID); err == nil && ok {
		if chatID, parsed := envelope.ParseTelegramThread(session.Thread); parsed && chatID != 0 {
			return chatID, true
		}
	}
	chat, ok, err := st.CommTG().LatestAllowedChat()
	if err != nil || !ok {
		return 0, false
	}
	return chat.ChatID, true
}
