package comm

import (
	"strings"
	"unicode"
)

const (
	// CommandNew starts a fresh OpenCode session for the conversation thread.
	CommandNew = "new"
	// NewSessionReply is the user-facing confirmation for /new.
	NewSessionReply = "New session started."
)

// parseCommand returns a normalized bot command when text is a slash command.
// It accepts "/new", "/new@botname" and "/new with args".
func parseCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	field := text[1:]
	if index := strings.IndexFunc(field, unicode.IsSpace); index >= 0 {
		field = field[:index]
	}
	if index := strings.Index(field, "@"); index >= 0 {
		field = field[:index]
	}
	name := strings.ToLower(field)
	if name == "" {
		return "", false
	}
	return name, true
}
