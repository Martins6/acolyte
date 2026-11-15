package telegram

import (
	"context"
	"log"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/media"
)

// This file holds the inbound UX feedback helpers: the acknowledgment
// reaction on an accepted message and the typing indicator shown while the
// turn runs. Both are best-effort and must never affect admission.

// acknowledge adds AckReaction to the user's message. It runs in the
// background so a slow Telegram call cannot delay admission, and failures are
// only logged.
func (a *Adapter) acknowledge(ctx context.Context, chatID int64, messageID int) {
	if a.api == nil || messageID == 0 {
		return
	}
	api := a.api
	token := a.token
	agent := a.agent.Name
	go func() {
		reactionCtx, cancel := context.WithTimeout(ctx, ackTimeout)
		defer cancel()
		params := &bot.SetMessageReactionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Reaction: []models.ReactionType{{
				Type:              models.ReactionTypeTypeEmoji,
				ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: AckReaction},
			}},
		}
		if _, err := api.SetMessageReaction(reactionCtx, params); err != nil {
			log.Printf("telegram: acknowledge message %d in chat %d for %s: %v",
				messageID, chatID, agent, media.RedactToken(err, token))
		}
	}()
}

// startTyping shows the typing chat action immediately and refreshes it until
// the returned stop function is called. Telegram expires chat actions after
// about five seconds, so the loop refreshes every TypingRefreshInterval.
func (a *Adapter) startTyping(ctx context.Context, chatID int64) func() {
	api := a.api
	if api == nil {
		return func() {}
	}
	if chatID == 0 {
		// Nothing to address; keep the stop function a no-op.
		return func() {}
	}

	typingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	firstAttempt := make(chan struct{})

	go func() {
		defer close(done)
		logged := false
		first := true
		for {
			if err := sendTyping(typingCtx, api, chatID); err != nil && !logged && typingCtx.Err() == nil {
				// One log per typing session keeps a failing send from
				// spamming the log every refresh.
				logged = true
				log.Printf("telegram: typing indicator for chat %d of %s: %v",
					chatID, a.agent.Name, media.RedactToken(err, a.token))
			}
			if first {
				close(firstAttempt)
				first = false
			}
			select {
			case <-typingCtx.Done():
				return
			case <-time.After(TypingRefreshInterval):
			}
		}
	}()

	return func() {
		// An instant turn can finish before the goroutine is scheduled. Wait
		// for its first attempt so "typing" is never skipped on fast replies.
		<-firstAttempt
		cancel()
		select {
		case <-done:
		case <-time.After(typingStopTimeout):
			log.Printf("telegram: typing indicator shutdown timed out for %s", a.agent.Name)
		}
	}
}

func sendTyping(ctx context.Context, api API, chatID int64) error {
	sendCtx, cancel := context.WithTimeout(ctx, typingSendTimeout)
	defer cancel()
	_, err := api.SendChatAction(sendCtx, &bot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	})
	return err
}

// registerCommands publishes botCommands as the bot's default command menu.
func (a *Adapter) registerCommands(ctx context.Context) error {
	if a.api == nil || len(botCommands) == 0 {
		return nil
	}
	_, err := a.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: append([]models.BotCommand(nil), botCommands...),
		Scope:    &models.BotCommandScopeDefault{},
	})
	return err
}
