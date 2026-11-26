package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/opencode"
)

func openBoardTestStore(t *testing.T) *board.Store {
	t.Helper()
	st, err := board.OpenDefault()
	if err != nil {
		t.Fatalf("board.OpenDefault() error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("board.Close() error: %v", err)
		}
	})
	return st
}

func TestBoardAutomationAdmissionAndCooldown(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "automation",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardAutomate(context.Background(), &bytes.Buffer{}, boardAutomateOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardAutomate() error: %v", err)
	}

	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "first",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Fatalf("admissions = %d, want 1", fake.synthCalls)
	}
	if len(fake.admitEvents) != 1 || fake.admitEvents[0].Delivery != "queue" {
		t.Fatalf("admit events = %+v", fake.admitEvents)
	}
	if got := fake.admitEvents[0].Metadata["channel"]; got != "agent-board" {
		t.Errorf("metadata channel = %v", got)
	}
	if !strings.Contains(fake.admitEvents[0].Text, "ACOLYTE BOARD AUTOMATION") {
		t.Errorf("prompt = %q", fake.admitEvents[0].Text)
	}

	st := openBoardTestStore(t)
	automation, err := st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || automation.Turns != 1 || automation.SessionID == "" {
		t.Fatalf("automation = %+v", automation)
	}
	firstSession := automation.SessionID

	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "second",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Errorf("cooldown admitted another turn: %d", fake.synthCalls)
	}

	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET last_turn_at = ? WHERE agent_id = 'scribe' AND post_id = ?`,
		time.Now().Add(-time.Minute).UTC(), result.Post.ID,
	); err != nil {
		t.Fatalf("clear cooldown: %v", err)
	}
	if err := runBoardInbox(context.Background(), &bytes.Buffer{}, boardInboxOptions{
		Target: scribe,
		Retry:  true,
	}); err != nil {
		t.Fatalf("runBoardInbox(retry) error: %v", err)
	}
	if fake.synthCalls != 2 {
		t.Fatalf("admissions after retry = %d, want 2", fake.synthCalls)
	}
	if fake.admitSessions[1] != firstSession {
		t.Errorf("second admission session = %q, want reused %q", fake.admitSessions[1], firstSession)
	}
	automation, err = st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.Turns != 2 {
		t.Errorf("turns = %d, want 2", automation.Turns)
	}
}

func TestBoardAutomationCapPausesAndAlerts(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "cap",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardAutomate(context.Background(), &bytes.Buffer{}, boardAutomateOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardAutomate() error: %v", err)
	}
	st := openBoardTestStore(t)
	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET turns = ?, last_turn_at = ? WHERE agent_id = 'scribe' AND post_id = ?`,
		boardAutomationMaxTurns, time.Now().Add(-time.Minute).UTC(), result.Post.ID,
	); err != nil {
		t.Fatalf("seed cap: %v", err)
	}

	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "over the cap",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if fake.synthCalls != 0 {
		t.Errorf("admissions = %d, want 0 at cap", fake.synthCalls)
	}
	automation, err := st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || !automation.Paused() {
		t.Fatalf("automation = %+v, want paused", automation)
	}
	alerts, err := st.Alerts(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("Alerts() error: %v", err)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0].Message, "turn cap") {
		t.Errorf("alerts = %+v", alerts)
	}
}

func TestBoardAutomationRecoversDeletedSession(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{
		admitErrOn: "ses_gone",
		admitErr:   &opencode.APIError{Status: 404, Message: "gone"},
	}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "recover",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardAutomate(context.Background(), &bytes.Buffer{}, boardAutomateOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardAutomate() error: %v", err)
	}
	st := openBoardTestStore(t)
	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET session_id = 'ses_gone' WHERE agent_id = 'scribe' AND post_id = ?`,
		result.Post.ID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "retry",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if fake.synthCalls != 2 {
		t.Fatalf("admissions = %d, want 2 (404 + retry)", fake.synthCalls)
	}
	if len(fake.admitSessions) != 2 || fake.admitSessions[0] != "ses_gone" || fake.admitSessions[1] != "" {
		t.Errorf("admit sessions = %v", fake.admitSessions)
	}
	automation, err := st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || automation.SessionID == "ses_gone" || automation.SessionID == "" {
		t.Errorf("automation = %+v, want a fresh session", automation)
	}
}

func TestBoardInboxPromote(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "promote",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(context.Background(), &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "promote me",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}

	var out bytes.Buffer
	if err := runBoardInboxPromote(context.Background(), &out, boardInboxPromoteOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardInboxPromote() error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Errorf("main-session turns = %d, want 1", fake.synthCalls)
	}
	if !strings.Contains(out.String(), "Promoted 1 pings") {
		t.Errorf("output = %q", out.String())
	}
	st := openBoardTestStore(t)
	inbox, err := st.Inbox(context.Background(), "scribe", false)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("unread inbox after promote = %+v", inbox)
	}
}
