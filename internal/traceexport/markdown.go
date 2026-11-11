package traceexport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
)

type sessionDoc struct {
	Info      opencode.SessionInfo
	Mapping   orch.Session
	Messages  []opencode.Message
	Truncated bool
	Inbox     []commtg.InboxItem
	Outbox    []commtg.OutboxItem
}

type traceMessage struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Time        traceTime      `json:"time"`
	Text        string         `json:"text"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	Agent       string         `json:"agent"`
	Model       any            `json:"model"`
	Content     []tracePart    `json:"content"`
	Cost        any            `json:"cost"`
	Tokens      any            `json:"tokens"`
	Finish      string         `json:"finish"`
	Outcome     string         `json:"outcome"`
	Error       any            `json:"error"`
	Command     string         `json:"command"`
	Status      string         `json:"status"`
	Exit        any            `json:"exit"`
	Output      any            `json:"output"`
	Skill       any            `json:"skill"`
	Name        string         `json:"name"`
	Files       []any          `json:"files"`
	Agents      []any          `json:"agents"`
	Skills      []any          `json:"skills"`
}

type traceTime struct {
	Created   float64 `json:"created"`
	Completed float64 `json:"completed"`
	Streamed  float64 `json:"streamed"`
}

type tracePart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Name     string          `json:"name"`
	ID       string          `json:"id"`
	Executed bool            `json:"executed"`
	State    json.RawMessage `json:"state"`
	Time     tracePartTime   `json:"time"`
}

type tracePartTime struct {
	Created   float64 `json:"created"`
	Ran       float64 `json:"ran"`
	Completed float64 `json:"completed"`
}

type traceEvent struct {
	timestamp time.Time
	priority  int
	id        string
	message   *opencode.Message
	inbound   *commtg.InboxItem
	outbound  *commtg.OutboxItem
}

func renderSession(doc sessionDoc) string {
	var builder strings.Builder
	sessionID := doc.Info.ID
	if sessionID == "" {
		sessionID = doc.Mapping.OpenCodeSessionID
	}
	builder.WriteString("# Session `" + sessionID + "`\n\n")

	mapping := doc.Mapping
	thread := mapping.Thread
	if thread == "" {
		thread = "(unmapped)"
	}
	fmt.Fprintf(&builder, "- Agent: `%s`\n", doc.Info.Agent)
	fmt.Fprintf(&builder, "- Thread: `%s`\n", thread)
	fmt.Fprintf(&builder, "- Workspace: `%s`\n", doc.Info.Location.Directory)
	if doc.Info.Title != "" {
		fmt.Fprintf(&builder, "- Title: %s\n", doc.Info.Title)
	}
	if created := sessionTime(doc.Info.Time.Created); !created.IsZero() {
		fmt.Fprintf(&builder, "- Created: %s\n", created.Format(time.RFC3339))
	}
	if updated := sessionTime(doc.Info.Time.Updated); !updated.IsZero() {
		fmt.Fprintf(&builder, "- Updated: %s\n", updated.Format(time.RFC3339))
	}
	fmt.Fprintf(&builder, "- Messages: %d\n", len(doc.Messages))
	if doc.Truncated {
		builder.WriteString("- Truncated: yes (the message page cap was reached)\n")
	}
	builder.WriteString("\n")

	if len(doc.Info.Raw) > 0 {
		builder.WriteString("<details><summary>Session projection JSON</summary>\n\n```json\n")
		builder.WriteString(prettyJSON(doc.Info.Raw))
		builder.WriteString("\n```\n\n</details>\n\n")
	}

	events := sessionEvents(doc)
	builder.WriteString("## Timeline\n\n")
	if len(events) == 0 {
		builder.WriteString("_No messages or communication events were recorded._\n")
		return builder.String()
	}
	for index, event := range events {
		number := index + 1
		switch {
		case event.inbound != nil:
			renderInbound(&builder, number, *event.inbound)
		case event.outbound != nil:
			renderOutbound(&builder, number, *event.outbound)
		case event.message != nil:
			renderMessage(&builder, number, *event.message)
		}
	}
	return builder.String()
}

