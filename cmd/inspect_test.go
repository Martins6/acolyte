package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
)

func TestInspectMergesCommunicationAndOpenCode(t *testing.T) {
	ws := setupCronCLI(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", now); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	if err := st.CommTG().UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: 1, ChatID: 10, EnvelopeID: "env_in", OpenCodeSessionID: "ses_x", OpenCodeMessageID: "msg_user", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("RecordInbound() error: %v", err)
	}
	if _, err := st.CommTG().RecordInbound(commtg.InboxItem{
		TGUpdateID: 2, ChatID: 10, EnvelopeID: "env_other", OpenCodeSessionID: "ses_other", OpenCodeMessageID: "msg_other", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("RecordInbound(other) error: %v", err)
	}
	sentAt := now.Add(3 * time.Millisecond)
	item, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_out", ChatID: 10, Text: "reply", OpenCodeSessionID: "ses_x",
		SourceMessageID: "msg_assistant", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("EnqueueOutbound() error: %v", err)
	}
	if err := st.CommTG().MarkOutboundSent(item.ID, 1, 123, sentAt); err != nil {
		t.Fatalf("MarkOutboundSent() error: %v", err)
	}
	if _, err := st.CommTG().EnqueueOutbound(commtg.OutboxItem{
		EnvelopeID: "env_other_out", ChatID: 10, Text: "other", OpenCodeSessionID: "ses_other",
		SourceMessageID: "msg_other", CreatedAt: now,
	}); err != nil {
		t.Fatalf("EnqueueOutbound(other) error: %v", err)
	}
	st.Close()

	fake := &fakeCLITurn{messages: []opencode.Message{
		{ID: "msg_user", Type: "user", Text: "hello", Time: opencode.MessageTime{Created: float64(now.Add(time.Millisecond).UnixMilli())}},
		{ID: "msg_assistant", Type: "assistant", Content: []opencode.ContentPart{{Type: "text", Text: "reply"}}, Time: opencode.MessageTime{Created: float64(now.Add(2 * time.Millisecond).UnixMilli())}},
	}}
	withFakeTurn(t, fake)
	oldURL := openCodeServiceURL
	openCodeServiceURL = func(context.Context) (string, error) { return "http://127.0.0.1:49374", nil }
	t.Cleanup(func() { openCodeServiceURL = oldURL })

	var out bytes.Buffer
	if err := runInspect(context.Background(), &out, inspectOptions{SessionID: "ses_x", AgentName: "valet"}); err != nil {
		t.Fatalf("runInspect() error: %v", err)
	}
	var result inspectOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if result.Agent != "valet" || result.SessionID != "ses_x" || result.Thread != "telegram:10" {
		t.Errorf("result = %+v", result)
	}
	if result.OpenCodeService != "http://127.0.0.1:49374" {
		t.Errorf("service = %q", result.OpenCodeService)
	}
	if len(result.Events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(result.Events), result.Events)
	}
	kinds := []string{result.Events[0].Kind, result.Events[1].Kind, result.Events[2].Kind, result.Events[3].Kind}
	if strings.Join(kinds, ",") != "inbound,user,assistant,outbound" {
		t.Errorf("event kinds = %v", kinds)
	}
	if result.Events[0].LinkedMessageID != "msg_user" {
		t.Errorf("inbound link = %q", result.Events[0].LinkedMessageID)
	}
	if result.Events[3].LinkedMessageID != "msg_assistant" {
		t.Errorf("outbound link = %q", result.Events[3].LinkedMessageID)
	}
	if result.Events[2].Text != "reply" {
		t.Errorf("assistant text = %q", result.Events[2].Text)
	}
}

func TestInspectNoComm(t *testing.T) {
	ws := setupCronCLI(t)
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Orch().UpsertSession(envelope.TelegramThread(10), "ses_x", time.Now().UTC()); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	st.Close()

	fake := &fakeCLITurn{messages: []opencode.Message{{ID: "msg_user", Type: "user", Text: "hello"}}}
	withFakeTurn(t, fake)
	oldURL := openCodeServiceURL
	openCodeServiceURL = func(context.Context) (string, error) { return "http://127.0.0.1:49374", nil }
	t.Cleanup(func() { openCodeServiceURL = oldURL })

	var out bytes.Buffer
	if err := runInspect(context.Background(), &out, inspectOptions{SessionID: "ses_x", AgentName: "valet", NoComm: true}); err != nil {
		t.Fatalf("runInspect() error: %v", err)
	}
	var result inspectOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(result.Events) != 1 || result.Events[0].Source != "opencode" {
		t.Errorf("events = %+v", result.Events)
	}
}

func TestInspectUnknownSession(t *testing.T) {
	setupCronCLI(t)
	if err := runInspect(context.Background(), &bytes.Buffer{}, inspectOptions{SessionID: "ses_missing"}); err == nil {
		t.Fatal("runInspect(unknown) error = nil, want error")
	}
}

func TestInspectRejectsUnmappedExplicitAgent(t *testing.T) {
	setupCronCLI(t)
	err := runInspect(context.Background(), &bytes.Buffer{}, inspectOptions{SessionID: "ses_missing", AgentName: "valet"})
	if err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("runInspect(unmapped explicit agent) error = %v, want not-mapped error", err)
	}
}
