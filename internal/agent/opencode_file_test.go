package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/workspace"
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

// TestResolveDefinedAgentsReadsLegacyAndCanonicalBlocks pins backward
// compatibility: existing workspaces may still carry the legacy singular
// "agent" map, while new scaffolds emit the canonical "agents" map.
func TestResolveDefinedAgentsReadsLegacyAndCanonicalBlocks(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "legacy agent block", raw: `{"agent":{"custom":{"prompt":"legacy"}}}`},
		{name: "canonical agents block", raw: `{"agents":{"custom":{"system":"canonical"}}}`},
	}
	want := []string{"build", "custom", "plan"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, opencodeConfigName), []byte(tc.raw), 0644); err != nil {
				t.Fatalf("write opencode.jsonc: %v", err)
			}
			doc, err := parseEngineDoc(tc.raw)
			if err != nil {
				t.Fatalf("parseEngineDoc() error: %v", err)
			}
			got, err := resolveDefinedAgents(ws, doc)
			if err != nil {
				t.Fatalf("resolveDefinedAgents() error: %v", err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("resolveDefinedAgents() = %v, want %v", got, want)
			}
		})
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

func TestSetOpencodeInstructionPreservesCommentsAndEntries(t *testing.T) {
	raw := `// top comment
{
  "instructions": [
    // persona entry
    ".acolyte/*.md",
    "extra.md" // keep this entry
  ]
}
`
	removed, err := setOpencodeInstruction(raw, workspace.PersonaInstruction, false)
	if err != nil {
		t.Fatalf("setOpencodeInstruction(remove) error: %v", err)
	}
	for _, want := range []string{"// top comment", "// persona entry", "// keep this entry", `"extra.md"`, `"instructions"`} {
		if !strings.Contains(removed, want) {
			t.Errorf("removed document missing %q:\n%s", want, removed)
		}
	}
	if strings.Contains(removed, workspace.PersonaInstruction) {
		t.Errorf("persona entry survived removal:\n%s", removed)
	}
	var doc struct {
		Instructions []string `json:"instructions"`
	}
	if err := parseJSONC(removed, &doc); err != nil {
		t.Fatalf("removed document is not valid JSONC: %v\n%s", err, removed)
	}
	if len(doc.Instructions) != 1 || doc.Instructions[0] != "extra.md" {
		t.Errorf("instructions = %v, want [extra.md]", doc.Instructions)
	}

	restored, err := setOpencodeInstruction(removed, workspace.PersonaInstruction, true)
	if err != nil {
		t.Fatalf("setOpencodeInstruction(add) error: %v", err)
	}
	for _, want := range []string{"// top comment", "// persona entry", "// keep this entry", `"extra.md"`} {
		if !strings.Contains(restored, want) {
			t.Errorf("restored document missing %q:\n%s", want, restored)
		}
	}
	var restoredDoc struct {
		Instructions []string `json:"instructions"`
	}
	if err := parseJSONC(restored, &restoredDoc); err != nil {
		t.Fatalf("restored document is not valid JSONC: %v\n%s", err, restored)
	}
	if len(restoredDoc.Instructions) != 2 ||
		restoredDoc.Instructions[0] != "extra.md" ||
		restoredDoc.Instructions[1] != workspace.PersonaInstruction {
		t.Errorf("instructions = %v, want extra.md + persona", restoredDoc.Instructions)
	}
}

func TestSetOpencodeInstructionInsertsMissingKey(t *testing.T) {
	raw := "// top comment\n{\n  \"model\": \"acme/model-1\" // keep\n}\n"
	updated, err := setOpencodeInstruction(raw, workspace.PersonaInstruction, true)
	if err != nil {
		t.Fatalf("setOpencodeInstruction(add) error: %v", err)
	}
	for _, want := range []string{"// top comment", "// keep", `"model": "acme/model-1"`, `"instructions": [".acolyte/*.md"]`} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated document missing %q:\n%s", want, updated)
		}
	}
	var doc struct {
		Instructions []string `json:"instructions"`
	}
	if err := parseJSONC(updated, &doc); err != nil {
		t.Fatalf("updated document is not valid JSONC: %v\n%s", err, updated)
	}
	if len(doc.Instructions) != 1 || doc.Instructions[0] != workspace.PersonaInstruction {
		t.Errorf("instructions = %v", doc.Instructions)
	}

	// Removing from an absent array is a no-op.
	without, err := setOpencodeInstruction(`{"model": "acme/model-1"}`, workspace.PersonaInstruction, false)
	if err != nil {
		t.Fatalf("setOpencodeInstruction(remove absent) error: %v", err)
	}
	if without != `{"model": "acme/model-1"}` {
		t.Errorf("remove on absent key changed the document:\n%s", without)
	}

	// A trailing comma before the root brace is legal JSONC and must be
	// reused rather than doubled.
	trailing := "{\n  \"model\": \"acme/model-1\",\n}\n"
	inserted, err := setOpencodeInstruction(trailing, workspace.PersonaInstruction, true)
	if err != nil {
		t.Fatalf("setOpencodeInstruction(trailing comma) error: %v", err)
	}
	if strings.Contains(inserted, ",,") {
		t.Errorf("insertion doubled the trailing comma:\n%s", inserted)
	}
	if err := parseJSONC(inserted, &doc); err != nil {
		t.Fatalf("trailing-comma document is not valid JSONC: %v\n%s", err, inserted)
	}
	if len(doc.Instructions) != 1 || doc.Instructions[0] != workspace.PersonaInstruction {
		t.Errorf("instructions = %v", doc.Instructions)
	}
}

func TestSetOpencodeInstructionRejectsAmbiguousShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "duplicate key",
			raw:  `{"instructions": [".acolyte/*.md"], "instructions": ["extra.md"]}`,
			want: "duplicate top-level key",
		},
		{
			name: "escaped duplicate key",
			raw:  `{"instructions": [".acolyte/*.md"], "instructi` + jsonUEscape("006f") + `ns": []}`,
			want: "duplicate top-level key",
		},
		{
			name: "string value",
			raw:  `{"instructions": ".acolyte/*.md"}`,
			want: "is not an array",
		},
		{
			name: "object value",
			raw:  `{"instructions": {".acolyte/*.md": true}}`,
			want: "is not an array",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, present := range []bool{true, false} {
				if _, err := setOpencodeInstruction(tc.raw, workspace.PersonaInstruction, present); err == nil ||
					!strings.Contains(err.Error(), tc.want) {
					t.Errorf("present=%v error = %v, want containing %q", present, err, tc.want)
				}
			}
		})
	}
}

func TestSetOpencodeInstructionRemovesSoleEntry(t *testing.T) {
	cases := []string{
		`{"instructions": [".acolyte/*.md"]}`,
		"{\n  \"instructions\": [\n    \".acolyte/*.md\"\n  ]\n}\n",
		`{"instructions": [".acolyte/*.md",]}`,
	}
	for _, raw := range cases {
		removed, err := setOpencodeInstruction(raw, workspace.PersonaInstruction, false)
		if err != nil {
			t.Fatalf("setOpencodeInstruction(%s) error: %v", raw, err)
		}
		var doc struct {
			Instructions []string `json:"instructions"`
		}
		if err := parseJSONC(removed, &doc); err != nil {
			t.Fatalf("removed document is not valid JSONC: %v\n%s", err, removed)
		}
		if len(doc.Instructions) != 0 {
			t.Errorf("instructions = %v, want empty\n%s", doc.Instructions, removed)
		}
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
