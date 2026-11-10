package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maskJSONC blanks out comments and trailing commas with spaces so the result
// is plain JSON whose byte offsets are identical to the original text. String
// literals are preserved verbatim, which is what targeted edits need: regexes
// run against the mask and are applied back to the original bytes.
func maskJSONC(raw string) string {
	out := []byte(raw)

	// Pass 1: comments -> spaces (newlines inside block comments are kept so
	// line-oriented tooling stays stable).
	i := 0
	for i < len(out) {
		switch {
		case out[i] == '"':
			i = skipJSONCString(out, i)
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			start := i
			i += 2
			for i < len(out) && !(out[i] == '*' && i+1 < len(out) && out[i+1] == '/') {
				i++
			}
			if i < len(out) {
				i += 2 // consume the closing "*/"
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
	return string(out)
}

// parseJSONC decodes JSONC (JSON with // and /* */ comments and trailing
// commas) into v.
func parseJSONC(raw string, v any) error {
	if err := json.Unmarshal([]byte(maskJSONC(raw)), v); err != nil {
		return fmt.Errorf("invalid JSONC: %w", err)
	}
	return nil
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
