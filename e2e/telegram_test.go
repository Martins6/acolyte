//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/comm/telegram/telegramtest"
	"github.com/martins6/acolyte/internal/store"
)

// TestTelegramDaemonEmulatorEndToEnd starts the real CLI daemon against the
// in-process Telegram emulator and a real OpenCode service. It covers the full
// path: emulated getUpdates -> adapter -> router -> real turn -> durable
// outbox -> emulated sendMessage.
func TestTelegramDaemonEmulatorEndToEnd(t *testing.T) {
	f := newTelegramFixture(t)
	server := telegramtest.New()
	t.Cleanup(server.Close)

	if err := os.WriteFile(filepath.Join(f.ws, ".env"),
		[]byte("TELEGRAM_BOT_TOKEN="+server.Token+"\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	stdout, stderr, done, stop := startDaemon(t, f.home, server.URL)
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	pollCtx, pollCancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-done:
			pollCancel()
		case <-pollCtx.Done():
		}
	}()
	err := server.WaitForCalls(pollCtx, "getUpdates", 1)
	pollCancel()
	if err != nil {
		t.Fatalf("daemon did not start polling\nstdout:\n%s\nstderr:\n%s\nerror: %v", stdout(), stderr(), err)
	}

	update := server.SendText(42, 42, "e2e", "Reply with exactly: ACOLYTE-TG-E2E-OK")
	messages, err := server.WaitForChatMessages(ctx, 42, 1)
	if err != nil {
		t.Fatalf("wait for Telegram reply\nstdout:\n%s\nstderr:\n%s\nerror: %v", stdout(), stderr(), err)
	}
	reply := messages[0]
	if !strings.Contains(reply.Text, "ACOLYTE-TG-E2E-OK") {
		t.Fatalf("reply = %q, want canary\nstdout:\n%s\nstderr:\n%s", reply.Text, stdout(), stderr())
	}
	if reply.ReplyTo == nil || *reply.ReplyTo != int64(update.Message.ID) {
		t.Errorf("reply_to = %v, want %d", reply.ReplyTo, update.Message.ID)
	}

	st, err := store.OpenReadOnly(f.ws)
	if err != nil {
		t.Fatalf("open store read-only: %v", err)
	}
	defer st.Close()

	// The emulator records the message as soon as the handler accepts it, but
	// the daemon still has to persist delivery progress. Wait for the durable
	// row before stopping so shutdown cannot cancel the in-flight response.
	deadline := time.Now().Add(30 * time.Second)
	for {
		outbox, listErr := st.CommTG().ListOutbox(42)
		if listErr == nil && len(outbox) == 1 && outbox[0].Delivery == "sent" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox did not reach sent\nstdout:\n%s\nstderr:\n%s", stdout(), stderr())
		}
		time.Sleep(50 * time.Millisecond)
	}

	session, ok, err := st.Orch().GetSessionByThread("telegram:42")
	if err != nil || !ok || session.OpenCodeSessionID == "" {
		t.Fatalf("session = %+v, %v, %v; want a mapped OpenCode session", session, ok, err)
	}
	inbox, err := st.CommTG().ListInbox(42)
	if err != nil || len(inbox) != 1 || inbox[0].ProcessedAt == nil {
		t.Fatalf("inbox = %+v, %v; want one processed row", inbox, err)
	}
	outbox, err := st.CommTG().ListOutbox(42)
	if err != nil || len(outbox) != 1 || outbox[0].Delivery != "sent" {
		t.Fatalf("outbox = %+v, %v; want one sent row", outbox, err)
	}

	stop()
}

func newTelegramFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "agent")
	model := strings.TrimSpace(os.Getenv("ACOLYTE_E2E_MODEL"))
	if model == "" {
		model = "opencode-go/gpt-6-luna"
	}
	config := fmt.Sprintf(`{
		"engine":{"model":%q,"agent":"build"},
		"communication":{"channels":["telegram"],"telegram":{"allowed_users":["42"]}}
	}`, model)

	stdout, stderr, code := runCLI(t, home, "new",
		"--name", "telegram-e2e",
		"--workspace-folder", ws,
		"--config", config,
	)
	if code != 0 {
		t.Fatalf("acolyte new failed (%d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	return fixture{home: home, ws: ws}
}

// startDaemon launches `acolyte start`. The returned stop function interrupts
// the process and waits for a clean exit; it is safe to call more than once.
func startDaemon(t *testing.T, home, serverURL string) (func() string, func() string, <-chan struct{}, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binaryPath, "start")
	cmd.Env = appendWithEnv(commandEnv(home), "ACOLYTE_TELEGRAM_API_URL="+serverURL)
	out := &lockedBuffer{}
	errOut := &lockedBuffer{}
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start daemon: %v", err)
	}

	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
			}
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
			}
			cancel()
		})
	}
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return out.String, errOut.String, done, stop
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func appendWithEnv(env []string, entry string) []string {
	key, _, _ := strings.Cut(entry, "=")
	out := make([]string, 0, len(env)+1)
	for _, existing := range env {
		existingKey, _, _ := strings.Cut(existing, "=")
		if existingKey != key {
			out = append(out, existing)
		}
	}
	return append(out, entry)
}
