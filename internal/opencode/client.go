package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Thin client for the OpenCode V2 HTTP API (SPEC §5.1). Request/response
// shapes are pinned against the OpenCode v2.2.7 /openapi.json contract.

// ModelRef selects a model (OpenCode Model.Ref).
type ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

// SessionInfo is the public session projection (OpenCode Session.Info).
type SessionInfo struct {
	ID          string           `json:"id"`
	Agent       string           `json:"agent"`
	Model       *ModelRef        `json:"model"`
	Title       string           `json:"title,omitempty"`
	Time        SessionTime      `json:"time,omitempty"`
	Location    SessionLocation  `json:"location,omitempty"`
	Permissions []PermissionRule `json:"permissions,omitempty"`

	// Raw is the exact session projection returned by the service. It is kept
	// for trace exports that must preserve fields this client does not model.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON retains the original projection alongside the typed fields.
func (s *SessionInfo) UnmarshalJSON(data []byte) error {
	type alias SessionInfo
	var value alias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = SessionInfo(value)
	s.Raw = append(s.Raw[:0], data...)
	return nil
}

// SessionTime holds session epoch-millisecond timestamps.
type SessionTime struct {
	Created float64 `json:"created"`
	Updated float64 `json:"updated"`
	// Busy and Idle are epoch-millisecond stamps the service sets when a turn
	// starts and when the session returns to idle.
	Busy float64 `json:"busy,omitempty"`
	Idle float64 `json:"idle,omitempty"`
}

// InFlight reports whether the session has an unfinished turn. The service
// stamps Busy when a turn starts and Idle when it settles, so a Busy stamp newer
// than the last Idle means work is still running (or wedged).
func (t SessionTime) InFlight() bool {
	return t.Busy > t.Idle
}

// SessionLocation is the project directory a session belongs to.
type SessionLocation struct {
	Directory string `json:"directory,omitempty"`
}

// Message is one projected session message (OpenCode Session.Message.Info
// union). Type selects the variant: Text is set for user/synthetic messages,
// Content for assistant messages, Outcome for idle markers.
type Message struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Time    MessageTime   `json:"time"`
	Text    string        `json:"text"`
	Content []ContentPart `json:"content"`
	Outcome string        `json:"outcome"`
	Finish  string        `json:"finish"`

	// Raw is the exact message returned by the service. Trace exports use it
	// to render fields beyond the reply-oriented projection above (tool state,
	// reasoning, metadata, attachments, cost, tokens, ...).
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON retains the original message alongside the typed fields.
// Content is decoded tolerantly: a malformed content value or part is skipped
// instead of failing message collection, because the raw message remains
// available for consumers that need the original shape.
func (m *Message) UnmarshalJSON(data []byte) error {
	var value struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Time    MessageTime     `json:"time"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		Outcome string          `json:"outcome"`
		Finish  string          `json:"finish"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = Message{
		ID:      value.ID,
		Type:    value.Type,
		Time:    value.Time,
		Text:    value.Text,
		Content: contentParts(value.Content),
		Outcome: value.Outcome,
		Finish:  value.Finish,
	}
	m.Raw = append(m.Raw[:0], data...)
	return nil
}

// contentParts decodes an assistant content array without letting one bad
// part fail the message. Unknown shapes yield no parts.
func contentParts(raw json.RawMessage) []ContentPart {
	if len(raw) == 0 {
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return parts
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil
	}
	parts = parts[:0]
	for _, rawPart := range rawParts {
		var part ContentPart
		if err := json.Unmarshal(rawPart, &part); err != nil {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

// MessageTime holds epoch-millisecond timestamps of a message.
type MessageTime struct {
	Created   float64 `json:"created"`
	Completed float64 `json:"completed"`
}

// ContentPart is one assistant content block (text, reasoning, or tool).
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// UnmarshalJSON tolerates non-string fields and non-object parts. A field
// with an unexpected type is dropped rather than failing the whole message;
// a non-object part returns an error so callers can skip just that part.
func (p *ContentPart) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*p = ContentPart{
		Type: jsonString(fields["type"]),
		Text: jsonString(fields["text"]),
	}
	return nil
}

// jsonString decodes a JSON string, returning the zero value for any other
// shape so a single malformed field cannot fail its parent object.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// InboxItem is an admitted input (OpenCode Session.Inbox.User / .Synthetic).
// The ID is retained by the delivered message and can be used as a marker.
type InboxItem struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
}

// APIError is a non-2xx response from the OpenCode API. The message contains
// "not found" for 404s and "invalid" for 400s so callers can keep their
// existing error matching.
type APIError struct {
	Status  int
	Message string
	// Code is the machine-readable error code when the service sends one, for
	// example "service_stopping" while it drains for a restart.
	Code string
	// RetryAfter is the delay the service asked for (Retry-After), or zero
	// when the header was absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusNotFound:
		return fmt.Sprintf("opencode: not found (404): %s", e.Message)
	case http.StatusBadRequest:
		return fmt.Sprintf("opencode: invalid request (400): %s", e.Message)
	default:
		return fmt.Sprintf("opencode: status %d: %s", e.Status, e.Message)
	}
}

// Permanent reports whether the service rejected the request in a way that
// retrying cannot fix. Every 4xx is permanent except 408 (request timeout) and
// 429 (rate limited), which may succeed later; 5xx responses are not permanent.
func (e *APIError) Permanent() bool {
	if e == nil || e.Status < 400 || e.Status >= 500 {
		return false
	}
	switch e.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

// IsPermanentError reports whether err is a definitive API rejection. Errors
// that are not *APIError (transport failures, timeouts, cancellations) are not
// permanent and should be retried.
func IsPermanentError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Permanent()
}

// Restart-window retry policy. The managed service answers with 503
// service_stopping or service_starting while it drains and boots, and asks the
// caller to retry with Retry-After. Those responses reject the request before
// it is handled, so replaying any call is safe. Transport failures are
// replayed only for idempotent calls: replaying a prompt could admit it twice.
const (
	defaultServiceRetryTimeout  = 30 * time.Second
	defaultServiceRetryInterval = time.Second
	serviceRecoverGap           = 5 * time.Second
)

// Client talks to one OpenCode V2 server instance.
type Client struct {
	mu       sync.RWMutex
	baseURL  string
	password string
	client   *http.Client

	// recover re-discovers the managed service after its endpoint goes away
	// (restart, crash). Ensure sets it; a bare NewClient stays pinned to its
	// URL. recoverMu serializes recoveries and recoveredAt rate-limits them so
	// concurrent failed requests cannot stampede `opencode service start`.
	recover     func(context.Context) (*Client, error)
	recoverMu   sync.Mutex
	recoveredAt time.Time

	// Retry policy, configurable per client so tests never race on globals.
	retryTimeout  time.Duration
	retryInterval time.Duration
}

// NewClient returns a client for the OpenCode service at baseURL, authenticating
// with HTTP Basic "opencode" / password (from ~/.config/opencode/service.json).
func NewClient(baseURL, password string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		password:      password,
		client:        &http.Client{},
		retryTimeout:  defaultServiceRetryTimeout,
		retryInterval: defaultServiceRetryInterval,
	}
}

