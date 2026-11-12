package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is one OpenCode V2 event delivered by GET /api/event (SSE).
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Created float64         `json:"created,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// SessionID extracts data.sessionID when the event carries one. Events such as
// server.connected have no session scope and return "".
func (e Event) SessionID() string {
	if len(e.Data) == 0 {
		return ""
	}
	var payload struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

// EventHandler receives one event. Returning an error stops the subscription.
type EventHandler func(Event) error

type eventHandlerError struct{ err error }

func (e *eventHandlerError) Error() string { return e.err.Error() }
func (e *eventHandlerError) Unwrap() error { return e.err }

var (
	// eventReconnectDelay is the initial wait after the event stream drops.
	eventReconnectDelay    = time.Second
	eventMaxReconnectDelay = 30 * time.Second
)

// SubscribeEvents consumes the OpenCode event stream until ctx is canceled.
// It reconnects with exponential backoff after a dropped stream; client errors
// (4xx) and handler errors stop the subscription instead of retrying forever.
// An optional types filter is sent as repeated `types` query parameters.
func (c *Client) SubscribeEvents(ctx context.Context, types []string, handle EventHandler) error {
	if handle == nil {
		return fmt.Errorf("opencode: event handler is required")
	}
	delay := eventReconnectDelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.subscribeEventsOnce(ctx, types, handle)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var handlerErr *eventHandlerError
		if errors.As(err, &handlerErr) {
			return handlerErr.err
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < eventMaxReconnectDelay {
			delay *= 2
			if delay > eventMaxReconnectDelay {
				delay = eventMaxReconnectDelay
			}
		}
	}
}

func (c *Client) subscribeEventsOnce(ctx context.Context, types []string, handle EventHandler) error {
	endpoint := c.baseURL + "/api/event"
	if len(types) > 0 {
		values := url.Values{}
		for _, eventType := range types {
			eventType = strings.TrimSpace(eventType)
			if eventType != "" {
				values.Add("types", eventType)
			}
		}
		if encoded := values.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("opencode: create event request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.password != "" {
		req.SetBasicAuth("opencode", c.password)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: subscribe events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	frame := Event{}
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			frame = Event{}
			return nil
		}
		raw := strings.Join(dataLines, "\n")
		event, err := decodeEvent(raw, frame.ID, frame.Type)
		frame = Event{}
		dataLines = nil
		if err != nil {
			// A malformed event is not a transport failure: skip it rather
			// than dropping the whole subscription.
			return nil
		}
		if err := handle(event); err != nil {
			return &eventHandlerError{err: err}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.ID = value
		case "event":
			frame.Type = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("opencode: read event stream: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	return io.EOF
}

// decodeEvent accepts both the direct JSON object form and the encoded JSON
// string form used by V2EventEncoded.
func decodeEvent(raw, frameID, frameType string) (Event, error) {
	var event Event
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		var encoded string
		if stringErr := json.Unmarshal([]byte(raw), &encoded); stringErr != nil {
			return Event{}, err
		}
		if err := json.Unmarshal([]byte(encoded), &event); err != nil {
			return Event{}, err
		}
	}
	if event.ID == "" {
		event.ID = frameID
	}
	if event.Type == "" {
		event.Type = frameType
	}
	return event, nil
}
