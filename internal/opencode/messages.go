package opencode

import (
	"context"
	"fmt"
	"net/url"
)

// DefaultMessagePageLimit bounds full-history reads used by acolyte inspect.
const DefaultMessagePageLimit = 100

// SessionMessages returns the session timeline in chronological order. The
// second return value reports whether the page cap was reached before the
// beginning of the conversation.
func (c *Client) SessionMessages(ctx context.Context, sessionID string, maxPages int) ([]Message, bool, error) {
	if maxPages <= 0 {
		maxPages = DefaultMessagePageLimit
	}
	var newestFirst []Message
	truncated := false
	cursor := ""
	for page := 0; page < maxPages; page++ {
		path := fmt.Sprintf("/api/session/%s/message?order=desc&limit=%d",
			url.PathEscape(sessionID), messagePageSize)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			Data   []Message `json:"data"`
			Cursor struct {
				Previous string `json:"previous"`
			} `json:"cursor"`
		}
		if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
			return nil, false, err
		}
		newestFirst = append(newestFirst, resp.Data...)
		if resp.Cursor.Previous == "" {
			break
		}
		cursor = resp.Cursor.Previous
		if page == maxPages-1 {
			truncated = true
		}
	}

	messages := make([]Message, len(newestFirst))
	for index := range newestFirst {
		messages[len(newestFirst)-1-index] = newestFirst[index]
	}
	return messages, truncated, nil
}
