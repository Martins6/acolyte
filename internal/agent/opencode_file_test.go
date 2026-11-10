package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonUEscape builds a JSON unicode escape (backslash + u + code) without
// embedding one literally, so tests can spell keys the escaped way.
func jsonUEscape(code string) string {
	return string(rune(92)) + "u" + code
}

// TestDuplicateTopLevelKeySemantic pins that duplicate detection compares
// decoded key values: a unicode-escaped spelling is the same key.
func TestDuplicateTopLevelKeySemantic(t *testing.T) {
	escapedModel := `{"model": "acme/a", "mo` + jsonUEscape("0064") + `el": "acme/b"}`
	escapedAgent := `{"default_agent": "build", "default_` + jsonUEscape("0061") + `gent": "plan"}`
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "escaped model equals plain", raw: escapedModel, want: "model"},
		{name: "escaped default_agent equals plain", raw: escapedAgent, want: "default_agent"},
		{name: "distinct keys are not duplicates", raw: `{"model": "a", "models": "b"}`, want: ""},
		{name: "single escaped key is unique", raw: `{"mo` + jsonUEscape("0064") + `el": "a"}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			masked, err := maskJSONC(tc.raw)
			if err != nil {
				t.Fatalf("maskJSONC() error: %v", err)
			}
			if got := duplicateTopLevelKey(masked); got != tc.want {
				t.Errorf("duplicateTopLevelKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetOpencodeStringRejectsEscapedDuplicateKey(t *testing.T) {
	raw := `{"model": "acme/a", "mo` + jsonUEscape("0064") + `el": "acme/b"}`
	_, err := setOpencodeString(raw, "model", "acme/c")
	if err == nil || !strings.Contains(err.Error(), `duplicate top-level key "model"`) {
		t.Fatalf("setOpencodeString() error = %v, want escaped duplicate-key error", err)
	}
}

// TestSetEngineModelRejectsEscapedDuplicateKey covers readEngine (via
// LoadConfig) and setOpencodeString (via SetConfig) on a document whose
// duplicate model key is only visible after decoding.
func TestSetEngineModelRejectsEscapedDuplicateKey(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	ambiguous := `{
  "model": "acme/a",
  "instructions": [".acolyte/*.md"],
  "mo` + jsonUEscape("0064") + `el": "acme/b"
}`
	path := filepath.Join(ws, "opencode.jsonc")
	if err := os.WriteFile(path, []byte(ambiguous), 0644); err != nil {
		t.Fatalf("write ambiguous config: %v", err)
	}
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := LoadConfig(st, target); err == nil || !strings.Contains(err.Error(), `duplicate top-level key "model"`) {
		t.Fatalf("LoadConfig() error = %v, want escaped duplicate-key error", err)
	}
	if _, err := SetConfig(st, target, SetOptions{Key: "engine.model", Value: "acme/c"}); err == nil ||
		!strings.Contains(err.Error(), "duplicate top-level key") {
		t.Fatalf("SetConfig() error = %v, want duplicate-key error", err)
	}
	if got := readOpencode(t, ws); got != ambiguous {
		t.Errorf("ambiguous file was modified:\n%s", got)
	}
}

func TestValidateEnginePathsRejectEscapedDuplicateKeys(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	path := filepath.Join(ws, "opencode.jsonc")
	raw := `{"default_agent": "build", "default_` + jsonUEscape("0061") + `gent": "plan"}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err := validateOpencodeDocument(ws, raw)
	if err == nil || !strings.Contains(err.Error(), `duplicate top-level key "default_agent"`) {
		t.Fatalf("validateOpencodeDocument() error = %v, want escaped duplicate-key error", err)
	}
	if err := validateEngineAgent(ws, "build"); err == nil ||
		!strings.Contains(err.Error(), `duplicate top-level key "default_agent"`) {
		t.Fatalf("validateEngineAgent() error = %v, want escaped duplicate-key error", err)
	}
}

func TestParseEngineDocRejectsNonObject(t *testing.T) {
	for _, raw := range []string{"null", `["model"]`, `"model"`, `42`, `true`} {
		if _, err := parseEngineDoc(raw); err == nil || !strings.Contains(err.Error(), "top-level value must be an object") {
			t.Errorf("parseEngineDoc(%s) error = %v, want top-level-object error", raw, err)
		}
	}
	// The shared decoder guards parseJSONC too.
	if err := parseJSONC("null", &opencodeEngineDoc{}); err == nil ||
		!strings.Contains(err.Error(), "top-level value must be an object") {
		t.Errorf("parseJSONC(null) error = %v, want top-level-object error", err)
	}
}

func TestEditOpencodeRejectsNonObjectAndRollsBack(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	original := readOpencode(t, ws)

	changed, err := EditOpencode(ws, func(path string) error {
		return os.WriteFile(path, []byte("null\n"), 0644)
	})
	if err == nil || !strings.Contains(err.Error(), "top-level value must be an object") {
		t.Fatalf("EditOpencode() error = %v, want top-level-object error", err)
	}
	if changed {
		t.Error("EditOpencode() changed = true, want false")
	}
	if got := readOpencode(t, ws); got != original {
		t.Errorf("original not restored:\n%s", got)
	}
}

func TestEditOpencodePreservesConflictingDirectory(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	path := filepath.Join(ws, "opencode.jsonc")
	original := readOpencode(t, ws)

	changed, err := EditOpencode(ws, func(string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(path, "inner"), 0755)
	})
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("EditOpencode() error = %v, want restore error", err)
	}
	if changed {
		t.Error("EditOpencode() changed = true, want false")
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("config file missing after rollback: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("config path is not a regular file after rollback")
	}
	if got := readOpencode(t, ws); got != original {
		t.Errorf("original not restored:\n%s", got)
	}

	// The editor's directory survives under a unique sibling name.
	conflicts, err := filepath.Glob(filepath.Join(ws, "opencode.jsonc.conflict-*"))
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflict siblings = %v, %v; want exactly one", conflicts, err)
	}
	if inner, err := os.Stat(filepath.Join(conflicts[0], "inner")); err != nil || !inner.IsDir() {
		t.Errorf("preserved directory lost its content: %v", err)
	}
}