func (c *Client) endpoint() (baseURL, password string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL, c.password
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("opencode: marshal request: %w", err)
		}
	}

	deadline := time.Now().Add(c.retryTimeout)
	for {
		baseURL, password := c.endpoint()
		err := c.doOnce(ctx, baseURL, password, method, path, payload, out)
		if err == nil {
			return nil
		}
		wait, retry := c.retryDelay(method, path, err)
		if !retry || !time.Now().Add(wait).Before(deadline) {
			// The call cannot be replayed (or its retry hint is outside the
			// window), but the endpoint may still have moved. Recover it so
			// later calls work.
			if c.needsRecover(err) {
				c.recoverEndpoint(ctx, time.Now().Add(c.retryTimeout))
			}
			return err
		}
		select {
		case <-ctx.Done():
			// The caller is gone, but a stopping service still needs to be
			// rediscovered for later calls. Recover on a context that the
			// cancellation cannot abort, bounded by a fresh retry window.
			if c.needsRecover(err) {
				c.recoverEndpoint(context.WithoutCancel(ctx), time.Now().Add(c.retryTimeout))
			}
			return err
		case <-time.After(wait):
		}
		if c.needsRecover(err) {
			c.recoverEndpoint(ctx, deadline)
		}
	}
}

// doOnce performs one request against one endpoint snapshot.
func (c *Client) doOnce(ctx context.Context, baseURL, password, method, path string, payload []byte, out any) error {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("opencode: create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if password != "" {
		req.SetBasicAuth("opencode", password)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return newAPIError(resp, raw)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("opencode: decode response: %w", err)
		}
	}
	return nil
}

