package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/comm/telegram"
	"github.com/martins6/acolyte/internal/comm/telegram/telegramtest"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/storepool"
)

// TestDaemonRoutesThroughEmulatedTelegram runs the real adapter and router
// against the in-process Telegram emulator. Only the OpenCode turn runner is
// faked, so polling, dedup, routing, persistence and outbound delivery are the
// production code paths.
func TestDaemonRoutesThroughEmulatedTelegram(t *testing.T) {
	server := telegramtest.New()
	t.Cleanup(server.Close)

	t.Setenv(paths.HomeEnv, t.TempDir())
	t.Setenv(telegram.ServerURLEnv, server.URL)
	clearProcessToken(t)

	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	pool := storepool.New()
	t.Cleanup(func() { _ = pool.CloseAll() })

	ws := filepath.Join(t.TempDir(), "valet")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CommTG().SetSetting("channels", `["telegram"]`); err != nil {
		t.Fatalf("channels setting: %v", err)
	}
	if err := st.CommTG().SetSetting("telegram.allowed_users", `["42"]`); err != nil {
		t.Fatalf("allowed users setting: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("TELEGRAM_BOT_TOKEN="+server.Token+"\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	agent := registry.Agent{Name: "valet", Workspace: ws}
	if err := reg.Register(agent); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	svc, err := Start(context.Background(), Options{
		Registry:        reg,
		Stores:          pool,
		OpenCode:        &fakeTurn{},
		RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(svc.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.WaitForCalls(ctx, "getUpdates", 1); err != nil {
		t.Fatalf("adapter did not start polling: %v", err)
	}

	update := server.SendText(10, 42, "alice", "hello from the emulator")
	messages, err := server.WaitForChatMessages(ctx, 10, 1)
	if err != nil {
		t.Fatalf("wait for reply: %v", err)
	}
	reply := messages[0]
	if reply.Text != "reply" {
		t.Errorf("reply text = %q, want fake turn response", reply.Text)
	}
	if reply.ReplyTo == nil || *reply.ReplyTo != int64(update.Message.ID) {
		t.Errorf("reply_to = %v, want %d", reply.ReplyTo, update.Message.ID)
	}

	st, err = store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	items, err := st.CommTG().ListInbox(10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("inbox = %+v, want one row", items)
	}
	if items[0].OpenCodeSessionID != "ses_user" || items[0].OpenCodeMessageID != "msg_in" || items[0].ProcessedAt == nil {
		t.Errorf("inbox row = %+v, want linked and processed", items[0])
	}
	session, ok, err := st.Orch().GetSessionByThread("telegram:10")
	if err != nil || !ok || session.OpenCodeSessionID != "ses_user" {
		t.Errorf("session = %+v, %v, %v", session, ok, err)
	}
	outbox, err := st.CommTG().ListOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].Delivery != "sent" || outbox[0].Text != "" {
		t.Errorf("outbox = %+v, %v", outbox, err)
	}
}

func clearProcessToken(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv("TELEGRAM_BOT_TOKEN")
	if err := os.Unsetenv("TELEGRAM_BOT_TOKEN"); err != nil {
		t.Fatalf("unset token: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("TELEGRAM_BOT_TOKEN", old)
			return
		}
		_ = os.Unsetenv("TELEGRAM_BOT_TOKEN")
	})
}
