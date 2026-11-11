package traceexport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

// secretTrace builds a workspace + fake service whose conversation contains
// a variety of secrets, and returns a zip path built with the given options.
func exportWithSecrets(t *testing.T, sanitize bool) map[string]string {
	t.Helper()
	ws := t.TempDir()
	writeFile(t, ws, "AGENTS.md", "# Agent hub\n")
	writeFile(t, ws, "opencode.jsonc", "{\n  // note\n  \"model\": \"x\"\n}\n")

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	readOnly, err := store.OpenReadOnly(ws)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer readOnly.Close()

	fake := &fakeOpenCode{
		sessions: []opencode.SessionInfo{{
			ID:       "ses_x",
			Agent:    "build",
			Time:     opencode.SessionTime{Created: float64(time.Now().UnixMilli())},
			Location: opencode.SessionLocation{Directory: ws},
		}},
		messages: map[string][]opencode.Message{
			"ses_x": {
				mustMessage(t, `{"id":"msg_user","type":"user","time":{"created":1},"text":"set OPENAI_API_KEY=sk-abc123def456 and PASSWORD=hunter2"}`),
				mustMessage(t, `{"id":"msg_bot","type":"user","time":{"created":2},"text":"auth header: authorization: Bearer eyJhbGciOi.fake-token; also bearer AnotherSecret99"}`),
				mustMessage(t, `{"id":"msg_env","type":"user","time":{"created":3},"text":"api_key=leakedvalue telegram 123456789:AAEabcdefghijklmnopqrstuvwxyz123456"}`),
				mustMessage(t, `{"id":"msg_clean","type":"assistant","time":{"created":4,"completed":5},"agent":"build","model":{"id":"m","providerID":"p"},"content":[{"type":"text","text":"a clean reply"}]}`),
			},
		},
		truncated: map[string]bool{"ses_x": false},
		exports: map[string]json.RawMessage{
			"ses_x": json.RawMessage(`{"info":{"id":"ses_x"},"messages":[]}`),
		},
	}

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	if _, err := Export(context.Background(), Request{
		Agent:          registry.Agent{Name: "valet", Workspace: ws},
		Store:          readOnly,
		OpenCode:       fake,
		OutPath:        outPath,
		MaxPages:       10,
		Sanitize:       sanitize,
		AcolyteVersion: "test",
		Now:            time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	return readZip(t, outPath)
}

func TestExportSanitizeRedactsSecrets(t *testing.T) {
	t.Parallel()

	files := exportWithSecrets(t, true)

	secrets := []string{
		"sk-abc123def456",
		"hunter2",
		"eyJhbGciOi.fake-token",
		"AnotherSecret99",
		"leakedvalue",
		"123456789:AAEabcdefghijklmnopqrstuvwxyz123456",
	}
	for name, content := range files {
		for _, secret := range secrets {
			if strings.Contains(content, secret) {
				t.Errorf("entry %s still contains secret %q", name, secret)
			}
		}
	}

	// The redacted markers must be present in the session markdown.
	session := files["sessions/ses_x.md"]
	if !strings.Contains(session, "[REDACTED]") {
		t.Errorf("session markdown has no [REDACTED] marker:\n%s", session)
	}
	// Non-secret content survives.
	if !strings.Contains(session, "a clean reply") {
		t.Errorf("clean content lost:\n%s", session)
	}

	// manifest.json records the sanitization and stays valid JSON.
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["manifest.json"]), &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest["sanitized"] != true {
		t.Errorf("manifest sanitized = %v, want true", manifest["sanitized"])
	}

	// The native session payload must stay importable: valid JSON with the
	// required info keys intact.
	var native struct {
		Info     map[string]any `json:"info"`
		Messages []any          `json:"messages"`
	}
	if err := json.Unmarshal([]byte(files["sessions/ses_x.json"]), &native); err != nil {
		t.Fatalf("native payload is not valid JSON: %v", err)
	}
	if native.Info["id"] != "ses_x" {
		t.Errorf("native info.id = %v, want ses_x", native.Info["id"])
	}
}

func TestExportWithoutSanitizePreservesSecrets(t *testing.T) {
	t.Parallel()

	files := exportWithSecrets(t, false)

	session := files["sessions/ses_x.md"]
	for _, secret := range []string{"sk-abc123def456", "hunter2", "leakedvalue"} {
		if !strings.Contains(session, secret) {
			t.Errorf("session markdown lost %q without --sanitize:\n%s", secret, session)
		}
	}
	if strings.Contains(session, "[REDACTED]") {
		t.Errorf("unexpected redaction without --sanitize:\n%s", session)
	}

	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["manifest.json"]), &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest["sanitized"] != false {
		t.Errorf("manifest sanitized = %v, want false", manifest["sanitized"])
	}
}

