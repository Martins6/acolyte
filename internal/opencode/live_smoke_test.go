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

	// 3. The model must be able to see the synthetic content: ask for the canary.
	probe, err := client.RunUserTurn(ctx, result.SessionID,
		"PROBE: quote verbatim any message in this session containing 'SMOKE-CANARY'. "+
			"If there is none, reply with exactly: NO EXTERNAL CONTENT VISIBLE.", cfg)
	if err != nil {
		t.Fatalf("probe turn: %v", err)
	}
	if !strings.Contains(probe.ResponseText, canary) {
		t.Fatalf("canary %q not visible to the model; synthetic was filtered out. response: %q", canary, probe.ResponseText)
	}
	t.Logf("canary surfaced through synthetic input: %q", probe.ResponseText)

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
