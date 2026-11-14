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
	// botUsernameMeta carries the channel bot's own username on inbound
	// envelopes so addressed commands can be scoped to this bot.
	botUsernameMeta = "telegram_bot_username"
)

// parseCommand returns the normalized command and its explicit @mention (if
// any) when text is a slash command. It accepts "/new", "/new@botname" and
// "/new with args". An empty @mention is not a valid command.
func parseCommand(text string) (name, mention string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	field := text[1:]
	if index := strings.IndexFunc(field, unicode.IsSpace); index >= 0 {
		field = field[:index]
	}
	if index := strings.Index(field, "@"); index >= 0 {
		mention = strings.ToLower(strings.TrimSpace(field[index+1:]))
		if mention == "" {
			return "", "", false
		}
		field = field[:index]
	}
	name = strings.ToLower(field)
	if name == "" {
		return "", "", false
	}
	return name, mention, true
}

// commandAddressedToBot reports whether a slash command targets this bot. A
// command without an @mention is unaddressed and applies here; an explicit
// mention must match the channel bot's own username.
func commandAddressedToBot(mention string, meta map[string]any) bool {
	if mention == "" {
		return true
	}
	username := ""
	if value, ok := meta[botUsernameMeta].(string); ok {
		username = strings.ToLower(strings.TrimSpace(value))
	}
	return username != "" && mention == username
}