func TestExportSanitizeREADMEWarns(t *testing.T) {
	t.Parallel()

	files := exportWithSecrets(t, true)
	readme := files["README.md"]
	if !strings.Contains(readme, "may contain secrets") {
		t.Errorf("README missing secrets warning:\n%s", readme)
	}
	if !strings.Contains(readme, "best-effort") {
		t.Errorf("README missing best-effort note:\n%s", readme)
	}
}

func TestSanitizeEntryKeepsJSONValid(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"api key assignment":  `{"api_key": "sk-abcdefghijklmnop", "other": 1}`,
		"nested secrets":      `{"settings": {"password": "hunter2", "port": 8080}, "list": ["sk-qrstuvwxyz123456"]}`,
		"token in text":       `{"text": "the TOKEN=supersecret value"}`,
		"already clean":       `{"text": "nothing to hide", "n": 42}`,
		"numbers stay exact":  `{"time": {"created": 1794346118687}, "cost": 0.000123}`,
		"authorization field": `{"headers": {"authorization": "Bearer abc.def.ghi"}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := sanitizeEntry("x.json", []byte(input))
			var round any
			if err := json.Unmarshal(out, &round); err != nil {
				t.Fatalf("output is not valid JSON: %v\ninput: %s\noutput: %s", err, input, out)
			}
			if strings.Contains(string(out), "sk-abcdefghijklmnop") ||
				strings.Contains(string(out), "hunter2") ||
				strings.Contains(string(out), "supersecret") {
				t.Errorf("secret not redacted: %s", out)
			}
		})
	}
}

func TestSanitizeJSONPreservesNumbersExactly(t *testing.T) {
	t.Parallel()

	in := []byte(`{"time":{"created":1794346118687,"updated":1794346118688},"big":9007199254740993,"text":"sk-abcdef1234567890"}`)
	out, ok := sanitizeJSON(in)
	if !ok {
		t.Fatal("sanitizeJSON reported no change for a payload with a secret")
	}
	if !strings.Contains(string(out), "1794346118687") {
		t.Errorf("timestamp altered: %s", out)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Errorf("large integer altered: %s", out)
	}
	if !strings.Contains(string(out), "[REDACTED]") {
		t.Errorf("secret not redacted: %s", out)
	}
}

// TestSanitizeJSONTrailingContent guards the dec.More() gap: trailing text
// after the first JSON value must be redacted instead of being returned
// verbatim alongside a clean prefix. dec.More() reports false when the next
// byte is ']' or '}', which let `{"a":1}] TOKEN=x` pass as plain JSON.
func TestSanitizeJSONTrailingContent(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in       string
		contains []string
		excludes []string
	}{
		"trailing env assignment": {
			in:       `{"a":1} API_KEY=secret`,
			contains: []string{`{"a":1}`, `API_KEY=[REDACTED]`},
			excludes: []string{"secret"},
		},
		"trailing after close bracket": {
			in:       `{"a":1}] TOKEN=x`,
			contains: []string{`{"a":1}`, `TOKEN=[REDACTED]`},
			excludes: []string{"TOKEN=x"},
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := string(sanitizeEntry("x.json", []byte(tt.in)))
			for _, want := range tt.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\ninput:  %s\noutput: %s", want, tt.in, out)
				}
			}
			for _, banned := range tt.excludes {
				if strings.Contains(out, banned) {
					t.Errorf("output still contains %q:\ninput:  %s\noutput: %s", banned, tt.in, out)
				}
			}
		})
	}

	t.Run("plain json with trailing whitespace untouched", func(t *testing.T) {
		t.Parallel()

		in := "{\n  \"a\": 1\n}\n"
		if out := string(sanitizeEntry("x.json", []byte(in))); out != in {
			t.Errorf("output changed:\nin:  %q\nout: %q", in, out)
		}
	})

	t.Run("json with comments still uses the jsonc scanner", func(t *testing.T) {
		t.Parallel()

		// The first value is not decodable JSON, so sanitizeEntry must fall
		// back to the JSONC scanner: the comment survives and the bare
		// number under a secret key stays intact (the text fallback would
		// replace 12345 with an unquoted [REDACTED]).
		in := "{\n  // note\n  \"password\": 12345\n}"
		out := string(sanitizeEntry("x.json", []byte(in)))
		if !strings.Contains(out, "// note") {
			t.Errorf("comment lost (jsonc scanner not used):\n%s", out)
		}
		if !strings.Contains(out, `"password": 12345`) {
			t.Errorf("bare number altered (jsonc scanner not used):\n%s", out)
		}
		if strings.Contains(out, "[REDACTED]") {
			t.Errorf("unexpected redaction:\n%s", out)
		}
	})
}

func TestSanitizeTextPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       string
		contains string
		excludes string
	}{
		{"env assignment", "OPENAI_API_KEY=sk-abc123def", "OPENAI_API_KEY=[REDACTED]", "sk-abc123def"},
		{"env with colon", "TELEGRAM_BOT_TOKEN: 123456789:AAEabcdefghijklmnopqrstuvwxyz123456", "TELEGRAM_BOT_TOKEN: [REDACTED]", "AAEabc"},
		{"authorization header", "authorization: Bearer secret.token.value", "authorization: [REDACTED]", "secret.token.value"},
		{"bearer alone", "use bearer tokengoeshere now", "bearer [REDACTED]", "tokengoeshere"},
		{"bare api key", "api_key=abcdef123456", "[REDACTED]", "abcdef123456"},
		{"standalone sk key", "prefix sk-abcdef123456 suffix", "[REDACTED]", "sk-abcdef123456"},
		{"telegram token", "token is 1234567890:AAEabcdefghijklmnopqrstuvwxyz123456 ok", "[REDACTED]", "AAEabcdefghijklmnopqrstuvwxyz123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := sanitizeText([]byte(tt.in))
			if !strings.Contains(string(out), tt.contains) {
				t.Errorf("output %q missing %q", out, tt.contains)
			}
			if strings.Contains(string(out), tt.excludes) {
				t.Errorf("output %q still contains %q", out, tt.excludes)
			}
		})
	}
}

func TestSanitizeIsIdempotent(t *testing.T) {
	t.Parallel()

	in := []byte("OPENAI_API_KEY=sk-abc123def authorization: Bearer x.y.z")
	once := sanitizeText(in)
	twice := sanitizeText(once)
	if string(once) != string(twice) {
		t.Errorf("sanitize not idempotent:\nonce: %q\ntwice: %q", once, twice)
	}
}

// TestSanitizeJSONCKeepsStructure guards the structural-safety bug: raw text
// patterns must never replace a bare JSON value (number/bool) with an
// unquoted [REDACTED], which would break the document.
func TestSanitizeJSONCKeepsStructure(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in       string
		contains []string
		excludes []string
	}{
		"bare numeric under secret key": {
			in:       `{"password": 12345, "other": 1}`,
			contains: []string{`"password": 12345`},
		},
		"bare bool under key-ish name": {
			in:       `{"my-key": true, "x": 2}`,
			contains: []string{`"my-key": true`},
		},
		"quoted secret still redacted": {
			in:       `{"api_key": "sk-abcdefghijklmnop", "port": 8080}`,
			contains: []string{`"api_key": "[REDACTED]"`, `"port": 8080`},
			excludes: []string{"sk-abcdefghijklmnop"},
		},
		"comments preserved": {
			in:       "{\n  // keep me\n  \"password\": \"hunter2\",\n  /* block */ \"n\": 1\n}",
			contains: []string{"// keep me", "/* block */", `"password": "[REDACTED]"`, `"n": 1`},
			excludes: []string{"hunter2"},
		},
		"env assignment inside a string value": {
			in:       `{"command": "export API_KEY=sk-abcdef123456"}`,
			contains: []string{`"command": "export API_KEY=[REDACTED]"`},
			excludes: []string{"sk-abcdef123456"},
		},
		"authorization field": {
			in:       `{"headers": {"authorization": "Bearer abc.def.ghi"}}`,
			contains: []string{`"authorization": "[REDACTED]"`},
			excludes: []string{"abc.def.ghi"},
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := string(sanitizeEntry("opencode.jsonc", []byte(tt.in)))
			for _, want := range tt.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\ninput:  %s\noutput: %s", want, tt.in, out)
				}
			}
			for _, banned := range tt.excludes {
				if strings.Contains(out, banned) {
					t.Errorf("output still contains %q:\ninput:  %s\noutput: %s", banned, tt.in, out)
				}
			}
			// The redacted document must still be structurally intact: same
			// number of braces and brackets as the input.
			if strings.Count(out, "{") != strings.Count(tt.in, "{") ||
				strings.Count(out, "}") != strings.Count(tt.in, "}") {
				t.Errorf("brace balance changed:\ninput:  %s\noutput: %s", tt.in, out)
			}
		})
	}
}

// TestSanitizeJSONCIsIdempotent verifies a redacted JSONC file does not
// change further on a second pass.
func TestSanitizeJSONCIsIdempotent(t *testing.T) {
	t.Parallel()

	in := []byte("{\n  // c\n  \"password\": \"hunter2\",\n  \"authorization\": \"Bearer x.y\"\n}")
	once := sanitizeEntry("opencode.jsonc", in)
	twice := sanitizeEntry("opencode.jsonc", once)
	if string(once) != string(twice) {
		t.Errorf("jsonc sanitize not idempotent:\nonce: %s\ntwice: %s", once, twice)
	}
}

// TestSanitizeJSONCRedactsBlockComments guards the block-comment gap: a
// secret inside /* ... */ must be redacted while the comment delimiters
// survive and the document stays parseable JSONC.
func TestSanitizeJSONCRedactsBlockComments(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in       string
		contains []string
		excludes []string
	}{
		"env assignment in block comment": {
			in:       `{"n":1, "m":2 /* API_KEY=sk-abcdef123456 */}`,
			contains: []string{"/* API_KEY=[REDACTED] */"},
			excludes: []string{"sk-abcdef123456"},
		},
		"authorization in multi-line block comment": {
			in:       "{\n  /*\n   * authorization: Bearer secret.token.value\n   */\n  \"a\": 1\n}",
			contains: []string{"/*", "*/", "authorization: [REDACTED]"},
			excludes: []string{"secret.token.value"},
		},
		"unrelated comment untouched": {
			in:       `{"n":1, "m":2 /* just a note */}`,
			contains: []string{"/* just a note */"},
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := string(sanitizeEntry("opencode.jsonc", []byte(tt.in)))
			for _, want := range tt.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\ninput:  %s\noutput: %s", want, tt.in, out)
				}
			}
			for _, banned := range tt.excludes {
				if strings.Contains(out, banned) {
					t.Errorf("output still contains %q:\ninput:  %s\noutput: %s", banned, tt.in, out)
				}
			}
			// The comment delimiters must be intact: exactly one /* and one */
			// in these inputs, and the JSONC must still parse once comments
			// are stripped.
			if strings.Count(out, "/*") != strings.Count(tt.in, "/*") ||
				strings.Count(out, "*/") != strings.Count(tt.in, "*/") {
				t.Errorf("comment delimiters changed:\ninput:  %s\noutput: %s", tt.in, out)
			}
			stripped := stripJSONCComments(out)
			if err := json.Unmarshal([]byte(stripped), new(any)); err != nil {
				t.Errorf("output is not valid JSONC (after stripping comments): %v\n%s", err, stripped)
			}
		})
	}
}

// stripJSONCComments removes // and /* */ comments so a test can hand the
// remainder to json.Unmarshal. Test inputs never contain comment-like
// sequences inside strings.
func stripJSONCComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, "")
	line := regexp.MustCompile(`(?m)//.*$`)
	return line.ReplaceAllString(s, "")
}

// TestSanitizeSkipsBinaryEntries ensures binary persona/skill assets are
// returned untouched.
func TestSanitizeSkipsBinaryEntries(t *testing.T) {
	t.Parallel()

	binary := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02, 's', 'k', '-', 'a', 'b', 'c'}
	out := sanitizeEntry("persona/logo.png", binary)
	if string(out) != string(binary) {
		t.Errorf("binary entry was modified: %v", out)
	}
}
