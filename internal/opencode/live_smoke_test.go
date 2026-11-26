package opencode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSmoke exercises the real client against the real OpenCode
// background service: service discovery, session creation, prompt turns,
// synthetic external events, and the turn collector. It is the live
// regression check for the V2 transport (SPEC §5.1/§5.2).
//
// Run manually:
//
//	ACOLYTE_LIVE_SMOKE=1 go test ./internal/opencode/ -run LiveSmoke -v
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("ACOLYTE_LIVE_SMOKE") == "" {
		t.Skip("set ACOLYTE_LIVE_SMOKE=1 to run against the real OpenCode service")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	cfg := TurnConfig{Workspace: t.TempDir()}

	// 1. Real user turn creates a session and returns the reply.
	result, err := client.RunUserTurn(ctx, "", "Reply with exactly: OK", cfg)
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("no session id returned")
	}
	if !strings.Contains(result.ResponseText, "OK") {
		t.Fatalf("response %q does not contain OK", result.ResponseText)
	}
	t.Logf("session=%s reply=%q", result.SessionID, result.ResponseText)

	// 2. Synthetic external event carrying a canary (SPEC §5.2 method).
	canary := "SMOKE-CANARY-7f3a"
	if _, err := client.RunSyntheticTurn(ctx, result.SessionID, SyntheticEvent{
		Text:        "External notification. Tag=" + canary,
		Description: "smoke: external background event",
		Metadata:    map[string]any{"source": "live-smoke"},
		Delivery:    "queue",
	}, cfg); err != nil {
		t.Fatalf("RunSyntheticTurn: %v", err)
	}

	// 3. The synthetic content must be part of the model's active context.
	// Checked against the deterministic context projection (SPEC §5.2): a
	// model self-report is unreliable because synthetic inputs are framed
	// as <system-update>, which models may or may not call "a message".
	msgs, err := client.Context(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Text, canary) {
			found = true
			break
		}
		for _, part := range m.Content {
			if strings.Contains(part.Text, canary) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("canary %q not in the model's active context; synthetic was filtered out. context: %+v", canary, msgs)
	}
	t.Logf("canary visible in the model's active context (%d context messages)", len(msgs))

	// 4. Model round-trip: switch to the session's current model and verify.
	info, err := client.GetSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if info.Model != nil {
		if err := client.SwitchModel(ctx, result.SessionID, *info.Model); err != nil {
			t.Fatalf("SwitchModel: %v", err)
		}
		got, err := client.GetSession(ctx, result.SessionID)
		if err != nil {
			t.Fatalf("GetSession after switch: %v", err)
		}
		if got.Model == nil || got.Model.ID != info.Model.ID || got.Model.ProviderID != info.Model.ProviderID {
			t.Fatalf("model after switch = %+v, want %+v", got.Model, info.Model)
		}
		t.Logf("model switch ok: %+v", got.Model)
	}
}

// TestLiveHeadlessQuestion exercises the headless settlement path against a
// real OpenCode service. It is opt-in because it needs a live model:
//
//	ACOLYTE_LIVE_HEADLESS=1 go test ./internal/opencode/ -run LiveHeadlessQuestion -v
//
// A model that declines to call the question tool is a skip, not a failure: the
// point is to prove that a question form, when it appears, is cancelled and the
// turn still completes.
func TestLiveHeadlessQuestion(t *testing.T) {
	if os.Getenv("ACOLYTE_LIVE_HEADLESS") == "" {
		t.Skip("set ACOLYTE_LIVE_HEADLESS=1 to run against the real OpenCode service")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var settlements []InteractionSettlement
	result, err := client.RunUserTurn(ctx, "", "Use the question tool to ask me one short question about my favorite color. If the tool is unavailable, reply exactly DONE.", TurnConfig{
		Workspace: t.TempDir(),
		OnInteraction: func(settlement InteractionSettlement) {
			settlements = append(settlements, settlement)
		},
	})
	if err != nil {
		t.Fatalf("RunUserTurn: %v", err)
	}
	if result.SessionID == "" || strings.TrimSpace(result.ResponseText) == "" {
		t.Fatalf("result = %+v, want a session and a reply", result)
	}

	sawQuestion := false
	for _, settlement := range settlements {
		if settlement.Kind != "form" {
			continue
		}
		sawQuestion = true
		if settlement.Action != "cancelled" {
			t.Errorf("form settlement = %+v, want cancelled", settlement)
		}
		if settlement.Message != headlessQuestionMessage {
			t.Errorf("form message = %q, want question message", settlement.Message)
		}
	}
	if !sawQuestion {
		t.Skip("model did not call the question tool; nothing to settle")
	}
}
