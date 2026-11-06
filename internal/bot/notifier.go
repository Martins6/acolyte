package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/logger"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/session"
)

type NotifierSender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
}

type NotifierService struct {
	bot    *bot.Bot
	ctx    context.Context
	cancel context.CancelFunc
	sender NotifierSender
}

type realSender struct {
	bot *bot.Bot
}

func (r *realSender) SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	return r.bot.SendMessage(ctx, params)
}

type mockSender struct{}

func (m *mockSender) SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	if params != nil {
		logger.LogDebug("Mock: would send message to chat %d: %s", params.ChatID, params.Text)
	}
	return nil, nil
}

func StartNotifier(ctx context.Context, b *bot.Bot) error {
	workspacePath := ""
	cfg := config.Get()
	if cfg != nil && cfg.Workspace.Path != "" {
		workspacePath = cfg.Workspace.Path
	} else {
		homeDir, _ := os.UserHomeDir()
		workspacePath = filepath.Join(homeDir, ".acolyte")
	}

	if err := database.Init(workspacePath); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	n := &NotifierService{
		bot: b,
		ctx: ctx,
	}
	n.ctx, n.cancel = context.WithCancel(ctx)

	if b != nil {
		n.sender = &realSender{bot: b}
	} else {
		n.sender = &mockSender{}
	}

	go n.run()

	logger.LogDebug("Notifier service started")
	return nil
}

func (n *NotifierService) run() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			logger.LogDebug("Notifier service stopped")
			return
		case <-ticker.C:
			n.processNotifications()
			n.processMails()
		}
	}
}

func (n *NotifierService) processNotifications() {
	userChatID := config.GetAllowedUserChatID()
	if userChatID == 0 {
		return
	}

	notifications, err := database.GetUnsentNotifications(userChatID)
	if err != nil {
		logger.LogDebug("Notifier: failed to get notifications for user %d: %v", userChatID, err)
		return
	}

	for _, notification := range notifications {
		message := fmt.Sprintf("--- Notification (Agent Unaware) ---\nReceived at: %s\n\n%s",
			notification.CreatedAt.Format("2006-01-02 15:04:05"),
			notification.Message,
		)

		_, err := n.sender.SendMessage(n.ctx, &bot.SendMessageParams{
			ChatID: userChatID,
			Text:   message,
		})
		if err != nil {
			logger.LogDebug("Notifier: failed to send notification %d: %v", notification.ID, err)
			continue
		}

		if err := database.MarkNotificationSent(notification.ID); err != nil {
			logger.LogDebug("Notifier: failed to mark notification as sent: %v", err)
		}
	}
}

