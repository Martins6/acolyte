package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
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
	if !strings.Contains(fake.admitEvents[0].Text, boardEntryMarker(fake.admitEvents[0].Metadata["entry_id"].(string))) {
		t.Errorf("prompt missing entry marker: %q", fake.admitEvents[0].Text)
	}
	if len(fake.admitConfigs) != 1 {
		t.Fatalf("admit configs = %+v", fake.admitConfigs)
	}
	rules := fake.admitConfigs[0].Permissions
	if len(rules) < 3 || rules[0].Action != "*" || rules[0].Effect != opencode.PermissionDeny {
		t.Fatalf("automation permissions = %+v", rules)
	}
	allowed := map[string]bool{}
	for _, rule := range rules {
		if rule.Effect == opencode.PermissionAllow {
			allowed[rule.Action] = true
		}
	}
	if !allowed["board_read"] || !allowed["board_reply"] {
		t.Errorf("automation permissions missing board tools: %+v", rules)
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

func TestBoardAutomationAmbiguousAdmissionKeepsClaim(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{
		admitErrOn: "",
		admitErr:   errors.New("timeout"),
	}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "ambiguous",
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
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Fatalf("admissions = %d, want 1", fake.synthCalls)
	}

	st := openBoardTestStore(t)
	automation, err := st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || automation.Turns != 1 {
		t.Fatalf("automation = %+v, want one reserved turn", automation)
	}
	if automation.SessionID == "" {
		t.Errorf("session was not persisted before admission: %+v", automation)
	}
	inbox, err := st.Inbox(context.Background(), "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ClaimedAt == nil || inbox[0].ProcessedAt != nil {
		t.Fatalf("ambiguous ping = %+v, want a preserved claim", inbox)
	}

	// A quick retry is blocked by the same reservation/cooldown, so the
	// ambiguous admission is not repeated immediately.
	if err := runBoardInbox(context.Background(), &bytes.Buffer{}, boardInboxOptions{
		Target: scribe,
		Retry:  true,
	}); err != nil {
		t.Fatalf("runBoardInbox(retry) error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Errorf("ambiguous retry admitted again: %d calls", fake.synthCalls)
	}
}

func TestBoardAutomationTruncatedHistoryKeepsClaim(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{messageTruncated: true}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "truncated",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardAutomate(ctx, &bytes.Buffer{}, boardAutomateOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardAutomate() error: %v", err)
	}

	st := openBoardTestStore(t)
	entry, err := st.AppendMessage(ctx, board.AgentActor("valet"), result.Post.ID, board.AppendMessageInput{
		Text: "pending",
	})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.Enqueue(ctx, entry); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, _, err := st.ClaimPendingAutomated(ctx, "scribe", result.Post.ID, "claim-1", time.Minute); err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET session_id = 'ses_cli' WHERE agent_id = 'scribe' AND post_id = ?`,
		result.Post.ID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_inbox SET claimed_at = ? WHERE agent_id = 'scribe' AND entry_id = ?`,
		time.Now().Add(-time.Hour).UTC(), entry.ID,
	); err != nil {
		t.Fatalf("age claim: %v", err)
	}

	if err := runBoardInbox(ctx, &bytes.Buffer{}, boardInboxOptions{
		Target: scribe,
		Retry:  true,
	}); err != nil {
		t.Fatalf("runBoardInbox(retry) error: %v", err)
	}
	if fake.synthCalls != 0 {
		t.Errorf("truncated history admitted again: %d calls", fake.synthCalls)
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ClaimedAt == nil || inbox[0].ProcessedAt != nil {
		t.Fatalf("truncated ping = %+v, want a retained claim", inbox)
	}
}

func TestBoardAutomationReconcilesStaleClaim(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "stale claim",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardAutomate(ctx, &bytes.Buffer{}, boardAutomateOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardAutomate() error: %v", err)
	}

	st := openBoardTestStore(t)
	entry, err := st.AppendMessage(ctx, board.AgentActor("valet"), result.Post.ID, board.AppendMessageInput{
		Text: "pending automation",
	})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.Enqueue(ctx, entry); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	claimed, stale, err := st.ClaimPendingAutomated(ctx, "scribe", result.Post.ID, "claim-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if claimed != entry.ID || stale {
		t.Fatalf("claim = %q stale=%v, want %q", claimed, stale, entry.ID)
	}
	ok, err := st.ReserveAutomationTurnForEntry(ctx, "scribe", result.Post.ID, entry.ID, 2*time.Minute, 20)
	if err != nil || !ok {
		t.Fatalf("ReserveAutomationTurnForEntry() = %v, %v", ok, err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET session_id = 'ses_cli', last_turn_at = ?
		 WHERE agent_id = 'scribe' AND post_id = ?`,
		time.Now().Add(-time.Hour).UTC(), result.Post.ID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_inbox SET claimed_at = ? WHERE agent_id = 'scribe' AND entry_id = ?`,
		time.Now().Add(-time.Hour).UTC(), entry.ID,
	); err != nil {
		t.Fatalf("age claim: %v", err)
	}
	fake.bySession = map[string][]opencode.Message{
		"ses_cli": {{ID: "msg_admitted", Type: "synthetic", Text: "admitted " + boardEntryMarker(entry.ID)}},
	}

	if err := runBoardInbox(ctx, &bytes.Buffer{}, boardInboxOptions{
		Target: scribe,
		Retry:  true,
	}); err != nil {
		t.Fatalf("runBoardInbox(retry) error: %v", err)
	}
	if fake.synthCalls != 0 {
		t.Errorf("stale claim admitted again: %d calls", fake.synthCalls)
	}
	automation, err := st.Automation(context.Background(), "scribe", result.Post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || automation.Turns != 1 {
		t.Errorf("reconciled turns = %+v, want exactly 1 (reservation counted once)", automation)
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	found := false
	for _, item := range inbox {
		if item.EntryID == entry.ID {
			found = true
			if item.ProcessedAt == nil {
				t.Errorf("reconciled ping not processed: %+v", item)
			}
		}
	}
	if !found {
		t.Errorf("reconciled ping missing from inbox: %+v", inbox)
	}
}

func TestBoardPostRejectsUnregisteredWith(t *testing.T) {
	setupBoardCLI(t)
	err := runBoardPost(context.Background(), &bytes.Buffer{}, boardPostOptions{
		Target: boardTarget{AgentName: "valet"},
		Text:   "invalid invite",
		With:   []string{"ghost"},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("runBoardPost(unknown with) error = %v", err)
	}
}

func TestBoardShowLimitOverflow(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "limit",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Text:   "reply",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "second reply",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	var out bytes.Buffer
	if err := runBoardShow(context.Background(), &out, boardShowOptions{
		Target: valet,
		Post:   result.Post.ID,
		Limit:  math.MaxInt,
	}); err != nil {
		t.Fatalf("runBoardShow(max limit) error: %v", err)
	}
	if !strings.Contains(out.String(), "reply") {
		t.Errorf("show output = %q", out.String())
	}

	var jsonOut bytes.Buffer
	if err := runBoardShow(context.Background(), &jsonOut, boardShowOptions{
		Target: valet,
		Post:   result.Post.ID,
		Limit:  1,
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardShow(json limit) error: %v", err)
	}
	var view board.PostView
	if err := json.Unmarshal(jsonOut.Bytes(), &view); err != nil {
		t.Fatalf("decode limited view: %v\n%s", err, jsonOut.String())
	}
	if len(view.Entries) != 2 || view.Entries[0].Kind != board.EntryPost || view.Entries[1].Text != "second reply" {
		t.Errorf("limited JSON entries = %+v, want post + second reply", view.Entries)
	}
}

func TestBoardInboxRetryReconcilesMissing(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "lost enqueue",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(ctx, &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	st := openBoardTestStore(t)
	entry, err := st.AppendMessage(ctx, board.AgentActor("valet"), result.Post.ID, board.AppendMessageInput{
		Text: "missing ping",
	})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}

	var out bytes.Buffer
	if err := runBoardInbox(ctx, &out, boardInboxOptions{
		Target: scribe,
		Retry:  true,
	}); err != nil {
		t.Fatalf("runBoardInbox(retry) error: %v", err)
	}
	if !strings.Contains(out.String(), "Recovered 1 missing pings") {
		t.Errorf("output = %q", out.String())
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	found := false
	for _, item := range inbox {
		if item.EntryID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("reconciled entry missing from inbox: %+v", inbox)
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
	all, err := st.Inbox(context.Background(), "scribe", true)
	if err != nil {
		t.Fatalf("Inbox(all) error: %v", err)
	}
	if len(all) != 1 || all[0].PromotionSessionID != "ses_cli" || all[0].ReadAt == nil {
		t.Errorf("promoted inbox row = %+v", all)
	}
}

func TestBoardInboxPromoteSkipsAutomationSession(t *testing.T) {
	_, scribeWS := setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "main session",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(ctx, &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(ctx, &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	perAgent, err := store.Open(scribeWS)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	if _, err := perAgent.Orch().UpsertSession(
		"board:"+result.Post.ID+":scribe", "ses_board", time.Now().UTC(),
	); err != nil {
		perAgent.Close()
		t.Fatalf("UpsertSession() error: %v", err)
	}
	if err := perAgent.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if err := runBoardInboxPromote(ctx, &bytes.Buffer{}, boardInboxPromoteOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardInboxPromote() error: %v", err)
	}
	if fake.synthCalls != 1 {
		t.Fatalf("promotion turns = %d, want 1", fake.synthCalls)
	}
	st := openBoardTestStore(t)
	all, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(all) != 1 || all[0].PromotionSessionID != "ses_cli" {
		t.Fatalf("promotion session = %+v, want a fresh main session", all)
	}
	perAgent, err = store.Open(scribeWS)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	defer perAgent.Close()
	session, ok, err := perAgent.Orch().GetSessionByThread("promote:scribe")
	if err != nil {
		t.Fatalf("GetSessionByThread() error: %v", err)
	}
	if !ok || session.OpenCodeSessionID != "ses_cli" {
		t.Errorf("promote mapping = %+v, ok=%v", session, ok)
	}
}

func TestBoardInboxPromoteRejectsAutomationSession(t *testing.T) {
	_, scribeWS := setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "reject board session",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(ctx, &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(ctx, &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	perAgent, err := store.Open(scribeWS)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	if _, err := perAgent.Orch().UpsertSession(
		"board:"+result.Post.ID+":scribe", "ses_board", time.Now().UTC(),
	); err != nil {
		perAgent.Close()
		t.Fatalf("UpsertSession() error: %v", err)
	}
	if err := perAgent.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	err = runBoardInboxPromote(ctx, &bytes.Buffer{}, boardInboxPromoteOptions{
		Target:    scribe,
		Post:      result.Post.ID,
		SessionID: "ses_board",
	})
	if err == nil || !strings.Contains(err.Error(), "board automation session") {
		t.Fatalf("runBoardInboxPromote(board session) error = %v", err)
	}
}

func TestBoardInboxPromoteRejectsForeignSession(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "foreign session",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(ctx, &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(ctx, &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	err := runBoardInboxPromote(ctx, &bytes.Buffer{}, boardInboxPromoteOptions{
		Target:    scribe,
		Post:      result.Post.ID,
		SessionID: "ses_foreign",
	})
	if err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("runBoardInboxPromote(foreign session) error = %v", err)
	}
}

func TestBoardInboxPromoteReconcilesStaleClaim(t *testing.T) {
	setupBoardCLI(t)
	fake := &fakeCLITurn{}
	withFakeTurn(t, fake)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	ctx := context.Background()

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "promotion retry",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(ctx, &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(ctx, &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}

	st := openBoardTestStore(t)
	token := "promote-token"
	marker := boardPromotionMarker(token)
	entryIDs, err := st.ClaimPromotion(ctx, "scribe", result.Post.ID, token, marker)
	if err != nil || len(entryIDs) != 1 {
		t.Fatalf("ClaimPromotion() = %v, %v", entryIDs, err)
	}
	if err := st.MarkPromotionSession(ctx, "scribe", token, "ses_cli"); err != nil {
		t.Fatalf("MarkPromotionSession() error: %v", err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_inbox SET promotion_claimed_at = ? WHERE agent_id = 'scribe' AND post_id = ?`,
		time.Now().Add(-time.Hour).UTC(), result.Post.ID,
	); err != nil {
		t.Fatalf("age promotion claim: %v", err)
	}
	fake.bySession = map[string][]opencode.Message{
		"ses_cli": {{ID: "msg_promoted", Type: "synthetic", Text: "promoted " + marker}},
	}

	var out bytes.Buffer
	if err := runBoardInboxPromote(ctx, &out, boardInboxPromoteOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardInboxPromote() error: %v", err)
	}
	if fake.synthCalls != 0 {
		t.Errorf("stale promotion admitted again: %d calls", fake.synthCalls)
	}
	if !strings.Contains(out.String(), "No unread pings") {
		t.Errorf("output = %q", out.String())
	}
	inbox, err := st.Inbox(ctx, "scribe", false)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("unread inbox after reconcile = %+v", inbox)
	}
}
