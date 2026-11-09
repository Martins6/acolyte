package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Thin client for the OpenCode V2 HTTP API (SPEC §5.1). Request/response
// shapes are pinned against the OpenCode v2.2.5 /openapi.json contract.

// ModelRef selects a model (OpenCode Model.Ref).
type ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

// SessionInfo is the public session projection (OpenCode Session.Info).
type SessionInfo struct {
	ID    string    `json:"id"`
	Agent string    `json:"agent"`
	Model *ModelRef `json:"model"`
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

// Client talks to one OpenCode V2 server instance.
type Client struct {
	baseURL  string
	password string
	client   *http.Client
}

// NewClient returns a client for the OpenCode service at baseURL, authenticating
// with HTTP Basic "opencode" / password (from ~/.config/opencode/service.json).
func NewClient(baseURL, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		client:   &http.Client{},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("opencode: marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("opencode: create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.password != "" {
		req.SetBasicAuth("opencode", c.password)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("opencode: decode response: %w", err)
		}
	}
	return nil
}

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
