package traceexport

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// redacted replaces any matched secret. It contains no quotes or backslashes,
// so substituting it inside a JSON string keeps the document parseable.
const redacted = "[REDACTED]"

// Secret redaction patterns for text entries. Every pattern is written so it
// never consumes a quote, brace, bracket, pipe, or newline that could belong
// to the surrounding structure, and every replacement is idempotent.
var (
	// Env-style assignment with a quoted value: KEY="value" / KEY: 'value'.
	// The key must contain TOKEN/KEY/SECRET/PASSWORD.
	envQuotedRe = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|key|secret|password)[A-Za-z0-9_.-]*["']?[ \t]*[=:][ \t]*["'])([^"']+)(["'])`)
	// Env-style assignment with a bare value: KEY=value. Structural chars and
	// table pipes are excluded from the value so embedded JSON displays in
	// Markdown survive redaction.
	envBareRe = regexp.MustCompile("(?i)([A-Za-z0-9_.-]*(?:token|key|secret|password)[A-Za-z0-9_.-]*[\"']?[ \t]*[=:][ \t]*)([^\\s\"'`" + "`" + `{},;\[\]|]+)`)
	// Authorization header or field: authorization: <value>. The value is a
	// space-separated token sequence, so it must start with a non-space
	// character — that keeps `authorization: [REDACTED]` from re-matching
	// (the leading space would otherwise be consumed as the whole value).
	authHeaderRe = regexp.MustCompile(`(?i)(authorization["']?[ \t]*[=:][ \t]*["']?)([^\s\r\n"'` + "`" + `|,;{}[\]]+(?:[ \t]+[^\s\r\n"'` + "`" + `|,;{}[\]]+)*)`)
	// Bearer token: bearer <token>.
	bearerRe = regexp.MustCompile(`(?i)(\bbearer[ \t]+)([A-Za-z0-9._\-+/=]+)`)
	// OpenAI-style API keys: sk-....
	skKeyRe = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
	// Telegram bot tokens: <bot id>:<35 char secret>.
	telegramTokenRe = regexp.MustCompile(`\b[0-9]{8,12}:[A-Za-z0-9_-]{30,}`)
)

// secretKeyPattern matches JSON object keys that hold secrets by name.
var secretKeyPattern = regexp.MustCompile(`(?i)(token|key|secret|password|authorization)`)

// sanitizeEntry redacts secret patterns from one bundle entry.
//
// JSON files are parsed and walked so redaction can never break their
// structure (numbers and layout outside redacted strings are untouched).
// JSONC files go through a comment-tolerant scanner that rewrites only
// string tokens, preserving comments and formatting. Everything else
// (Markdown, plain text) is redacted with the text patterns; binary entries
// are returned unchanged because no pattern can be applied safely.
func sanitizeEntry(name string, data []byte) []byte {
	if len(data) == 0 || bytes.IndexByte(data, 0) >= 0 {
		return data // binary or empty: nothing safe to redact
	}
	if strings.HasSuffix(name, ".json") {
		if clean, ok := sanitizeJSON(data); ok {
			return clean
		}
		// The first value is not decodable JSON (comments, trailing
		// commas, ...): fall through to the JSONC scanner, which only
		// rewrites string tokens.
	}
	if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonc") {
		if clean, ok := sanitizeJSONC(data); ok {
			return clean
		}
		// Malformed beyond the scanner too: best-effort text redaction.
	}
	return sanitizeText(data)
}

// sanitizeJSON walks a JSON document redacting string values. It returns the
// original bytes when nothing changed, so untouched files keep their exact
// formatting, and ok=false when the data is not JSON at all. Bytes after the
// first value (trailing text, a second value, ...) are sanitized as text and
// appended, so a clean JSON prefix can never smuggle a secret past redaction.
func sanitizeJSON(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // preserve numeric literals exactly (timestamps, costs)
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, false
	}
	// dec.More() is not a reliable end-of-document check: it reports false
	// when the next byte is ']' or '}', so a clean prefix followed by
	// "] TOKEN=x" would be returned unchanged. Decode a second value
	// instead: only io.EOF proves the document ends after the first value.
	end := dec.InputOffset()
	var extra any
	plain := dec.Decode(&extra) == io.EOF

	changed := false
	if s, ok := root.(string); ok {
		if clean, did := redactString("", s); did {
			root = clean
			changed = true
		}
	} else {
		changed = walkSanitize(root, "")
	}

	if !changed {
		if plain {
			return data, true // untouched: keep the original bytes
		}
		cleanTrailing := sanitizeText(data[end:])
		if bytes.Equal(cleanTrailing, data[end:]) {
			return data, true
		}
		return append(append([]byte{}, data[:end]...), cleanTrailing...), true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, false
	}
	if plain {
		return buf.Bytes(), true
	}
	// Not a plain JSON document: re-encode the redacted first value and
	// append the trailing bytes with the text patterns applied.
	return append(buf.Bytes(), sanitizeText(data[end:])...), true
}