// retryDelay reports whether a failed request should be replayed and how long
// to wait first. A service_stopping/service_starting response rejects the
// request before it is handled, so every method is safe to replay. A transport
// failure is only replayed for idempotent calls, and only when the client can
// re-discover the service; a bare client would just hit the same dead URL.
func (c *Client) retryDelay(method, path string, err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status != http.StatusServiceUnavailable ||
			(apiErr.Code != "service_stopping" && apiErr.Code != "service_starting") {
			return 0, false
		}
		if apiErr.RetryAfter > 0 {
			return apiErr.RetryAfter, true
		}
		return c.retryInterval, true
	}
	if c.recover == nil || !idempotentRequest(method, path) || !transportFailure(err) {
		return 0, false
	}
	return c.retryInterval, true
}

// idempotentRequest reports whether replaying the request cannot create
// duplicate work. The wait and agent/model switches only observe or replace
// state; prompt and synthetic admission are deliberately excluded.
func idempotentRequest(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	case http.MethodPost:
		return strings.HasSuffix(path, "/wait") ||
			strings.HasSuffix(path, "/agent") ||
			strings.HasSuffix(path, "/model")
	default:
		return false
	}
}

// transportFailure reports whether err failed below the HTTP status layer. A
// canceled or expired caller context is not a service restart.
func transportFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// needsRecover reports whether the service endpoint itself may have moved.
// service_starting still points at the live service; service_stopping is on
// its way out, so the next attempt should rediscover it.
func (c *Client) needsRecover(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusServiceUnavailable && apiErr.Code == "service_stopping"
	}
	return transportFailure(err)
}

// recoverEndpoint re-discovers the managed service after a restart. It is
// best-effort: the caller still retries the current endpoint if recovery
// fails, and the cooldown keeps concurrent failures from all starting a
// service. TryLock lets a request keep its own retry deadline instead of
// blocking behind another recovery.
func (c *Client) recoverEndpoint(ctx context.Context, deadline time.Time) {
	if c.recover == nil || !c.recoverMu.TryLock() {
		return
	}
	defer c.recoverMu.Unlock()
	if time.Since(c.recoveredAt) < serviceRecoverGap {
		return
	}
	c.recoveredAt = time.Now()

	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	recoverCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()

	fresh, err := c.recover(recoverCtx)
	if err != nil || fresh == nil {
		return
	}
	baseURL, password := fresh.endpoint()
	c.mu.Lock()
	c.baseURL, c.password = baseURL, password
	c.mu.Unlock()
}

