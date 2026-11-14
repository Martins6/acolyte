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
		"/api/event", "get")
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

	// Request-body properties the thin client sends. Guarding them keeps a
	// refreshed spec from silently dropping a field the client depends on.
	requireRequestBodyProperties(t, spec,
		"/api/session", "post", "location", "agent", "model")
	requireRequestBodyProperties(t, spec,
		"/api/session/{sessionID}/prompt", "post", "text")
	requireRequestBodyProperties(t, spec,
		"/api/session/{sessionID}/synthetic", "post", "text", "description", "metadata", "delivery")
	requireRequestBodyProperties(t, spec,
		"/api/session/{sessionID}", "patch", "permissions")
	requireRequestBodyProperties(t, spec,
		"/api/session/{sessionID}/agent", "post", "agent")
	requireRequestBodyProperties(t, spec,
		"/api/session/{sessionID}/model", "post", "model")
	requireRequestBodyProperties(t, spec,
		"/api/experimental/session/import", "post", "info", "messages", "location")

	// Query parameters the thin client uses (or may use) on list/stream reads.
	requireQueryParameters(t, spec,
		"/api/session", "get", "limit", "order", "cursor")
	requireQueryParameters(t, spec,
		"/api/session/{sessionID}/message", "get", "limit", "order", "cursor")
	requireQueryParameters(t, spec,
		"/api/event", "get", "types")

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
	requireSchema(t, spec, "V2EventEncoded")
	requireSchema(t, spec, "V2Event")
	requireSchema(t, spec, "session.idle")
	requireSchemaProperties(t, spec, "session.idle", "id", "type", "data")
	requireSchema(t, spec, "session.text.delta")
	requireSchemaProperties(t, spec, "session.text.delta", "id", "type", "data")
	requireSchema(t, spec, "session.tool.called")
	requireSchema(t, spec, "session.tool.success")
	requireSchema(t, spec, "session.tool.failed")
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

// openAPIOperation is the subset of a path operation the contract test
// inspects.
type openAPIOperation struct {
	Parameters []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

func requireOperation(t *testing.T, spec openAPISpec, path, method string) openAPIOperation {
	t.Helper()
	raw, ok := spec.Paths[path][method]
	if !ok {
		t.Fatalf("pinned OpenAPI spec is missing %s %s", method, path)
	}
	var op openAPIOperation
	if err := json.Unmarshal(raw, &op); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return op
}

func requireRequestBodyProperties(t *testing.T, spec openAPISpec, path, method string, properties ...string) {
	t.Helper()
	op := requireOperation(t, spec, path, method)
	if op.RequestBody == nil {
		t.Fatalf("%s %s has no request body", method, path)
	}
	media, ok := op.RequestBody.Content["application/json"]
	if !ok {
		t.Fatalf("%s %s has no application/json request body", method, path)
	}
	for _, property := range properties {
		if _, ok := media.Schema.Properties[property]; !ok {
			t.Errorf("%s %s request body is missing property %s", method, path, property)
		}
	}
}

func requireQueryParameters(t *testing.T, spec openAPISpec, path, method string, names ...string) {
	t.Helper()
	op := requireOperation(t, spec, path, method)
	params := make(map[string]bool, len(op.Parameters))
	for _, param := range op.Parameters {
		if param.In == "query" {
			params[param.Name] = true
		}
	}
	for _, name := range names {
		if !params[name] {
			t.Errorf("%s %s is missing query parameter %s", method, path, name)
		}
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