func sessionEvents(doc sessionDoc) []traceEvent {
	events := make([]traceEvent, 0, len(doc.Messages)+len(doc.Inbox)+len(doc.Outbox))
	messageTexts := make(map[string]string, len(doc.Messages))
	for index := range doc.Messages {
		message := &doc.Messages[index]
		messageTexts[message.ID] = messageText(*message)
		events = append(events, traceEvent{
			timestamp: messageTimestamp(*message),
			priority:  messagePriority(message.Type),
			id:        message.ID,
			message:   message,
		})
	}
	for index := range doc.Inbox {
		item := &doc.Inbox[index]
		events = append(events, traceEvent{
			timestamp: item.ReceivedAt,
			priority:  0,
			id:        item.EnvelopeID,
			inbound:   item,
		})
	}
	for index := range doc.Outbox {
		item := &doc.Outbox[index]
		if strings.TrimSpace(item.Text) == "" {
			if text := messageTexts[item.SourceMessageID]; strings.TrimSpace(text) != "" {
				clone := *item
				clone.Text = text
				item = &clone
			}
		}
		timestamp := item.CreatedAt
		if item.SentAt != nil {
			timestamp = *item.SentAt
		}
		events = append(events, traceEvent{
			timestamp: timestamp,
			priority:  3,
			id:        item.EnvelopeID,
			outbound:  item,
		})
	}
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i], events[j]
		if !left.timestamp.Equal(right.timestamp) {
			return left.timestamp.Before(right.timestamp)
		}
		if left.priority != right.priority {
			return left.priority < right.priority
		}
		return left.id < right.id
	})
	return events
}

func renderMessage(builder *strings.Builder, number int, message opencode.Message) {
	raw := message.Raw
	if len(raw) == 0 {
		raw, _ = json.Marshal(message)
	}
	var trace traceMessage
	_ = json.Unmarshal(raw, &trace)
	messageType := trace.Type
	if messageType == "" {
		messageType = message.Type
	}

	fmt.Fprintf(builder, "## %d. %s `%s`", number, messageType, message.ID)
	if timestamp := messageTimestamp(message); !timestamp.IsZero() {
		fmt.Fprintf(builder, " — %s", timestamp.Format(time.RFC3339Nano))
	}
	builder.WriteString("\n\n")

	switch messageType {
	case "assistant":
		renderAssistant(builder, trace, message)
	case "user":
		renderInput(builder, trace, false)
	case "synthetic":
		renderInput(builder, trace, true)
	case "system":
		writeText(builder, trace.Text)
		if trace.Description != "" {
			fmt.Fprintf(builder, "_%s_\n\n", trace.Description)
		}
	case "skill":
		if trace.Name != "" {
			fmt.Fprintf(builder, "**Skill:** `%s`\n\n", trace.Name)
		}
		if trace.Skill != nil {
			fmt.Fprintf(builder, "**Skill ID:** `%s`\n\n", formatAny(trace.Skill))
		}
		writeText(builder, trace.Text)
	case "shell":
		if trace.Command != "" {
			fmt.Fprintf(builder, "**Command:** `%s`\n\n", trace.Command)
		}
		if trace.Status != "" {
			fmt.Fprintf(builder, "**Status:** %s\n\n", trace.Status)
		}
		if trace.Exit != nil {
			fmt.Fprintf(builder, "**Exit:** %s\n\n", formatAny(trace.Exit))
		}
		if trace.Output != nil {
			builder.WriteString("**Output:**\n\n")
			builder.WriteString(prettyAny(trace.Output))
			builder.WriteString("\n\n")
		}
		writeText(builder, trace.Text)
	case "idle":
		if trace.Outcome != "" {
			fmt.Fprintf(builder, "**Outcome:** %s\n\n", trace.Outcome)
		}
		if trace.Error != nil {
			builder.WriteString("**Error:**\n\n")
			builder.WriteString(prettyAny(trace.Error))
			builder.WriteString("\n\n")
		}
	case "compaction":
		writeText(builder, trace.Text)
	default:
		writeText(builder, trace.Text)
		if trace.Description != "" {
			fmt.Fprintf(builder, "_%s_\n\n", trace.Description)
		}
	}

	builder.WriteString("<details><summary>Raw message JSON</summary>\n\n```json\n")
	builder.WriteString(prettyJSON(raw))
	builder.WriteString("\n```\n\n</details>\n\n")
}

func renderAssistant(builder *strings.Builder, trace traceMessage, message opencode.Message) {
	var meta []string
	if trace.Agent != "" {
		meta = append(meta, "Agent: `"+trace.Agent+"`")
	}
	if trace.Model != nil {
		meta = append(meta, "Model: `"+formatAny(trace.Model)+"`")
	}
	if trace.Finish != "" {
		meta = append(meta, "Finish: `"+trace.Finish+"`")
	}
	if trace.Cost != nil {
		meta = append(meta, "Cost: `"+formatAny(trace.Cost)+"`")
	}
	if trace.Tokens != nil {
		meta = append(meta, "Tokens: `"+formatAny(trace.Tokens)+"`")
	}
	if len(meta) > 0 {
		builder.WriteString(strings.Join(meta, " · "))
		builder.WriteString("\n\n")
	}
	if len(trace.Content) == 0 {
		writeText(builder, firstNonEmpty(trace.Text, messageText(message)))
		return
	}
	for _, part := range trace.Content {
		switch part.Type {
		case "text":
			writeText(builder, part.Text)
		case "reasoning":
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			builder.WriteString("<details><summary>Reasoning</summary>\n\n")
			builder.WriteString(part.Text)
			builder.WriteString("\n\n</details>\n\n")
		case "tool":
			renderTool(builder, part)
		default:
			if strings.TrimSpace(part.Text) != "" {
				writeText(builder, part.Text)
			} else {
				builder.WriteString("**" + part.Type + "**\n\n")
			}
		}
	}
}

