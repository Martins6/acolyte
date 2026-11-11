package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// topLevelKeyPattern matches a JSON string key followed by a colon. Comments
// are already masked out and strings are preserved verbatim, so every match
// at depth 1 is a real top-level key.
var topLevelKeyPattern = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*:`)

// maskJSONC blanks out comments and trailing commas with spaces so the result
// is plain JSON whose byte offsets are identical to the original text. String
// literals are preserved verbatim, which is what targeted edits need: regexes
// run against the mask and are applied back to the original bytes.
//
// Unterminated strings or block comments are rejected instead of being masked
// through EOF: otherwise a valid config followed by a dangling "/*" would
// mask into valid JSON and malformed JSONC would be silently accepted.
func maskJSONC(raw string) (string, error) {
	out := []byte(raw)

	// Pass 1: comments -> spaces (newlines inside block comments are kept so
	// line-oriented tooling stays stable).
	i := 0
	for i < len(out) {
		switch {
		case out[i] == '"':
			end, ok := stringEnd(raw, i)
			if !ok {
				return "", fmt.Errorf("unterminated string literal at byte %d", i)
			}
			i = end
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			start := i
			i += 2
			closed := false
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", fmt.Errorf("unterminated block comment starting at byte %d", start)
			}
			for j := start; j < i; j++ {
				if out[j] != '\n' && out[j] != '\r' {
					out[j] = ' '
				}
			}
		default:
			i++
		}
	}

	// Pass 2: trailing commas before "}" or "]" -> spaces (JSONC allows them).
	// Pass 1 already rejected unterminated strings, so skipping them is safe.
	for i = 0; i < len(out); {
		if out[i] == '"' {
			i = skipJSONCString(out, i)
			continue
		}
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && isJSONCSpace(out[j]) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				out[i] = ' '
			}
		}
		i++
	}
	return string(out), nil
}

// parseJSONC decodes JSONC (JSON with // and /* */ comments and trailing
// commas) into v. The top-level value must be a JSON object.
func parseJSONC(raw string, v any) error {
	masked, err := maskJSONC(raw)
	if err != nil {
		return fmt.Errorf("invalid JSONC: %w", err)
	}
	if err := decodeJSONObject(masked, v); err != nil {
		return fmt.Errorf("invalid JSONC: %w", err)
	}
	return nil
}

// decodeJSONObject decodes already-masked JSON into v and requires the
// top-level value to be an object: json.Unmarshal into a struct accepts
// `null` silently (leaving every field empty), which would let `edit`
// replace the config with a value OpenCode cannot use.
func decodeJSONObject(masked string, v any) error {
	var value any
	if err := json.Unmarshal([]byte(masked), &value); err != nil {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("top-level value must be an object")
	}
	return json.Unmarshal([]byte(masked), v)
}

// skipJSONCString returns the index just past the string literal that starts
// at i (which must point at the opening quote).
func skipJSONCString(b []byte, i int) int {
	i++ // opening quote
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1
		default:
			i++
		}
	}
	return i
}

// stringEnd returns the index just past the string literal starting at i, and
// whether a closing quote was found.
func stringEnd(s string, i int) (int, bool) {
	if i >= len(s) || s[i] != '"' {
		return i, false
	}
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return len(s), false
}

func isJSONCSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// jsoncDepth reports the nesting depth of the JSON structure at pos. It
// returns -1 when pos falls inside a string literal, so callers can reject a
// match that only looks like a key inside prompt text.
func jsoncDepth(masked string, pos int) int {
	depth := 0
	for i := 0; i < len(masked); {
		if i == pos {
			return depth
		}
		switch masked[i] {
		case '"':
			end, ok := stringEnd(masked, i)
			if !ok {
				end = len(masked)
			}
			if pos < end {
				return -1
			}
			i = end
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
		default:
			i++
		}
	}
	return depth
}

// duplicateTopLevelKey returns the first key that appears more than once at
// depth 1 of a masked document, or "" when every top-level key is unique.
// Keys are compared by their decoded value, so a unicode-escaped spelling of
// "model" and the plain one count as the same key. JSON decoding silently
// keeps the last duplicate, so an ambiguous document must be rejected before
// it is read or edited.
func duplicateTopLevelKey(masked string) string {
	seen := map[string]int{}
	for _, loc := range topLevelKeyPattern.FindAllStringSubmatchIndex(masked, -1) {
		if jsoncDepth(masked, loc[0]) != 1 {
			continue
		}
		key, err := unquoteJSONKey(masked[loc[2]:loc[3]])
		if err != nil {
			continue // undecodable: the document decode rejects it later
		}
		seen[key]++
		if seen[key] > 1 {
			return key
		}
	}
	return ""
}

// unquoteJSONKey decodes a quoted JSON string token (its quotes excluded)
// so escaped spellings compare equal to the plain one.
func unquoteJSONKey(content string) (string, error) {
	var key string
	if err := json.Unmarshal([]byte(`"`+content+`"`), &key); err != nil {
		return "", err
	}
	return key, nil
}

// trimJSONCIndent detects the indentation used by the first indented line of
// a JSONC document, falling back to two spaces (the acolyte scaffold style).
func trimJSONCIndent(masked string) string {
	for _, line := range strings.Split(masked, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == line || trimmed == "" {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		if strings.ContainsAny(indent, " \t") {
			return indent
		}
	}
	return "  "
}

// lastStructural returns the index of the last byte that is neither JSON
// whitespace nor inside a comment (comments are already masked to spaces).
func lastStructural(masked string) int {
	for i := len(masked) - 1; i >= 0; i-- {
		if !isJSONCSpace(masked[i]) {
			return i
		}
	}
	return -1
}
