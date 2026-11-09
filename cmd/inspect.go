package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
	"github.com/spf13/cobra"
)

var (
	inspectSessionID  string
	inspectAgent      string
	inspectWorkspace  string
	inspectNoOpenCode bool
	inspectNoComm     bool
	inspectLimit      int
	inspectNDJSON     bool
)

type inspectOptions struct {
	SessionID  string
	AgentName  string
	Workspace  string
	NoOpenCode bool
	NoComm     bool
	Limit      int
	NDJSON     bool
}

type inspectOutput struct {
	Agent           string         `json:"agent"`
	Workspace       string         `json:"workspace"`
	AcolyteDB       string         `json:"acolyte_db"`
	OpenCodeService string         `json:"opencode_service,omitempty"`
	SessionID       string         `json:"session_id"`
	Thread          string         `json:"thread,omitempty"`
	Events          []inspectEvent `json:"events"`
	Warnings        []string       `json:"warnings,omitempty"`
	Truncated       bool           `json:"opencode_truncated,omitempty"`
}

type inspectEvent struct {
	Seq             int       `json:"seq"`
	Timestamp       time.Time `json:"timestamp"`
	Source          string    `json:"source"`
	Kind            string    `json:"kind"`
	ID              string    `json:"id,omitempty"`
	Text            string    `json:"text,omitempty"`
	ChatID          int64     `json:"chat_id,omitempty"`
	TGUpdateID      int64     `json:"tg_update_id,omitempty"`
	Media           []string  `json:"media,omitempty"`
	Delivery        string    `json:"delivery,omitempty"`
	Attempts        int       `json:"attempts,omitempty"`
	TGMessageID     *int64    `json:"tg_message_id,omitempty"`
	LinkedMessageID string    `json:"linked_opencode_message_id,omitempty"`
	Error           string    `json:"error,omitempty"`
}

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Merge an OpenCode session with communication routing events",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspect(cmd.Context(), cmd.OutOrStdout(), inspectOptions{
			SessionID:  inspectSessionID,
			AgentName:  inspectAgent,
			Workspace:  inspectWorkspace,
			NoOpenCode: inspectNoOpenCode,
			NoComm:     inspectNoComm,
			Limit:      inspectLimit,
			NDJSON:     inspectNDJSON,
		})
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)
	inspectCmd.Flags().StringVar(&inspectSessionID, "session-id", "", "OpenCode session id to inspect")
	inspectCmd.Flags().StringVar(&inspectAgent, "agent-name", "", "target agent name")
	inspectCmd.Flags().StringVar(&inspectWorkspace, "workspace-folder", "", "target agent workspace")
	inspectCmd.Flags().BoolVar(&inspectNoOpenCode, "no-opencode", false, "skip OpenCode session messages")
	inspectCmd.Flags().BoolVar(&inspectNoComm, "no-comm", false, "skip communication routing events")
	inspectCmd.Flags().IntVar(&inspectLimit, "limit", 100, "maximum OpenCode message pages")
	inspectCmd.Flags().BoolVar(&inspectNDJSON, "ndjson", false, "print one event per line instead of one JSON object")
}

func runInspect(ctx context.Context, w io.Writer, opts inspectOptions) error {
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	if opts.SessionID == "" {
		return fmt.Errorf("--session-id is required")
	}
	agent, session, st, err := findInspectTarget(opts.SessionID, opts.AgentName, opts.Workspace)
	if err != nil {
		return err
	}
	defer st.Close()

	out := inspectOutput{
		Agent:     agent.Name,
		Workspace: agent.Workspace,
		AcolyteDB: st.Path(),
		SessionID: opts.SessionID,
		Thread:    session.Thread,
	}
	if session.Thread == "" {
		out.Warnings = append(out.Warnings, "session is not mapped to an agent thread; communication events are unavailable")
	}

	if !opts.NoComm && out.Thread != "" {
		chatID, ok := envelope.ParseTelegramThread(out.Thread)
		if !ok {
			out.Warnings = append(out.Warnings, fmt.Sprintf("thread %q is not a Telegram thread", out.Thread))
		} else {
			commEvents, warnings := communicationEvents(st, chatID, opts.SessionID)
			out.Events = append(out.Events, commEvents...)
			out.Warnings = append(out.Warnings, warnings...)
		}
	}

	if !opts.NoOpenCode {
		if serviceURL, err := openCodeServiceURL(ctx); err == nil {
			out.OpenCodeService = serviceURL
		}
		client, err := openCodeEnsure(ctx)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("OpenCode unavailable: %v", err))
		} else {
			messages, truncated, err := client.SessionMessages(ctx, opts.SessionID, opts.Limit)
			if err != nil {
				out.Warnings = append(out.Warnings, fmt.Sprintf("read OpenCode messages: %v", err))
			} else {
				out.Truncated = truncated
				out.Events = append(out.Events, openCodeEvents(messages)...)
			}
		}
	}

	sort.SliceStable(out.Events, func(i, j int) bool {
		left, right := out.Events[i], out.Events[j]
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.Before(right.Timestamp)
		}
		lp, rp := eventPriority(left), eventPriority(right)
		if lp != rp {
			return lp < rp
		}
		return left.ID < right.ID
	})
	for index := range out.Events {
		out.Events[index].Seq = index + 1
	}

	if opts.NDJSON {
		encoder := json.NewEncoder(w)
		for _, event := range out.Events {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		return nil
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(raw))
	return nil
}