func renderTool(builder *strings.Builder, part tracePart) {
	var state struct {
		Status   string            `json:"status"`
		Input    json.RawMessage   `json:"input"`
		Content  []json.RawMessage `json:"content"`
		Metadata json.RawMessage   `json:"metadata"`
		Error    json.RawMessage   `json:"error"`
	}
	if len(part.State) > 0 {
		_ = json.Unmarshal(part.State, &state)
	}
	status := state.Status
	if status == "" {
		status = "unknown"
	}
	fmt.Fprintf(builder, "**Tool call `%s`** — %s\n\n", part.Name, status)
	if len(state.Input) > 0 {
		builder.WriteString("Input:\n\n```json\n")
		builder.WriteString(prettyJSON(state.Input))
		builder.WriteString("\n```\n\n")
	}
	if len(state.Content) > 0 {
		builder.WriteString("Output:\n\n")
		for _, content := range state.Content {
			var item struct {
				Type string  `json:"type"`
				Text string  `json:"text"`
				URI  string  `json:"uri"`
				Mime string  `json:"mime"`
				Name *string `json:"name"`
			}
			if err := json.Unmarshal(content, &item); err != nil {
				builder.WriteString(prettyJSON(content))
				builder.WriteString("\n\n")
				continue
			}
			switch item.Type {
			case "text":
				builder.WriteString(item.Text)
				builder.WriteString("\n\n")
			case "file":
				name := item.URI
				if item.Name != nil && *item.Name != "" {
					name = *item.Name
				}
				fmt.Fprintf(builder, "- File: `%s` (%s, `%s`)\n", name, item.Mime, item.URI)
			default:
				builder.WriteString(prettyJSON(content))
				builder.WriteString("\n\n")
			}
		}
		builder.WriteString("\n")
	}
	if len(state.Error) > 0 && string(state.Error) != "null" {
		builder.WriteString("Error:\n\n```json\n")
		builder.WriteString(prettyJSON(state.Error))
		builder.WriteString("\n```\n\n")
	}
	if len(state.Metadata) > 0 && string(state.Metadata) != "null" && string(state.Metadata) != "{}" {
		builder.WriteString("<details><summary>Tool metadata</summary>\n\n```json\n")
		builder.WriteString(prettyJSON(state.Metadata))
		builder.WriteString("\n```\n\n</details>\n\n")
	}
	if !part.Executed && status == "completed" {
		builder.WriteString("_Tool was not executed (cached/denied)._\n\n")
	}
}

func renderInput(builder *strings.Builder, trace traceMessage, synthetic bool) {
	if synthetic && trace.Description != "" {
		fmt.Fprintf(builder, "_%s_\n\n", trace.Description)
	}
	writeText(builder, trace.Text)
	if len(trace.Files) > 0 {
		builder.WriteString("**Files:**\n\n")
		builder.WriteString(prettyAny(trace.Files))
		builder.WriteString("\n\n")
	}
	if len(trace.Agents) > 0 {
		builder.WriteString("**Agents:**\n\n")
		builder.WriteString(prettyAny(trace.Agents))
		builder.WriteString("\n\n")
	}
	if len(trace.Skills) > 0 {
		builder.WriteString("**Skills:**\n\n")
		builder.WriteString(prettyAny(trace.Skills))
		builder.WriteString("\n\n")
	}
	if len(trace.Metadata) > 0 {
		builder.WriteString("<details><summary>Metadata</summary>\n\n```json\n")
		builder.WriteString(prettyAny(trace.Metadata))
		builder.WriteString("\n```\n\n</details>\n\n")
	}
}

