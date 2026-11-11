package traceimport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/store"
)

// sessionPlan is one manifest session prepared for import.
type sessionPlan struct {
	sourceID   string
	title      string
	thread     string
	nativeJSON string
	skip       bool
	mappedTo   string
}

// planSessions turns the manifest into per-session import decisions. Sessions
// are processed in manifest order; a session whose thread is already mapped
// is skipped unless force is set. Duplicate source ids are rejected up front:
// both entries would import and the second would overwrite the first's thread
// mapping, orphaning the session it imported.
func planSessions(m *manifest, traceID string, st *store.Store, force bool) ([]sessionPlan, []string, error) {
	var plans []sessionPlan
	var warnings []string
	seen := map[string]string{} // source id -> payload of its first manifest entry
	noPayload := 0
	for _, entry := range m.Sessions {
		sourceID := strings.TrimSpace(entry.ID)
		if sourceID == "" {
			warnings = append(warnings, "manifest session entry has no id; skipped")
			continue
		}
		payload := strings.TrimSpace(entry.NativeJSON)
		if first, dup := seen[sourceID]; dup {
			return nil, nil, fmt.Errorf("traceimport: duplicate session id %q in manifest (payloads %q and %q)", sourceID, first, payload)
		}
		seen[sourceID] = payload
		if payload == "" {
			noPayload++
			continue
		}
		plan := sessionPlan{
			sourceID:   sourceID,
			title:      entry.Title,
			thread:     "memory:" + traceID + ":" + sourceID,
			nativeJSON: entry.NativeJSON,
		}
		if st != nil {
			existing, ok, err := st.Orch().GetSessionByThread(plan.thread)
			if err != nil {
				return nil, nil, err
			}
			if ok && !force {
				plan.skip = true
				plan.mappedTo = existing.OpenCodeSessionID
			}
		}
		plans = append(plans, plan)
	}
	if noPayload > 0 {
		warnings = append(warnings, fmt.Sprintf("%d sessions have no native JSON payload and were skipped", noPayload))
	}
	if len(m.Sessions) > 0 && len(plans) == 0 {
		warnings = append(warnings, "manifest lists no importable sessions")
	}
	return plans, warnings, nil
}

// validatePayloads decodes every payload that would be imported and checks
// its structure before any import request is issued — dry-run included. It
// runs alongside the size-budget preflight and skips the same plans:
// info.id must match the manifest source id (so the thread mapping points at
// the session that was actually imported), and message ids must be unique
// within and across payloads so one import cannot collide with or orphan
// another.
func (b *traceBundle) validatePayloads(plans []sessionPlan) error {
	seenMessageIDs := map[string]string{} // message id -> session that claimed it
	for _, plan := range plans {
		if plan.skip {
			continue
		}
		raw, err := b.readEntry(plan.nativeJSON)
		if err != nil {
			return err
		}
		payload, err := decodeTransfer(raw)
		if err != nil {
			return fmt.Errorf("traceimport: decode session %s payload: %w", plan.sourceID, err)
		}
		infoID, _ := payload.info["id"].(string)
		if strings.TrimSpace(infoID) == "" {
			return fmt.Errorf("traceimport: session %s payload has no info.id", plan.sourceID)
		}
		if infoID != plan.sourceID {
			return fmt.Errorf("traceimport: session %s payload has info.id %q, which does not match the manifest session id", plan.sourceID, infoID)
		}
		seenInPayload := map[string]bool{}
		for _, entry := range payload.messages {
			message, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			messageID, _ := message["id"].(string)
			if messageID == "" {
				continue
			}
			if seenInPayload[messageID] {
				return fmt.Errorf("traceimport: session %s payload has duplicate message id %q", plan.sourceID, messageID)
			}
			seenInPayload[messageID] = true
			if other, ok := seenMessageIDs[messageID]; ok {
				return fmt.Errorf("traceimport: duplicate message id %q across payloads (sessions %s and %s)", messageID, other, plan.sourceID)
			}
			seenMessageIDs[messageID] = plan.sourceID
		}
	}
	return nil
}

// sessionImporter imports session payloads, rewriting identifiers when the
// target service already holds conflicting data.
type sessionImporter struct {
	client    OpenCode
	workspace string
	imported  map[string]string // source session id -> imported session id (this run)
}