// walkSanitize redacts string values under node, using key as the owning
// object key. It reports whether anything changed.
func walkSanitize(node any, key string) bool {
	changed := false
	switch value := node.(type) {
	case map[string]any:
		for k, child := range value {
			if s, ok := child.(string); ok {
				if clean, ok := redactString(k, s); ok {
					value[k] = clean
					changed = true
				}
				continue
			}
			if walkSanitize(child, k) {
				changed = true
			}
		}
	case []any:
		for i, child := range value {
			if s, ok := child.(string); ok {
				if clean, ok := redactString(key, s); ok {
					value[i] = clean
					changed = true
				}
				continue
			}
			if walkSanitize(child, key) {
				changed = true
			}
		}
	}
	return changed
}

// redactString redacts one string value: entirely when its owning key names a
// secret (token/key/secret/password/authorization), otherwise by applying the
// text patterns to the value itself.
func redactString(key, value string) (string, bool) {
	if value == "" {
		return value, false
	}
	if secretKeyPattern.MatchString(key) {
		return redacted, true
	}
	clean := sanitizeTextString(value)
	return clean, clean != value
}

// sanitizeJSONC redacts string values in a JSONC document (JSON with // and
// /* */ comments). Only string tokens are rewritten — comments, whitespace,
// numbers, and punctuation are copied byte-for-byte — so the output stays as
// valid as the input. ok=false means the input is malformed (e.g. an
// unterminated string) and the caller should fall back to text redaction.
func sanitizeJSONC(data []byte) ([]byte, bool) {
	out := make([]byte, 0, len(data))
	lastKey := ""
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			// Line comment: redact inside it. Every pattern is line-bounded
			// or token-bounded, so a comment can never be corrupted.
			end := i
			for end < len(data) && data[end] != '\n' {
				end++
			}
			comment := string(data[i:end])
			out = append(out, sanitizeTextString(comment)...)
			i = end
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			// Block comment: redact secrets in the body while keeping the
			// /* and */ delimiters. The body cannot contain the terminator
			// (we stop at the first "*/"), and the patterns only ever insert
			// "[REDACTED]" or substrings of the body, so no replacement can
			// form a "*/" and end the comment early.
			rel := bytes.Index(data[i+2:], []byte("*/"))
			if rel < 0 {
				return nil, false // unterminated block comment
			}
			body := data[i+2 : i+2+rel]
			out = append(out, '/', '*')
			out = append(out, sanitizeTextString(string(body))...)
			out = append(out, '*', '/')
			i += 2 + rel + 2
		case c == '"':
			// A string token: either an object key (followed by ':') or a
			// value. Keys are kept verbatim; values are decoded, redacted,
			// and re-encoded only when they changed.
			end := i + 1
			for end < len(data) {
				if data[end] == '\\' {
					end += 2
					continue
				}
				if data[end] == '"' {
					break
				}
				end++
			}
			if end >= len(data) {
				return nil, false // unterminated string
			}
			token := data[i : end+1]
			if next := skipJSONCNoise(data, end+1); next < len(data) && data[next] == ':' {
				var key string
				if err := json.Unmarshal(token, &key); err != nil {
					return nil, false
				}
				lastKey = key
				out = append(out, token...)
				i = end + 1
				continue
			}
			var value string
			if err := json.Unmarshal(token, &value); err != nil {
				return nil, false
			}
			clean, changed := redactString(lastKey, value)
			if !changed {
				out = append(out, token...)
				i = end + 1
				continue
			}
			encoded, err := encodeJSONString(clean)
			if err != nil {
				return nil, false
			}
			out = append(out, encoded...)
			i = end + 1
		default:
			out = append(out, c)
			i++
		}
	}
	return out, true
}

// skipJSONCNoise advances past whitespace and comments and returns the index
// of the next significant byte (used to detect "string" followed by ':').
func skipJSONCNoise(data []byte, i int) int {
	for i < len(data) {
		switch {
		case data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n':
			i++
		case data[i] == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case data[i] == '/' && i+1 < len(data) && data[i+1] == '*':
			rel := bytes.Index(data[i+2:], []byte("*/"))
			if rel < 0 {
				return len(data)
			}
			i += 2 + rel + 2
		default:
			return i
		}
	}
	return i
}

// encodeJSONString re-encodes s as a JSON string literal without HTML
// escaping.
func encodeJSONString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	// Encode appends a newline; the token must not carry one.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// sanitizeText applies the secret patterns to a whole text blob.
func sanitizeText(data []byte) []byte {
	return []byte(sanitizeTextString(string(data)))
}

func sanitizeTextString(s string) string {
	replaced := envQuotedRe.ReplaceAllString(s, "${1}"+redacted+"${3}")
	replaced = envBareRe.ReplaceAllString(replaced, "${1}"+redacted)
	replaced = authHeaderRe.ReplaceAllString(replaced, "${1}"+redacted)
	replaced = bearerRe.ReplaceAllString(replaced, "${1}"+redacted)
	replaced = skKeyRe.ReplaceAllString(replaced, redacted)
	replaced = telegramTokenRe.ReplaceAllString(replaced, redacted)
	return replaced
}
