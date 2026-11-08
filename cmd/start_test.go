package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martins6/acolyte/internal/config"
	"github.com/martins6/acolyte/internal/paths"
)

func clearEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestApplyEnvTokenFromGlobalEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	clearEnv(t, "TELEGRAM_BOT_TOKEN")

	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("TELEGRAM_BOT_TOKEN=from-env\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg := &config.Config{}
	applyEnvToken(cfg)
	if cfg.Bot.Token != "from-env" {
		t.Errorf("token = %q, want from-env", cfg.Bot.Token)
	}
}

func TestApplyEnvTokenKeepsConfiguredToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	clearEnv(t, "TELEGRAM_BOT_TOKEN")

	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("TELEGRAM_BOT_TOKEN=from-env\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg := &config.Config{}
	cfg.Bot.Token = "configured"
	applyEnvToken(cfg)
	if cfg.Bot.Token != "configured" {
		t.Errorf("token = %q, want configured", cfg.Bot.Token)
	}
}

func TestApplyEnvTokenProcessWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	t.Setenv("TELEGRAM_BOT_TOKEN", "process")

	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("TELEGRAM_BOT_TOKEN=from-env\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg := &config.Config{}
	applyEnvToken(cfg)
	if cfg.Bot.Token != "process" {
		t.Errorf("token = %q, want process", cfg.Bot.Token)
	}
}
