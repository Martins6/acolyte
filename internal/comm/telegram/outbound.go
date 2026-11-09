package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/store/commtg"
)

func (a *Adapter) deliverDue(ctx context.Context) error {
	a.deliverMu.Lock()
	defer a.deliverMu.Unlock()

	items, err := a.store.CommTG().DueOutbound(a.now(), 20)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := a.deliverItem(ctx, item); err != nil {
			log.Printf("telegram: deliver %s for %s: %v", item.ID, a.agent.Name, err)
		}
	}
	return nil
}

func (a *Adapter) deliverItem(ctx context.Context, item commtg.OutboxItem) error {
	chunks := splitText(item.Text, a.maxMessageChars)
	if len(chunks) == 0 {
		return a.store.CommTG().MarkOutboundFailed(item.ID, "empty outbound text")
	}
	if a.api == nil {
		return nil
	}

	lastMessageID := int64(0)
	if item.TGMessageID != nil {
		lastMessageID = *item.TGMessageID
	}
	for index := item.ChunksSent; index < len(chunks); index++ {
		params := &bot.SendMessageParams{ChatID: item.ChatID, Text: chunks[index]}
		if index == 0 && item.ReplyTo != nil {
			params.ReplyParameters = &models.ReplyParameters{
				MessageID:                int(*item.ReplyTo),
				AllowSendingWithoutReply: true,
			}
		}
		message, err := a.api.SendMessage(ctx, params)
		if err != nil {
			return a.recordDeliveryFailure(item, err)
		}
		if message != nil {
			lastMessageID = int64(message.ID)
		}
		if err := a.store.CommTG().MarkOutboundProgress(item.ID, index+1, lastMessageID); err != nil {
			return err
		}
	}
	return a.store.CommTG().MarkOutboundSent(item.ID, len(chunks), lastMessageID, a.now())
}

func (a *Adapter) recordDeliveryFailure(item commtg.OutboxItem, sendErr error) error {
	attempts := item.Attempts + 1
	if attempts >= a.maxAttempts {
		if err := a.store.CommTG().MarkOutboundFailed(item.ID, sendErr.Error()); err != nil {
			return err
		}
		return sendErr
	}
	next := a.now().Add(backoff(attempts))
	if err := a.store.CommTG().MarkOutboundRetry(item.ID, attempts, next, sendErr.Error()); err != nil {
		return err
	}
	return sendErr
}

func backoff(attempts int) time.Duration {
	delay := 5 * time.Second
	for index := 1; index < attempts; index++ {
		delay *= 3
		if delay >= 15*time.Minute {
			return 15 * time.Minute
		}
	}
	return delay
}

func splitText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = DefaultMaxMessageChars
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}

	var chunks []string
	for len(runes) > limit {
		cut := limit
		for index := limit; index > 0; index-- {
			if unicode.IsSpace(runes[index]) {
				cut = index
				break
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

func chatIDFor(env envelope.Envelope) (int64, error) {
	if chatID, ok := envelope.ParseTelegramThread(env.Thread); ok && chatID != 0 {
		return chatID, nil
	}
	if chatID, err := strconv.ParseInt(strings.TrimSpace(env.To.ID), 10, 64); err == nil && chatID != 0 {
		return chatID, nil
	}
	if env.Meta != nil {
		if chatID := metaInt64(env.Meta["telegram_chat_id"]); chatID != nil && *chatID != 0 {
			return *chatID, nil
		}
	}
	return 0, fmt.Errorf("telegram: outbound envelope has no chat id")
}

func commItem(env envelope.Envelope, chatID int64, text string, now time.Time) commtg.OutboxItem {
	return commtg.OutboxItem{
		EnvelopeID:        env.ID,
		ChatID:            chatID,
		Text:              text,
		ReplyTo:           metaInt64(env.Meta["reply_to_message_id"]),
		OpenCodeSessionID: metaString(env.Meta["opencode_session_id"]),
		SourceMessageID:   metaString(env.Meta["source_message_id"]),
		CreatedAt:         now,
	}
}

func metaInt64(value any) *int64 {
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
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		return &parsed
	default:
		return nil
	}
}

func metaString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}