func renderInbound(builder *strings.Builder, number int, item commtg.InboxItem) {
	timestamp := item.ReceivedAt
	fmt.Fprintf(builder, "## %d. communication inbound `%s`", number, item.EnvelopeID)
	if !timestamp.IsZero() {
		fmt.Fprintf(builder, " — %s", timestamp.Format(time.RFC3339Nano))
	}
	builder.WriteString("\n\n")
	fmt.Fprintf(builder, "- Chat: `%d`\n", item.ChatID)
	fmt.Fprintf(builder, "- Telegram update: `%d`\n", item.TGUpdateID)
	if item.OpenCodeMessageID != "" {
		fmt.Fprintf(builder, "- Linked OpenCode message: `%s`\n", item.OpenCodeMessageID)
	}
	if item.MediaPath != "" {
		fmt.Fprintf(builder, "- Media: `%s`\n", item.MediaPath)
	}
	if item.ProcessedAt != nil {
		fmt.Fprintf(builder, "- Processed: %s\n", item.ProcessedAt.Format(time.RFC3339))
	} else {
		builder.WriteString("- Processed: no\n")
	}
	if item.Attempts > 0 {
		fmt.Fprintf(builder, "- Attempts: %d\n", item.Attempts)
	}
	if item.LastError != "" {
		fmt.Fprintf(builder, "- Last error: %s\n", item.LastError)
	}
	builder.WriteString("\n<details><summary>Raw routing JSON</summary>\n\n```json\n")
	raw, _ := json.MarshalIndent(item, "", "  ")
	builder.Write(raw)
	builder.WriteString("\n```\n\n</details>\n\n")
}

func renderOutbound(builder *strings.Builder, number int, item commtg.OutboxItem) {
	timestamp := item.CreatedAt
	if item.SentAt != nil {
		timestamp = *item.SentAt
	}
	fmt.Fprintf(builder, "## %d. communication outbound `%s`", number, item.EnvelopeID)
	if !timestamp.IsZero() {
		fmt.Fprintf(builder, " — %s", timestamp.Format(time.RFC3339Nano))
	}
	builder.WriteString("\n\n")
	fmt.Fprintf(builder, "- Chat: `%d`\n", item.ChatID)
	fmt.Fprintf(builder, "- Delivery: %s\n", item.Delivery)
	if item.TGMessageID != nil {
		fmt.Fprintf(builder, "- Telegram message: `%d`\n", *item.TGMessageID)
	}
	if item.Attempts > 0 {
		fmt.Fprintf(builder, "- Attempts: %d\n", item.Attempts)
	}
	if item.SourceMessageID != "" {
		fmt.Fprintf(builder, "- Linked OpenCode message: `%s`\n", item.SourceMessageID)
	}
	if item.LastError != "" {
		fmt.Fprintf(builder, "- Last error: %s\n", item.LastError)
	}
	if strings.TrimSpace(item.Text) != "" {
		builder.WriteString("\n")
		writeText(builder, item.Text)
	} else {
		builder.WriteString("\n")
	}
	builder.WriteString("<details><summary>Raw routing JSON</summary>\n\n```json\n")
	raw, _ := json.MarshalIndent(item, "", "  ")
	builder.Write(raw)
	builder.WriteString("\n```\n\n</details>\n\n")
}

func renderLogs(entries []orch.LogEntry) string {
	var builder strings.Builder
	builder.WriteString("# Orchestrator logs\n\n")
	fmt.Fprintf(&builder, "- Entries: %d\n\n", len(entries))
	if len(entries) == 0 {
		builder.WriteString("_No orchestrator log entries were recorded._\n")
		return builder.String()
	}
	for _, entry := range entries {
		header := entry.CreatedAt.Format(time.RFC3339Nano)
		if entry.Level != "" {
			header += " · " + entry.Level
		}
		if entry.Source != "" {
			header += " · " + entry.Source
		}
		fmt.Fprintf(&builder, "## %s · #%d\n\n", header, entry.ID)
		if entry.Message != "" {
			builder.WriteString(entry.Message)
			builder.WriteString("\n\n")
		}
		if entry.Meta != "" && entry.Meta != "{}" && entry.Meta != "null" {
			builder.WriteString("<details><summary>Metadata</summary>\n\n```json\n")
			builder.WriteString(prettyJSON([]byte(entry.Meta)))
			builder.WriteString("\n```\n\n</details>\n\n")
		}
	}
	return builder.String()
}

func writeText(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	builder.WriteString(text)
	builder.WriteString("\n\n")
}

func prettyJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func prettyAny(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func formatAny(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func messageTimestamp(message opencode.Message) time.Time {
	if message.Time.Created <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(message.Time.Created)).UTC()
}

func messagePriority(messageType string) int {
	switch messageType {
	case "user", "synthetic":
		return 1
	case "assistant":
		return 2
	default:
		return 4
	}
}

func messageText(message opencode.Message) string {
	if strings.TrimSpace(message.Text) != "" {
		return message.Text
	}
	var parts []string
	for _, part := range message.Content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