func (n *NotifierService) processMails() {
	if _, err := config.Load(""); err != nil {
		logger.LogDebug("notifier: failed to reload config: %v", err)
	}
	if fresh := config.Get(); fresh != nil {
		cfg = fresh
	}

	userChatID := config.GetAllowedUserChatID()
	if userChatID == 0 {
		return
	}

	workspace := ""
	if cfg != nil && cfg.Workspace.Path != "" {
		workspace = cfg.Workspace.Path
	} else {
		homeDir, _ := os.UserHomeDir()
		workspace = filepath.Join(homeDir, ".acolyte")
	}

	mails, err := database.GetUnsentMails(userChatID)
	if err != nil {
		logger.LogDebug("Notifier: failed to get mails for user %d: %v", userChatID, err)
		return
	}

	for _, mail := range mails {
		logger.LogDebug("Notifier: processing mail %s for user %d (sender: %s, subject: %s)", mail.ID, userChatID, mail.Sender, mail.Subject)

		agentMessage := fmt.Sprintf(
			"New email received:\nID: %s\nFrom: %s\nSubject: %s\nTimestamp: %s\n\nContent:\n%s",
			mail.ID,
			mail.Sender,
			mail.Subject,
			mail.CreatedAt.Format("2006-01-02 15:04:05"),
			mail.Content,
		)

		sessionMgr := session.GetManager()
		userSession, err := sessionMgr.GetSession(userChatID, workspace)
		if err != nil {
			logger.LogDebug("Notifier: failed to get session for user %d: %v", userChatID, err)
			n.sendFallbackMailNotification(userChatID, mail)
			continue
		}

		agent := cfg.Defaults.Agent
		model := cfg.Defaults.Model
		provider := cfg.Defaults.Provider

		if settings := userSettings[userChatID]; settings != nil {
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

		if agent == "" {
			agent = "build"
		}
		if model == "" {
			model = "MiniMax-M3"
		}
		if provider == "" {
			provider = "minimax-coding-plan"
		}

		client, err := opencode.Ensure(n.ctx)
		if err != nil {
			logger.LogDebug("Notifier: OpenCode service unavailable for mail %s: %v", mail.ID, err)
			n.sendFallbackMailNotification(userChatID, mail)
			continue
		}

		var sessionID string
		if userSession.OpenCodeID != "" && !userSession.IsNewSession {
			sessionID = userSession.OpenCodeID
		}

		logger.LogDebug("Notifier: triggering agent for mail %s (session: %s, agent: %s, model: %s, provider: %s)",
			mail.ID, sessionID, agent, model, provider)

		result, err := client.RunSyntheticTurn(n.ctx, sessionID, opencode.SyntheticEvent{
			Text:        agentMessage,
			Description: "New email received",
			Metadata: map[string]any{
				"source":  "mail",
				"mail_id": mail.ID,
				"sender":  mail.Sender,
				"subject": mail.Subject,
			},
			Delivery: "queue",
		}, opencode.TurnConfig{
			Workspace: workspace,
			Agent:     agent,
			Provider:  provider,
			Model:     model,
		})
		if err != nil {
			logger.LogDebug("Notifier: agent failed for mail %s: %v", mail.ID, err)
			n.sendFallbackMailNotification(userChatID, mail)
			continue
		}

		if result.ResponseText != "" {
			logger.LogDebug("Notifier: sending agent response for mail %s to user %d", mail.ID, userChatID)
			_, err := n.sender.SendMessage(n.ctx, &bot.SendMessageParams{
				ChatID: userChatID,
				Text:   result.ResponseText,
			})
			if err != nil {
				logger.LogDebug("Notifier: failed to send agent response for mail %s: %v", mail.ID, err)
				n.sendFallbackMailNotification(userChatID, mail)
				continue
			}
		}

		if result.SessionID != "" {
			userSession.OpenCodeID = result.SessionID
			userSession.IsNewSession = false
			sessionMgr.UpdateSession(userSession)
			logger.LogDebug("Notifier: updated session ID to %s for user %d", result.SessionID, userChatID)
		}

		if err := database.MarkMailSent(mail.ID); err != nil {
			logger.LogDebug("Notifier: failed to mark mail %s as sent: %v", mail.ID, err)
		} else {
			logger.LogDebug("Notifier: mail %s delivered successfully", mail.ID)
		}
	}
}

func (n *NotifierService) sendFallbackMailNotification(userChatID int64, mail database.Mail) {
	message := fmt.Sprintf("You have a new mail from %s: %s", mail.Sender, mail.Subject)

	_, err := n.sender.SendMessage(n.ctx, &bot.SendMessageParams{
		ChatID: userChatID,
		Text:   message,
	})
	if err != nil {
		logger.LogDebug("Notifier: fallback failed to send mail %s: %v", mail.ID, err)
		return
	}

	if err := database.MarkMailSent(mail.ID); err != nil {
		logger.LogDebug("Notifier: fallback failed to mark mail %s as sent: %v", mail.ID, err)
	} else {
		logger.LogDebug("Notifier: mail %s delivered via fallback", mail.ID)
	}
}

func StopNotifier() {
	logger.LogDebug("Stopping notifier service...")
}
