package opencode

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"testing"
)

//go:embed contract/openapi-v2.2.7.json
var pinnedOpenAPI embed.FS

type openAPISpec struct {
	Info struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func TestPinnedOpenAPIContract(t *testing.T) {
	raw, err := pinnedOpenAPI.ReadFile("contract/openapi-v2.2.7.json")
	if err != nil {
		t.Fatalf("read pinned OpenAPI spec: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != PinnedOpenAPISHA256 {
		t.Fatalf("pinned openapi sha256 = %s, want %s (update contract.go deliberately when refreshing the spec)", got, PinnedOpenAPISHA256)
	}

	var spec openAPISpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode pinned OpenAPI spec: %v", err)
	}
	if spec.Info.Title == "" {
		t.Fatal("pinned OpenAPI spec has no title")
	}

	requirePathOps(t, spec,
		"/api/info", "get")
	requirePathOps(t, spec,
		"/api/session", "get", "post")
	requirePathOps(t, spec,
		"/api/session/{sessionID}", "get", "patch")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/agent", "post")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/model", "post")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/prompt", "post")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/synthetic", "post")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/inbox", "get")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/context", "get")
	requirePathOps(t, spec,
		"/api/session/{sessionID}/message", "get")
	requirePathOps(t, spec,
		"/api/experimental/session/{sessionID}/wait", "post")
	requirePathOps(t, spec,
		"/api/experimental/session/{sessionID}/export", "get")
	requirePathOps(t, spec,
		"/api/experimental/session/import", "post")

	requireSchema(t, spec, "Session.Info")
	requireSchemaProperties(t, spec, "Session.Info", "id", "time", "location")
	requireSchema(t, spec, "Session.Message.User")
	requireSchemaProperties(t, spec, "Session.Message.User", "id", "type", "time", "text")
	requireSchema(t, spec, "Session.Message.Synthetic")
	requireSchemaProperties(t, spec, "Session.Message.Synthetic", "id", "type", "time", "text", "description")
	requireSchema(t, spec, "Session.Message.Assistant")
	requireSchemaProperties(t, spec, "Session.Message.Assistant", "id", "type", "time", "agent", "model", "content")
	requireSchema(t, spec, "Session.Message.Assistant.Tool")
	requireSchemaProperties(t, spec, "Session.Message.Assistant.Tool", "type", "id", "name", "state", "time")
	requireSchema(t, spec, "Session.Message.ToolState.Completed")
	requireSchemaProperties(t, spec, "Session.Message.ToolState.Completed", "status", "input", "content")
	requireSchema(t, spec, "Session.Message.ToolState.Error")
	requireSchemaProperties(t, spec, "Session.Message.ToolState.Error", "status", "input", "error")
	requireSchema(t, spec, "Session.Message.Idle")
	requireSchemaProperties(t, spec, "Session.Message.Idle", "id", "type", "time", "outcome")
	requireSchema(t, spec, "Session.Message.Shell")
	requireSchemaProperties(t, spec, "Session.Message.Shell", "type", "command", "status")
	requireSchema(t, spec, "Session.Message.Skill")
	requireSchemaProperties(t, spec, "Session.Message.Skill", "type", "skill", "name", "text")
	requireSchema(t, spec, "Session.Message.Compaction")
	requireSchema(t, spec, "SessionMessagesResponse")
	requireSchema(t, spec, "SessionTransfer.Data")
	requireSchemaProperties(t, spec, "SessionTransfer.Data", "info", "messages")
}

func requirePathOps(t *testing.T, spec openAPISpec, path string, methods ...string) {
	t.Helper()
	operations, ok := spec.Paths[path]
	if !ok {
		t.Fatalf("pinned OpenAPI spec is missing path %s", path)
	}
	for _, method := range methods {
		if _, ok := operations[method]; !ok {
			t.Errorf("pinned OpenAPI spec is missing %s %s", method, path)
		}
	}
}

func requireSchema(t *testing.T, spec openAPISpec, name string) {
	t.Helper()
	if _, ok := spec.Components.Schemas[name]; !ok {
		t.Fatalf("pinned OpenAPI spec is missing schema %s", name)
	}
}

func requireSchemaProperties(t *testing.T, spec openAPISpec, name string, properties ...string) {
	t.Helper()
	raw, ok := spec.Components.Schemas[name]
	if !ok {
		t.Fatalf("pinned OpenAPI spec is missing schema %s", name)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema %s: %v", name, err)
	}
	for _, property := range properties {
		if _, ok := schema.Properties[property]; !ok {
			t.Errorf("schema %s is missing property %s", name, property)
		}
	}
}