// newAPIError projects a non-2xx response into the typed client error,
// retaining the service code and retry hint when present.
func newAPIError(resp *http.Response, raw []byte) *APIError {
	return &APIError{
		Status:     resp.StatusCode,
		Message:    strings.TrimSpace(string(raw)),
		Code:       apiErrorCode(raw),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// apiErrorCode extracts the machine-readable code from an error body.
func apiErrorCode(raw []byte) string {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	return body.Code
}

// parseRetryAfter decodes a Retry-After header in seconds or HTTP-date form.
func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > maxRetryAfterSeconds {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	} else if isPositiveInteger(raw) {
		// A decimal hint that overflows int64 is still a valid minimum;
		// saturate it instead of falling back to the short retry interval.
		return maxRetryAfter
	}
	if at, err := http.ParseTime(raw); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

// isPositiveInteger reports whether raw is a non-empty run of decimal digits.
// It distinguishes an oversized Retry-After from a non-numeric value.
func isPositiveInteger(raw string) bool {
	raw = strings.TrimPrefix(raw, "+")
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// maxRetryAfterSeconds is the largest whole-second Retry-After value that fits
// in a time.Duration. Larger hints saturate so a server-requested minimum
// never collapses into an early retry.
const (
	maxRetryAfterSeconds = int64(1<<63-1) / int64(time.Second)
	maxRetryAfter        = time.Duration(maxRetryAfterSeconds) * time.Second
)

// HealthCheck verifies the service is reachable and authenticated.
func (c *Client) HealthCheck(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/info", nil, nil)
}

// CreateSession creates a session rooted at workspace with the given agent/model.
func (c *Client) CreateSession(ctx context.Context, workspace, agent string, model *ModelRef) (*SessionInfo, error) {
	body := map[string]any{"location": map[string]string{"directory": workspace}}
	if agent != "" {
		body["agent"] = agent
	}
	if model != nil {
		body["model"] = model
	}
	var resp struct {
		Data SessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/session", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ListSessions returns every OpenCode session visible to the service.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	var resp struct {
		Data []SessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// GetSession retrieves the session projection (agent, model, ...).
func (c *Client) GetSession(ctx context.Context, sessionID string) (*SessionInfo, error) {
	var resp struct {
		Data SessionInfo `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// SetSessionPermissions replaces the session-scoped permission rules. Rules
// are evaluated after the agent's rules (last match wins), so callers can
// confine a turn without editing the workspace config.
func (c *Client) SetSessionPermissions(ctx context.Context, sessionID string, rules []PermissionRule) error {
	if rules == nil {
		rules = []PermissionRule{}
	}
	path := "/api/session/" + url.PathEscape(sessionID)
	return c.do(ctx, http.MethodPatch, path, map[string]any{"permissions": rules}, nil)
}

// SwitchAgent selects the agent used by subsequent turns.
func (c *Client) SwitchAgent(ctx context.Context, sessionID, agent string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/agent"
	return c.do(ctx, http.MethodPost, path, map[string]string{"agent": agent}, nil)
}

// SwitchModel selects the model used by subsequent turns.
func (c *Client) SwitchModel(ctx context.Context, sessionID string, model ModelRef) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/model"
	return c.do(ctx, http.MethodPost, path, map[string]any{"model": model}, nil)
}

// Prompt admits a real user turn and returns the admitted message (marker).
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (*InboxItem, error) {
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	var resp struct {
		Data InboxItem `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"text": text}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// Synthetic admits an external event (SPEC §5.1/§14) and returns the admitted
// message (marker). delivery is "steer" (interrupt) or "queue" (next idle).
func (c *Client) Synthetic(ctx context.Context, sessionID, text, description string, metadata map[string]any, delivery string) (*InboxItem, error) {
	body := map[string]any{"text": text}
	if description != "" {
		body["description"] = description
	}
	if metadata != nil {
		body["metadata"] = metadata
	}
	if delivery != "" {
		body["delivery"] = delivery
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/synthetic"
	var resp struct {
		Data InboxItem `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// Wait blocks until the session's agent loop is idle.
func (c *Client) Wait(ctx context.Context, sessionID string) error {
	path := "/api/experimental/session/" + url.PathEscape(sessionID) + "/wait"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Interrupt stops the session's in-flight turn and settles it. Recovery uses it
// when a wedged turn owns the session and its marker will never produce a reply.
func (c *Client) Interrupt(ctx context.Context, sessionID string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/interrupt"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Context returns the session's active context messages (all messages after
// the last compaction) — exactly what the model sees on the next turn.
func (c *Client) Context(ctx context.Context, sessionID string) ([]Message, error) {
	path := "/api/session/" + url.PathEscape(sessionID) + "/context"
	var resp struct {
		Data []Message `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ExportSession returns the OpenCode-native transfer payload for a session
// (info + complete projected messages). It is the format accepted by
// ImportSession.
func (c *Client) ExportSession(ctx context.Context, sessionID string) (json.RawMessage, error) {
	path := "/api/experimental/session/" + url.PathEscape(sessionID) + "/export"
	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("opencode: empty session export for %s", sessionID)
	}
	return resp.Data, nil
}

// ImportSession imports an OpenCode-native transfer payload (the {info,
// messages} shape produced by ExportSession) into workspace and returns the
// session projection the service created. The workspace is sent as the
// top-level location, which is what the service applies to the imported
// session. An empty messages array is normalized to [] because the endpoint
// requires the key but accepts no messages.
func (c *Client) ImportSession(ctx context.Context, raw json.RawMessage, workspace string) (*SessionInfo, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, fmt.Errorf("opencode: import workspace is required")
	}
	var payload struct {
		Info     json.RawMessage `json:"info"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("opencode: decode session payload: %w", err)
	}
	if len(payload.Info) == 0 || string(payload.Info) == "null" {
		return nil, fmt.Errorf("opencode: session payload has no info")
	}
	if len(payload.Messages) == 0 || string(payload.Messages) == "null" {
		payload.Messages = json.RawMessage("[]")
	}
	body := struct {
		Info     json.RawMessage   `json:"info"`
		Messages json.RawMessage   `json:"messages"`
		Location map[string]string `json:"location"`
	}{
		Info:     payload.Info,
		Messages: payload.Messages,
		Location: map[string]string{"directory": workspace},
	}
	var resp struct {
		Data SessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/experimental/session/import", body, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == "" {
		return nil, fmt.Errorf("opencode: import returned no session id")
	}
	return &resp.Data, nil
}

// Message pagination bounds, vars so tests can shrink them.
var (
	messagePageSize = 100
	messageMaxPages = 20
)

// messagesAfter returns the chronological messages that follow markerID in the
// session timeline. The collection stops at the first later input
// (user/synthetic message): a later input starts a later turn, so its output
// must not leak into this turn's reply. (Tradeoff: a turn whose output
// straddles a later input keeps only the tail before that input — correct for
// the serial queue/prompt paths in production; revisit if interleaved prompts
// become common.) ok is false when markerID has not been delivered yet.
// truncated is true when the marker was not found and the walk hit the page
// cap with more of the timeline left, so delivery state is unknown.
func (c *Client) messagesAfter(ctx context.Context, sessionID, markerID string) ([]Message, bool, bool, error) {
	var after []Message // newest-first while walking back
	found := false
	truncated := false
	cursor := ""
	for page := 0; page < messageMaxPages; page++ {
		path := fmt.Sprintf("/api/session/%s/message?limit=%d", url.PathEscape(sessionID), messagePageSize)
		if cursor == "" {
			path += "&order=desc"
		} else {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			Data   []Message `json:"data"`
			Cursor struct {
				Previous string `json:"previous"`
				Next     string `json:"next"`
			} `json:"cursor"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, false, false, err
		}
		for _, m := range resp.Data {
			if m.ID == markerID {
				found = true
				break
			}
			if m.Type == "user" || m.Type == "synthetic" {
				// A later input starts a later turn: everything collected
				// so far is newer than it and belongs to that turn.
				after = after[:0]
				continue
			}
			after = append(after, m)
		}
		if found || resp.Cursor.Next == "" {
			break
		}
		if page == messageMaxPages-1 {
			truncated = true
		}
		cursor = resp.Cursor.Next
	}

	// Reverse to chronological order.
	for i, j := 0, len(after)-1; i < j; i, j = i+1, j-1 {
		after[i], after[j] = after[j], after[i]
	}
	return after, found, truncated, nil
}

// NextInputID returns the oldest user/synthetic input newer than markerID, and
// whether markerID itself was found in the session timeline. Recovery uses it
// to tell whether an aged marker's turn is still the one owning a busy session.
func (c *Client) NextInputID(ctx context.Context, sessionID, markerID string) (string, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	markerID = strings.TrimSpace(markerID)
	if sessionID == "" || markerID == "" {
		return "", false, fmt.Errorf("opencode: session id and marker id are required")
	}
	next := ""
	found := false
	cursor := ""
	for page := 0; page < messageMaxPages; page++ {
		path := fmt.Sprintf("/api/session/%s/message?limit=%d", url.PathEscape(sessionID), messagePageSize)
		if cursor == "" {
			path += "&order=desc"
		} else {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			Data   []Message `json:"data"`
			Cursor struct {
				Previous string `json:"previous"`
				Next     string `json:"next"`
			} `json:"cursor"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return "", false, err
		}
		for _, m := range resp.Data {
			if m.ID == markerID {
				found = true
				break
			}
			if m.Type == "user" || m.Type == "synthetic" {
				// Walking newest-first: the last input seen before the marker
				// is the oldest input newer than it.
				next = m.ID
			}
		}
		if found || resp.Cursor.Next == "" {
			break
		}
		cursor = resp.Cursor.Next
	}
	if !found {
		return "", false, nil
	}
	return next, true, nil
}

// InboxPending reports whether the given inbox item is still awaiting
// delivery to the session.
func (c *Client) InboxPending(ctx context.Context, sessionID, itemID string) (bool, error) {
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox"
	var resp struct {
		Data []InboxItem `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return false, err
	}
	for _, item := range resp.Data {
		if item.ID == itemID {
			return true, nil
		}
	}
	return false, nil
}