func findInspectTarget(sessionID, agentName, workspace string) (registry.Agent, orch.Session, *store.Store, error) {
	if strings.TrimSpace(agentName) != "" || strings.TrimSpace(workspace) != "" {
		agent, err := resolveTargetAgent(agentName, workspace)
		if err != nil {
			return registry.Agent{}, orch.Session{}, nil, err
		}
		st, err := store.OpenReadOnly(agent.Workspace)
		if err != nil {
			return registry.Agent{}, orch.Session{}, nil, err
		}
		session, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID)
		if err != nil {
			st.Close()
			return registry.Agent{}, orch.Session{}, nil, err
		}
		if !ok {
			return agent, orch.Session{}, st, nil
		}
		return agent, session, st, nil
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		return registry.Agent{}, orch.Session{}, nil, err
	}
	defer reg.Close()
	agents, err := reg.List()
	if err != nil {
		return registry.Agent{}, orch.Session{}, nil, err
	}
	for _, agent := range agents {
		st, err := store.OpenReadOnly(agent.Workspace)
		if err != nil {
			continue
		}
		session, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID)
		if err == nil && ok {
			return agent, session, st, nil
		}
		st.Close()
	}
	return registry.Agent{}, orch.Session{}, nil, fmt.Errorf("session %q is not mapped to any registered agent; use --agent-name or --workspace-folder", sessionID)
}

func communicationEvents(st *store.Store, chatID int64, sessionID string) ([]inspectEvent, []string) {
	var events []inspectEvent
	var warnings []string
	inbox, err := st.CommTG().ListInbox(chatID)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("read communication inbox: %v", err))
	} else {
		for _, item := range inbox {
			if item.OpenCodeSessionID != sessionID {
				continue
			}
			event := inspectEvent{
				Timestamp:       item.ReceivedAt,
				Source:          "communication",
				Kind:            "inbound",
				ID:              item.EnvelopeID,
				ChatID:          item.ChatID,
				TGUpdateID:      item.TGUpdateID,
				LinkedMessageID: item.OpenCodeMessageID,
			}
			if item.MediaPath != "" {
				event.Media = []string{item.MediaPath}
			}
			events = append(events, event)
		}
	}
	outbox, err := st.CommTG().ListOutbox(chatID)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("read communication outbox: %v", err))
	} else {
		for _, item := range outbox {
			if item.OpenCodeSessionID != sessionID {
				continue
			}
			timestamp := item.CreatedAt
			if item.SentAt != nil {
				timestamp = *item.SentAt
			}
			events = append(events, inspectEvent{
				Timestamp:       timestamp,
				Source:          "communication",
				Kind:            "outbound",
				ID:              item.EnvelopeID,
				Text:            item.Text,
				ChatID:          item.ChatID,
				Delivery:        item.Delivery,
				Attempts:        item.Attempts,
				TGMessageID:     item.TGMessageID,
				LinkedMessageID: item.SourceMessageID,
				Error:           item.LastError,
			})
		}
	}
	return events, warnings
}

func openCodeEvents(messages []opencode.Message) []inspectEvent {
	events := make([]inspectEvent, 0, len(messages))
	for _, message := range messages {
		events = append(events, inspectEvent{
			Timestamp: messageTime(message),
			Source:    "opencode",
			Kind:      message.Type,
			ID:        message.ID,
			Text:      messageText(message),
		})
	}
	return events
}

func messageTime(message opencode.Message) time.Time {
	if message.Time.Created <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(message.Time.Created)).UTC()
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

func eventPriority(event inspectEvent) int {
	switch {
	case event.Source == "communication" && event.Kind == "inbound":
		return 0
	case event.Source == "opencode" && (event.Kind == "user" || event.Kind == "synthetic"):
		return 1
	case event.Source == "opencode" && event.Kind == "assistant":
		return 2
	case event.Source == "communication" && event.Kind == "outbound":
		return 3
	default:
		return 4
	}
}
