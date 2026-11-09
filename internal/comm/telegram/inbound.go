package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/media"
	"github.com/martins6/acolyte/internal/store/commtg"
)

func (a *Adapter) handleUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	if !a.isAllowed(msg.From.ID, msg.From.Username) {
		a.reply(ctx, msg.Chat.ID, "You are not authorized to use this bot.", 0)
		return
	}

	now := a.now()
	if err := a.store.CommTG().UpsertChat(msg.Chat.ID, msg.From.Username, true, now); err != nil {
		log.Printf("telegram: upsert chat for %s: %v", a.agent.Name, err)
	}

	text, mediaPath, contentType, err := a.buildContent(ctx, msg)
	if err != nil {
		log.Printf("telegram: build content for %s: %v", a.agent.Name, err)
		a.reply(ctx, msg.Chat.ID, "Error processing message; the daemon log has details.", int64(msg.ID))
		return
	}
	if strings.TrimSpace(text) == "" && mediaPath == "" {
		return
	}

	env := envelope.New(envelope.DirectionIn, Channel, envelope.TelegramThread(msg.Chat.ID),
		envelope.Party{
			Kind: envelope.PartyUser,
			ID:   strconv.FormatInt(msg.From.ID, 10),
			Name: msg.From.Username,
		},
		envelope.Party{Kind: envelope.PartyAgent, ID: a.agent.Name, Name: a.agent.Name},
		envelope.Content{Type: contentType, Text: text})
	if mediaPath != "" {
		env.Content.Media = []string{mediaPath}
	}
	env.Meta = map[string]any{
		"telegram_update_id":  update.ID,
		"telegram_message_id": int64(msg.ID),
		"telegram_chat_id":    msg.Chat.ID,
		"telegram_user_id":    msg.From.ID,
	}

	inserted, err := a.store.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: update.ID,
		ChatID:     msg.Chat.ID,
		EnvelopeID: env.ID,
		MediaPath:  mediaPath,
		ReceivedAt: now,
	})
	if err != nil {
		log.Printf("telegram: record inbound for %s: %v", a.agent.Name, err)
		return
	}
	if !inserted {
		return
	}

	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		sink(env)
	}
}

func (a *Adapter) buildContent(ctx context.Context, msg *models.Message) (string, string, envelope.ContentType, error) {
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := media.ExtractFileRef(msg)
	if !ok || fileID == "" {
		return strings.TrimSpace(msg.Text), "", envelope.ContentText, nil
	}

	item, found, err := a.store.CommTG().GetMedia(fileID)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		data, _, err := media.DownloadFile(ctx, a.api, fileID)
		if err != nil {
			return "", "", "", fmt.Errorf("download %s: %w", mediaKind(msg), err)
		}
		path := media.GetFilePath(a.agent.Workspace, mediaType, ext)
		if err := media.SaveFile(path, data); err != nil {
			return "", "", "", fmt.Errorf("save %s: %w", mediaKind(msg), err)
		}
		item = commtg.MediaItem{
			TGFileID: fileID,
			FilePath: path,
			Type:     string(mediaType),
			ChatID:   msg.Chat.ID,
			SavedAt:  a.now(),
		}
		if err := a.store.CommTG().RecordMedia(item); err != nil {
			return "", "", "", err
		}
	}

	prompt := media.BuildPrompt(item.FilePath, media.MediaMetadata{
		Kind:         mediaKind(msg),
		FileSize:     fileSize,
		MimeType:     mimeType,
		OriginalName: originalName,
	}, msg.Caption)
	return prompt, item.FilePath, envelope.ContentMedia, nil
}

func (a *Adapter) reply(ctx context.Context, chatID int64, text string, replyTo int64) {
	if a.api == nil || strings.TrimSpace(text) == "" {
		return
	}
	params := &bot.SendMessageParams{ChatID: chatID, Text: text}
	if replyTo > 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: int(replyTo), AllowSendingWithoutReply: true}
	}
	if _, err := a.api.SendMessage(ctx, params); err != nil {
		log.Printf("telegram: send reply for %s: %v", a.agent.Name, media.RedactToken(err, a.token))
	}
}

func (a *Adapter) isAllowed(userID int64, username string) bool {
	if len(a.allowed) == 0 {
		return false
	}
	id := strconv.FormatInt(userID, 10)
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	for _, raw := range a.allowed {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if entry == id {
			return true
		}
		if name != "" && strings.EqualFold(strings.TrimPrefix(entry, "@"), name) {
			return true
		}
	}
	return false
}

func mediaKind(msg *models.Message) string {
	switch {
	case msg.Photo != nil:
		return "Photo"
	case msg.Document != nil:
		return "Document"
	case msg.Audio != nil:
		return "Audio"
	case msg.Voice != nil:
		return "Voice"
	case msg.Video != nil:
		return "Video"
	default:
		return "Media"
	}
}