// importOne imports one session payload. The service preserves the supplied
// session and message ids, so a trace exported from the same service (the
// common agent-to-agent move) collides: an existing session yields 409 and
// existing message ids yield 500. On those conflicts all identifiers in the
// payload are regenerated and the import retried. A 404 means the payload's
// parent session does not exist in the target; the parent/fork references are
// dropped and the import retried. Each fix is applied at most once.
func (im *sessionImporter) importOne(ctx context.Context, plan sessionPlan, raw []byte) (*SessionResult, error) {
	payload, err := decodeTransfer(raw)
	if err != nil {
		return nil, fmt.Errorf("traceimport: decode session %s payload: %w", plan.sourceID, err)
	}
	origID, ok := payload.info["id"].(string)
	if !ok || strings.TrimSpace(origID) == "" {
		return nil, fmt.Errorf("traceimport: session %s payload has no info.id", plan.sourceID)
	}
	payload.remapRefs(im.imported)

	idsRewritten := false
	parentDropped := false
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := payload.marshal()
		if err != nil {
			return nil, err
		}
		info, err := im.client.ImportSession(ctx, body, im.workspace)
		if err == nil {
			im.imported[origID] = info.ID
			if plan.sourceID != origID {
				im.imported[plan.sourceID] = info.ID
			}
			return &SessionResult{
				SourceSessionID: plan.sourceID,
				SessionID:       info.ID,
				Thread:          plan.thread,
				Title:           plan.title,
			}, nil
		}
		lastErr = err
		switch status := apiStatus(err); {
		case (status == http.StatusConflict || status >= 500) && !idsRewritten:
			payload.rewriteIDs()
			idsRewritten = true
		case status == http.StatusNotFound && !parentDropped:
			payload.dropSessionRefs()
			parentDropped = true
		default:
			return nil, fmt.Errorf("traceimport: import session %s: %w", plan.sourceID, err)
		}
	}
	return nil, fmt.Errorf("traceimport: import session %s: %w", plan.sourceID, lastErr)
}

// apiStatus extracts the HTTP status from an APIError, or 0.
func apiStatus(err error) int {
	var apiErr *opencode.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// transfer is the decoded {info, messages} import payload. Numbers are kept
// as json.Number so re-marshaling never alters their literal form.
type transfer struct {
	root     map[string]any
	info     map[string]any
	messages []any
}

func decodeTransfer(raw []byte) (*transfer, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	info, ok := root["info"].(map[string]any)
	if !ok {
		return nil, errors.New("payload has no info object")
	}
	t := &transfer{root: root, info: info}
	if messages, ok := root["messages"].([]any); ok {
		t.messages = messages
	}
	return t, nil
}

func (t *transfer) marshal() (json.RawMessage, error) {
	raw, err := json.Marshal(t.root)
	if err != nil {
		return nil, fmt.Errorf("traceimport: re-encode session payload: %w", err)
	}
	return raw, nil
}

// remapRefs points parent/fork references at sessions imported earlier in
// this run. References that stay unresolved are left for the service to
// accept or reject (a 404 drops them).
func (t *transfer) remapRefs(imported map[string]string) {
	if len(imported) == 0 {
		return
	}
	if parent, ok := t.info["parentID"].(string); ok {
		if mapped, ok := imported[parent]; ok {
			t.info["parentID"] = mapped
		}
	}
	if fork, ok := t.info["fork"].(map[string]any); ok {
		if source, ok := fork["sessionID"].(string); ok {
			if mapped, ok := imported[source]; ok {
				fork["sessionID"] = mapped
			}
		}
	}
}

// rewriteIDs regenerates the session id and every message id in the payload,
// then rewrites the identifier fields that reference them: info.id,
// info.parentID, info.fork.sessionID, message ids, and message revert targets.
// Content fields (text, summary, metadata, tool input/output, ...) are never
// touched — a value that happens to equal an old id must stay byte-identical.
func (t *transfer) rewriteIDs() {
	idMap := map[string]string{}
	if old, ok := t.info["id"].(string); ok && old != "" {
		idMap[old] = randomID("ses_")
	}
	for _, entry := range t.messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		old, ok := message["id"].(string)
		if !ok || old == "" {
			continue
		}
		if _, taken := idMap[old]; taken {
			continue
		}
		idMap[old] = randomID("msg_")
	}
	replaceIdentifier(t.info, "id", idMap)
	replaceIdentifier(t.info, "parentID", idMap)
	if fork, ok := t.info["fork"].(map[string]any); ok {
		replaceIdentifier(fork, "sessionID", idMap)
	}
	for _, entry := range t.messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		replaceIdentifier(message, "id", idMap)
		if revert, ok := message["revert"].(map[string]any); ok {
			replaceIdentifier(revert, "messageID", idMap)
		}
	}
}

// dropSessionRefs removes references to sessions that do not exist in the
// target service (parent and fork lineage).
func (t *transfer) dropSessionRefs() {
	delete(t.info, "parentID")
	delete(t.info, "fork")
}

// replaceIdentifier swaps object[key] when its value is a key of idMap. Only
// identifier fields are routed through here, so content that happens to equal
// an id (message text, metadata, ...) stays byte-identical.
func replaceIdentifier(object map[string]any, key string, idMap map[string]string) {
	current, ok := object[key].(string)
	if !ok {
		return
	}
	if replaced, ok := idMap[current]; ok {
		object[key] = replaced
	}
}

// randomID builds a service-valid identifier: sessions need a "ses" prefix,
// messages need "msg_".
func randomID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// time-based value rather than panicking in an import path.
		return prefix + fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
}
