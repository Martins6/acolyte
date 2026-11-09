package opencode

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DigestOptions caps the session digest used by dream/heartbeat prompts.
type DigestOptions struct {
	MaxSessions     int
	MaxMessages     int
	MaxMessageChars int
	MaxTotalChars   int
	MaxPages        int
}

func (o DigestOptions) withDefaults() DigestOptions {
	if o.MaxSessions <= 0 {
		o.MaxSessions = 20
	}
	if o.MaxMessages <= 0 {
		o.MaxMessages = 50
	}
	if o.MaxMessageChars <= 0 {
		o.MaxMessageChars = 2000
	}
	if o.MaxTotalChars <= 0 {
		o.MaxTotalChars = 64 * 1024
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 5
	}
	return o
}

// DigestSessions returns a compact, capped text digest of the supplied
// sessions for a routine prompt.
func (c *Client) DigestSessions(ctx context.Context, sessions []SessionInfo, opts DigestOptions) (string, error) {
	opts = opts.withDefaults()
	var builder strings.Builder
	for index, session := range sessions {
		if index >= opts.MaxSessions {
			_ = writeCapped(&builder, "\n[more sessions omitted]\n", opts.MaxTotalChars)
			break
		}
		if builder.Len() >= opts.MaxTotalChars {
			_ = writeCapped(&builder, "\n[digest truncated]\n", opts.MaxTotalChars)
			break
		}
		header := fmt.Sprintf("SESSION %s", session.ID)
		if session.Title != "" {
			header += " | " + session.Title
		}
		if session.Time.Updated > 0 {
			header += " | updated " + time.UnixMilli(int64(session.Time.Updated)).UTC().Format(time.RFC3339)
		}
		if !writeCapped(&builder, "\n"+header+"\n", opts.MaxTotalChars) {
			break
		}

		messages, _, err := c.SessionMessages(ctx, session.ID, opts.MaxPages)
		if err != nil {
			return "", fmt.Errorf("opencode: digest session %s: %w", session.ID, err)
		}
		start := 0
		if len(messages) > opts.MaxMessages {
			start = len(messages) - opts.MaxMessages
		}
		for _, message := range messages[start:] {
			text := strings.TrimSpace(digestMessageText(message))
			if text == "" {
				continue
			}
			text = truncateText(text, opts.MaxMessageChars)
			line := message.Type + ": " + text + "\n"
			if !writeCapped(&builder, line, opts.MaxTotalChars) {
				return strings.TrimSpace(builder.String()), nil
			}
		}
	}
	return strings.TrimSpace(builder.String()), nil
}

func writeCapped(builder *strings.Builder, text string, limit int) bool {
	if builder.Len()+len(text) <= limit {
		builder.WriteString(text)
		return true
	}
	remaining := limit - builder.Len()
	if remaining > 0 {
		builder.WriteString(text[:remaining])
	}
	return false
}

func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func digestMessageText(message Message) string {
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
