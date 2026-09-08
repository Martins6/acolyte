package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/logger"
	"github.com/martins6/acolyte/internal/media"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/session"
)

var userSettings map[int64]*UserSettings
var cfg *config.Config

type UserSettings struct {
	Agent     string
	Model     string
	Provider  string
	Workspace string
}

func SetConfig(c *config.Config) {
	cfg = c
	if userSettings == nil {
		userSettings = make(map[int64]*UserSettings)
	}
}

// mediaKindLabel returns the user-facing media type for logging and prompt
// metadata. Case order is kept in sync with ExtractFileRef in
// internal/media/downloader.go (Photo, Document, Audio, Voice, Video).
func mediaKindLabel(msg *models.Message) string {
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

func processMediaAttachment(ctx context.Context, b *bot.Bot, msg *models.Message, chatID int64, workspace string, userID int64) (string, bool) {
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := media.ExtractFileRef(msg)
	if !ok || fileID == "" {
		return "", false
	}

	kind := mediaKindLabel(msg)

	data, _, err := media.DownloadFile(ctx, b, fileID)
	if err != nil {
		logger.Log(logger.ERROR, userID, fmt.Sprintf("Failed to download %s: %v", kind, err))
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("Error: failed to download %s: %v", kind, err),
		})
		return "", true
	}

	localPath := media.GetFilePath(workspace, mediaType, ext)
	if err := media.SaveFile(localPath, data); err != nil {
		logger.Log(logger.ERROR, userID, fmt.Sprintf("Failed to save %s: %v", kind, err))
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("Error: failed to save %s: %v", kind, err),
		})
		return "", true
	}

	caption := strings.TrimSpace(msg.Caption)
	meta := media.MediaMetadata{
		Kind:         kind,
		FileSize:     fileSize,
		MimeType:     mimeType,
		OriginalName: originalName,
	}
	prompt := media.BuildPrompt(localPath, meta, caption)

	logger.Log(logger.INPUT, userID, fmt.Sprintf("%s received: fileID=%s size=%d saved=%s",
		kind, fileID, fileSize, localPath))
	log.Printf("Downloaded %s for user %d to %s (%d bytes)", kind, userID, localPath, len(data))

	return prompt, false
}

func DefaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}

	if _, err := config.Load(""); err != nil {
		logger.LogDebug("bot: failed to reload config: %v", err)
	}
	if fresh := config.Get(); fresh != nil {
		cfg = fresh
	}

	userID := update.Message.From.ID
	username := update.Message.From.Username

	if !isUserAllowed(userID, username) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "You are not authorized to use this bot.",
		})
		return
	}

	if username != "" && cfg.Bot.AllowedUserID == username {
		if err := database.SetResolvedChatID(username, userID); err != nil {
			log.Printf("Failed to store resolved chat ID for user %s: %v", username, err)
		}
	}

	log.Printf("Received message from user %d: %s", userID, update.Message.Text)

	chatID := update.Message.Chat.ID
	workspace := cfg.Workspace.Path

	userMessage, mediaHandled := processMediaAttachment(ctx, b, update.Message, chatID, workspace, userID)
	if mediaHandled && userMessage == "" {
		return
	}
	if userMessage == "" {
		userMessage = update.Message.Text
	}

	if strings.TrimSpace(userMessage) == "" {
		logger.Log(logger.DEBUG, userID, "Received empty or whitespace-only message, skipping")
		return
	}

	logger.Log(logger.INPUT, userID, fmt.Sprintf("Message: %s", userMessage))

	sessionMgr := session.GetManager()
	userSession, err := sessionMgr.GetSession(userID, workspace)
	if err != nil {
		log.Printf("Error getting session for user %d: %v", userID, err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Error: Could not initialize session. Please try again.",
		})
		return
	}

	settings := userSettings[userID]
	agent := cfg.Defaults.Agent
	model := cfg.Defaults.Model
	provider := cfg.Defaults.Provider

	if agent == "" {
		agent = "build"
	}

	if settings != nil {
		if settings.Agent != "" {
			agent = settings.Agent
		}
		if settings.Model != "" {
			model = settings.Model
		}
		if settings.Provider != "" {
			provider = settings.Provider
		}
	}

	if model == "" {
		model = "MiniMax-M3"
	}
	if provider == "" {
		provider = "minimax-coding-plan"
	}

	runner := opencode.NewRunner(workspace, agent, model, provider)

	var sessionID string
	if userSession.OpenCodeID != "" && !userSession.IsNewSession {
		sessionID = userSession.OpenCodeID
		log.Printf("Continuing session %s for user %d", sessionID, userID)
	} else {
		log.Printf("Starting new session for user %d", userID)
	}

	log.Printf("Sending message to OpenCode for user %d (session: %s, agent: %s, model: %s, provider: %s)",
		userID, sessionID, agent, model, provider)
	log.Printf("Sending prompt for user %d: %s", userID, userMessage)

	result, err := runner.Execute(sessionID, userMessage)
	if err != nil {
		log.Printf("Error running opencode for user %d: %v", userID, err)

		errorMsg := "Error: Could not get response from OpenCode."
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "invalid") {
			errorMsg = fmt.Sprintf("Error: %v. Please check your model/provider settings with /set-model or /set-provider command.", err)
		} else if strings.Contains(err.Error(), "no valid output") {
			errorMsg = "Error: OpenCode returned no output. Please check your model/provider settings."
		} else {
			errorMsg = fmt.Sprintf("Error: Could not get response from OpenCode. %v", err)
		}

		logger.Log(logger.ERROR, userID, fmt.Sprintf("Failed to run opencode: %v", err))
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   errorMsg,
		})
		return
	}

	if result.SessionID != "" {
		userSession.OpenCodeID = result.SessionID
		userSession.IsNewSession = false
		sessionMgr.UpdateSession(userSession)
		log.Printf("Updated session ID to %s for user %d", result.SessionID, userID)
	}

	responseText := result.ResponseText
	if responseText == "" {
		responseText = "No response from OpenCode."
	}

	logger.Log(logger.OUTPUT, userID, fmt.Sprintf("Response: %s", responseText))

	const maxMessageLength = 4000
	if len(responseText) > maxMessageLength {
		logger.Log(logger.DEBUG, userID, fmt.Sprintf("Response too long (%d chars), splitting into chunks", len(responseText)))
		sendLongMessage(ctx, b, chatID, responseText, maxMessageLength)
	} else {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   responseText,
		})
	}
}

func sendLongMessage(ctx context.Context, b *bot.Bot, chatID int64, text string, maxLen int) {
	for len(text) > 0 {
		chunkLen := maxLen
		if len(text) < chunkLen {
			chunkLen = len(text)
		} else {
			lastSpace := strings.LastIndex(text[:chunkLen], " ")
			if lastSpace > 0 {
				chunkLen = lastSpace
			}
		}

		chunk := text[:chunkLen]
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   chunk,
		})
		if err != nil {
			log.Printf("Error sending message chunk: %v", err)
			return
		}

		text = text[chunkLen:]
		if len(text) > 0 && text[0] == ' ' {
			text = text[1:]
		}
	}
}

func extractResponseText(resp *opencode.MessageResponse) string {
	if resp == nil {
		return ""
	}

	var text string
	for _, part := range resp.Parts {
		if part.Type == "text" {
			text += part.Text
		}
	}
	return text
}

func isUserAllowed(userID int64, username string) bool {
	if cfg == nil {
		return false
	}

	if cfg.Bot.AllowedUserID == "" {
		return false
	}

	userIDStr := fmt.Sprintf("%d", userID)
	return cfg.Bot.AllowedUserID == userIDStr || cfg.Bot.AllowedUserID == username
}
