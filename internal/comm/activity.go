package comm

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

const (
	// activityFieldMax caps each identifier copied out of an event payload.
	activityFieldMax = 256
	// activityMetaMax caps the persisted meta JSON as a last line of defense.
	activityMetaMax = 2048
)

// ActivityRecorder persists one orch_logs row per tool event so trace export
// and learning feeds can see what a turn did. It is intentionally narrow: only
// tool lifecycle events are recorded, and only their identifiers. Tool input,
// results, prompts, and text/reasoning deltas are never persisted.
type ActivityRecorder struct {
	Agent string
	Now   func() time.Time
}

// ActivityEventRecorder builds the production per-turn event handler.
// It satisfies EventRecorder.
func ActivityEventRecorder(agent registry.Agent, st *store.Store) opencode.EventHandler {
	return ActivityRecorder{Agent: agent.Name}.Handler(st)
}

// Handler returns an event handler that appends tool activity to st. The
// handler is best-effort: it ignores every other event type and always returns
// nil, so logging failures can never fail or stall a turn.
func (r ActivityRecorder) Handler(st *store.Store) opencode.EventHandler {
	return func(event opencode.Event) error {
		if st == nil {
			return nil
		}
		level, message, ok := activityLogEntry(event.Type)
		if !ok {
			return nil
		}
		_ = st.Orch().AppendLog(level, "opencode:"+r.Agent, message, activityMeta(event), r.now())
		return nil
	}
}

func (r ActivityRecorder) now() time.Time {
	if r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now()
}

// activityLogEntry maps an event type to its audit level and message. Unknown
// event types (text deltas, reasoning, idle, and so on) are not recorded.
func activityLogEntry(eventType string) (level, message string, ok bool) {
	switch strings.TrimSpace(eventType) {
	case "session.tool.called":
		return "info", "tool called", true
	case "session.tool.success":
		return "info", "tool succeeded", true
	case "session.tool.failed":
		return "warn", "tool failed", true
	default:
		return "", "", false
	}
}

// activityMeta builds the IDs-only meta payload. Identifiers are extracted
// defensively: malformed or unexpected payloads simply yield empty fields.
func activityMeta(event opencode.Event) string {
	fields := map[string]string{
		"event_id":             activityString(event.ID),
		"event_type":           activityString(event.Type),
		"session_id":           "",
		"assistant_message_id": "",
		"tool_call_id":         "",
	}
	if len(event.Data) > 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			fields["session_id"] = activityRaw(payload["sessionID"], payload["session_id"])
			fields["assistant_message_id"] = activityRaw(payload["assistantMessageID"], payload["assistant_message_id"])
			fields["tool_call_id"] = activityRaw(payload["id"], payload["toolCallID"])
		}
	}

	raw, err := json.Marshal(fields)
	if err != nil || len(raw) > activityMetaMax {
		return "{}"
	}
	return string(raw)
}

// activityRaw decodes the first usable string value from event data.
func activityRaw(primary, fallback json.RawMessage) string {
	for _, candidate := range []json.RawMessage{primary, fallback} {
		if len(candidate) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(candidate, &value); err != nil {
			continue
		}
		if value = activityString(value); value != "" {
			return value
		}
	}
	return ""
}

// activityString trims and bounds an identifier so meta cannot grow unbounded.
func activityString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= activityFieldMax {
		return value
	}
	runes := []rune(value)
	if len(runes) > activityFieldMax {
		runes = runes[:activityFieldMax]
	}
	return string(runes)
}
