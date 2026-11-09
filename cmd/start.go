package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/martins6/acolyte/internal/bot"
	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/database"
	"github.com/martins6/acolyte/internal/logger"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/scheduler"
	"github.com/martins6/acolyte/internal/secrets"
	"github.com/martins6/acolyte/internal/updater"
	"github.com/martins6/acolyte/internal/workspace"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start bot in workspace",
	Long: `Starts the Telegram bot in the configured workspace.

The bot will:
1. Initialize the Telegram bot
2. Handle incoming messages through the OpenCode V2 API

Press Ctrl+C to stop the bot gracefully.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load("")
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		applyEnvToken(cfg)

		workspacePath := cfg.Workspace.Path
		if workspacePath == "" {
			homeDir, _ := os.UserHomeDir()
			workspacePath = homeDir + "/.acolyte"
		}

		if err := workspace.ValidateWorkspace(workspacePath); err != nil {
			log.Printf("Workspace not found, creating: %v", err)
			if err := workspace.CreateTemplate(workspacePath); err != nil {
				return fmt.Errorf("failed to create workspace: %w", err)
			}
		}

		if err := logger.Initialize(workspacePath); err != nil {
			log.Printf("Warning: Failed to initialize logger: %v", err)
		}

		logger.LogDebug("Logger initialized in workspace: %s", workspacePath)

		log.Println("Ensuring OpenCode service...")
		ensureCtx, ensureCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ensureCancel()
		if _, err := opencode.Ensure(ensureCtx); err != nil {
			return fmt.Errorf("opencode service unavailable: %w", err)
		}

		log.Println("Initializing Telegram bot...")
		bot.SetConfig(cfg)
		telegramBot, err := bot.Initialize(cfg)
		if err != nil {
			return fmt.Errorf("failed to initialize bot: %w", err)
		}

		if telegramBot != nil {
			bot.RegisterHandlers(telegramBot)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := bot.Start(ctx, telegramBot); err != nil {
			return fmt.Errorf("failed to start bot: %w", err)
		}

		if err := bot.StartNotifier(ctx, telegramBot); err != nil {
			log.Printf("Warning: Failed to start notifier service: %v", err)
		}

		if err := scheduler.StartScheduler(ctx, telegramBot, workspacePath); err != nil {
			log.Printf("Warning: Failed to start scheduler service: %v", err)
		}

		go checkForUpdatesAsync(ctx)

		log.Println("Bot is running. Press Ctrl+C to stop.")

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down...")

		if telegramBot != nil {
			log.Println("Stopping Telegram bot...")
		}

		logger.Close()

		log.Println("Shutdown complete")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}

// applyEnvToken lets the global $ACOLYTE_HOME/.env supply the Telegram token
// when the legacy config.toml does not set one. The per-agent workspace .env
// is consumed by the multi-agent Communication adapter in a later slice.
func applyEnvToken(cfg *config.Config) {
	if cfg == nil || cfg.Bot.Token != "" {
		return
	}
	loaded, err := secrets.Load("")
	if err != nil {
		log.Printf("Warning: failed to load secrets: %v", err)
		return
	}
	if token, ok := loaded.Lookup("TELEGRAM_BOT_TOKEN"); ok {
		cfg.Bot.Token = token
	}
}

func checkForUpdatesAsync(parent context.Context) {
	checkCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	latest, outdated, err := updater.IsOutdated(checkCtx, updater.Options{})
	if err != nil {
		if err != updater.ErrDevBuild {
			log.Printf("acolyte: update check failed: %v", err)
		}
		return
	}
	if !outdated {
		return
	}

	msg := fmt.Sprintf("Acolyte %s is available. Run `acolyte update` to upgrade.", latest)

	userID := config.GetAllowedUserChatID()
	if userID == 0 {
		log.Printf("acolyte: update available: %s (run 'acolyte update')", latest)
		logger.LogDebug("update available: %s", latest)
		return
	}

	if _, err := database.InsertNotification(userID, msg); err != nil {
		log.Printf("acolyte: failed to queue update notification: %v", err)
		return
	}
	log.Printf("acolyte: queued update notification for v%s", latest)
	logger.LogDebug("queued update notification for v%s", latest)
}
