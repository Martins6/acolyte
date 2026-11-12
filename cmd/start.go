package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/martins6/acolyte/internal/cron"
	"github.com/martins6/acolyte/internal/daemon"
	"github.com/martins6/acolyte/internal/logging"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/storepool"
	"github.com/martins6/acolyte/internal/updater"
	"github.com/spf13/cobra"
)

var startLogFile string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the multi-agent daemon",
	Long: `Starts the Acolyte daemon for every registered agent.

The daemon:
1. Ensures the OpenCode background service is reachable.
2. Starts each agent's Communication adapters.
3. Runs the shared cron service.
4. Routes inbound messages through OpenCode sessions and replies through the
   agent's channel.

Press Ctrl+C to stop gracefully.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if startLogFile != "" {
			logPath, err := paths.Abs(startLogFile)
			if err != nil {
				return fmt.Errorf("resolve --log-file: %w", err)
			}
			writer, err := logging.Open(logPath, logging.DefaultMaxSize, logging.DefaultBackups)
			if err != nil {
				return err
			}
			defer writer.Close()
			log.SetOutput(writer)
		}

		log.Println("Ensuring OpenCode service...")
		ensureCtx, ensureCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer ensureCancel()
		opencodeClient, err := opencode.Ensure(ensureCtx)
		if err != nil {
			return fmt.Errorf("opencode service unavailable: %w", err)
		}

		reg, err := registry.OpenDefault()
		if err != nil {
			return fmt.Errorf("open agent registry: %w", err)
		}
		defer reg.Close()

		pool := storepool.New()
		defer pool.CloseAll()

		daemonSvc, err := daemon.Start(ctx, daemon.Options{
			Registry: reg,
			Stores:   pool,
			OpenCode: opencodeClient,
		})
		if err != nil {
			return fmt.Errorf("start communication daemon: %w", err)
		}
		defer daemonSvc.Stop()

		cronSvc, err := cron.Start(ctx, cron.Options{
			Registry: reg,
			Routines: routine.Default(),
			Sink:     daemonSvc,
			OpenCode: opencodeClient,
			Stores:   pool,
		})
		if err != nil {
			return fmt.Errorf("start cron service: %w", err)
		}
		defer cronSvc.Stop()

		go checkForUpdatesAsync(ctx, daemonSvc)

		log.Println("Daemon is running. Press Ctrl+C to stop.")
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down...")
		return nil
	},
}

func init() {
	startCmd.Flags().StringVar(&startLogFile, "log-file", "", "Write logs to this file instead of stderr (rotates at 10 MiB, keeps 3 backups)")
	rootCmd.AddCommand(startCmd)
}

func checkForUpdatesAsync(parent context.Context, daemonSvc *daemon.Service) {
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
	if daemonSvc != nil {
		if count := daemonSvc.NotifyAll(checkCtx, msg); count > 0 {
			log.Printf("acolyte: queued update notification for %d agent(s)", count)
			return
		}
	}
	log.Printf("acolyte: update available: %s (run 'acolyte update')", latest)
}
