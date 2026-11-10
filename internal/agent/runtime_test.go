package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

// createRuntimeAgent scaffolds + registers "valet" with the given --config.
func createRuntimeAgent(t *testing.T, raw string) string {
	t.Helper()
	_, ws := setup(t)
	cfg := createTestConfig(t, raw)
	if _, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg}); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return ws
}

func openRuntimeStore(t *testing.T, ws string) *store.Store {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func runtimeAgent(t *testing.T, name string) registry.Agent {
	t.Helper()
	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	agent, err := reg.GetByName(name)
	if err != nil {
		t.Fatalf("GetByName(%q): %v", name, err)
	}
	return agent
}

func readOpencode(t *testing.T, ws string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ws, "opencode.jsonc"))
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	return string(raw)
}

func TestLoadConfigFreshDefaults(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)

	cfg, err := LoadConfig(st, runtimeAgent(t, "valet"))
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	want := Config{
		Engine: EngineConfig{Model: "", Agent: "build"},
		Communication: CommunicationConfig{
			Channels: []string{"telegram"},
			Telegram: TelegramConfig{AllowedUsers: []string{}},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigFull(t *testing.T) {
	ws := createRuntimeAgent(t, `{
		"description": "Personal assistant",
		"engine": { "model": "minimax-coding-plan/MiniMax-M3", "agent": "build" },
		"memory-load": { "mode": "default" },
		"memory-access": { "mode": "native" },
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"memory-export": { "mode": "opencode-export" },
		"communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
	st := openRuntimeStore(t, ws)

	cfg, err := LoadConfig(st, runtimeAgent(t, "valet"))
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	want := Config{
		Description:  "Personal assistant",
		Engine:       EngineConfig{Model: "minimax-coding-plan/MiniMax-M3", Agent: "build"},
		MemoryLoad:   &MemoryConfig{Mode: "default"},
		MemoryAccess: &MemoryConfig{Mode: "native"},
		MemoryLearn:  &MemoryConfig{Mode: "simple-dreaming", Args: map[string]any{"days": 3}},
		MemoryExport: &MemoryConfig{Mode: "opencode-export"},
		Communication: CommunicationConfig{
			Channels: []string{"telegram"},
			Telegram: TelegramConfig{AllowedUsers: []string{"@you"}},
		},
		Schedule: ScheduleConfig{Timezone: "UTC", HeartbeatMinutes: 60},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("LoadConfig() = %+v, want %+v", cfg, want)
	}
}

func TestSetEngineModelPreservesCommentsAndIsIdempotent(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	path := filepath.Join(ws, "opencode.jsonc")
	original := readOpencode(t, ws)
	annotated := "// keep this comment\n" + original
	if err := os.WriteFile(path, []byte(annotated), 0644); err != nil {
		t.Fatalf("annotate opencode.jsonc: %v", err)
	}

	st := openRuntimeStore(t, ws)
	res, err := SetConfig(st, runtimeAgent(t, "valet"), SetOptions{Key: "engine.model", Value: "acme/model-1"})
	if err != nil {
		t.Fatalf("SetConfig(model) error: %v", err)
	}
	if !res.Changed || res.OpencodeDiff == "" {
		t.Fatalf("result = %+v, want changed with a diff", res)
	}

	updated := readOpencode(t, ws)
	for _, want := range []string{"// keep this comment", `"model": "acme/model-1"`, `"$schema"`, `"instructions"`, `"default_agent"`} {
		if !strings.Contains(updated, want) {
			t.Errorf("opencode.jsonc missing %q:\n%s", want, updated)
		}
	}
	var doc opencodeEngineDoc
	if err := parseJSONC(updated, &doc); err != nil {
		t.Fatalf("updated file is not valid JSONC: %v", err)
	}
	if doc.Model != "acme/model-1" {
		t.Errorf("model = %q", doc.Model)
	}

	// Idempotent: a second set changes nothing and rewrites nothing.
	res, err = SetConfig(st, runtimeAgent(t, "valet"), SetOptions{Key: "engine.model", Value: "acme/model-1"})
	if err != nil {
		t.Fatalf("SetConfig(model again) error: %v", err)
	}
	if res.Changed {
		t.Errorf("second set Changed = true, want false")
	}
	if again := readOpencode(t, ws); again != updated {
		t.Errorf("second set rewrote opencode.jsonc:\n%s", again)
	}
}

func TestSetEngineAgentRequiresDefinedAgent(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "engine.agent", Value: "ghost"}); err == nil ||
		!strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("SetConfig(ghost agent) error = %v, want not-defined error", err)
	}

	if _, err := SetConfig(st, target, SetOptions{Key: "engine.agent", Value: "plan"}); err != nil {
		t.Fatalf("SetConfig(plan) error: %v", err)
	}
	var doc opencodeEngineDoc
	if err := parseJSONC(readOpencode(t, ws), &doc); err != nil {
		t.Fatalf("parse opencode.jsonc: %v", err)
	}
	if doc.DefaultAgent != "plan" {
		t.Errorf("default_agent = %q, want plan", doc.DefaultAgent)
	}
}

func TestSetHeartbeatRequiresTimezone(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)
	_, err := SetConfig(st, runtimeAgent(t, "valet"), SetOptions{Key: "schedule.heartbeat_minutes", Value: "60"})
	if err == nil || !strings.Contains(err.Error(), "timezone is required") {
		t.Fatalf("SetConfig(heartbeat without timezone) error = %v, want timezone-required error", err)
	}
}

func TestSetHeartbeatCreatesUpdatesRemoves(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: "60"}); err != nil {
		t.Fatalf("SetConfig(60) error: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("get heartbeat: %v", err)
	}
	if job.Spec != "@every 60m" || job.Routine != "heartbeat" || job.Timezone != "UTC" || job.NextRunAt == nil {
		t.Errorf("job = %+v", job)
	}
	firstNext := job.NextRunAt

	// Re-setting the same interval must not reset next_run_at.
	if res, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: "60"}); err != nil {
		t.Fatalf("SetConfig(60 again) error: %v", err)
	} else if res.Changed {
		t.Error("second set Changed = true, want false")
	}
	if job, err := st.Orch().GetCronJobByTitle("heartbeat"); err != nil || !job.NextRunAt.Equal(*firstNext) {
		t.Errorf("next_run_at changed on re-set: %+v", job)
	}

	if _, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: "15"}); err != nil {
		t.Fatalf("SetConfig(15) error: %v", err)
	}
	if job, err := st.Orch().GetCronJobByTitle("heartbeat"); err != nil || job.Spec != "@every 15m" {
		t.Errorf("job after update = %+v, %v", job, err)
	}

	if _, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: "0"}); err != nil {
		t.Fatalf("SetConfig(0) error: %v", err)
	}
	if _, err := st.Orch().GetCronJobByTitle("heartbeat"); err == nil {
		t.Fatal("heartbeat job still exists after set 0")
	}
}

func TestSetMemoryLearnAddsAndRemovesDream(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "memory-learn", Value: `{"args":{"days":5}}`}); err != nil {
		t.Fatalf("SetConfig(memory-learn) error: %v", err)
	}
	job, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("get dream: %v", err)
	}
	if job.Spec != "@every 120h" || !strings.Contains(job.RoutineArgs, "5") {
		t.Errorf("dream job = %+v", job)
	}
	setting, ok, err := st.Orch().GetSetting("memory.learn")
	if err != nil || !ok || !strings.Contains(setting, "simple-dreaming") {
		t.Errorf("memory.learn = %q, %v, %v", setting, ok, err)
	}

	if _, err := SetConfig(st, target, SetOptions{Key: "memory-learn", Value: "null"}); err != nil {
		t.Fatalf("SetConfig(memory-learn null) error: %v", err)
	}
	if _, err := st.Orch().GetCronJobByTitle("dream"); err == nil {
		t.Fatal("dream job still exists after null")
	}
	if _, ok, err := st.Orch().GetSetting("memory.learn"); err != nil || ok {
		t.Errorf("memory.learn still set: %v, %v", ok, err)
	}
}

func TestSetMemoryLearnRequiresTimezone(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)
	_, err := SetConfig(st, runtimeAgent(t, "valet"), SetOptions{Key: "memory-learn", Value: `{"args":{"days":3}}`})
	if err == nil || !strings.Contains(err.Error(), "timezone is required") {
		t.Fatalf("error = %v, want timezone-required error", err)
	}
}

func TestSetCommunication(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "communication.channels", Value: `[]`}); err != nil {
		t.Fatalf("SetConfig(channels []) error: %v", err)
	}
	if raw, ok, err := st.CommTG().GetSetting("channels"); err != nil || !ok || raw != "[]" {
		t.Errorf("channels = %q, %v, %v", raw, ok, err)
	}
	cfg, err := LoadConfig(st, target)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if len(cfg.Communication.Channels) != 0 {
		t.Errorf("channels = %v, want empty", cfg.Communication.Channels)
	}

	if _, err := SetConfig(st, target, SetOptions{Key: "communication.channels", Value: `["telegram"]`}); err != nil {
		t.Fatalf("SetConfig(channels) error: %v", err)
	}
	_, err = SetConfig(st, target, SetOptions{Key: "communication.channels", Value: `["sms"]`})
	if err == nil || !strings.Contains(err.Error(), "not available in v0") {
		t.Fatalf("channels [sms] error = %v, want v0 error", err)
	}

	// Comma-separated input is normalized to a deduped JSON array.
	if _, err := SetConfig(st, target, SetOptions{
		Key: "communication.telegram.allowed_users", Value: "@alice, @bob,@alice",
	}); err != nil {
		t.Fatalf("SetConfig(allowed_users) error: %v", err)
	}
	raw, ok, err := st.CommTG().GetSetting("telegram.allowed_users")
	if err != nil || !ok || raw != `["@alice","@bob"]` {
		t.Errorf("allowed_users = %q, %v, %v", raw, ok, err)
	}
}

func TestSetDryRunLeavesStateUnchanged(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	path := filepath.Join(ws, "opencode.jsonc")
	if err := os.WriteFile(path, []byte("// marker\n"+readOpencode(t, ws)), 0644); err != nil {
		t.Fatalf("annotate opencode.jsonc: %v", err)
	}
	before := readOpencode(t, ws)

	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")
	settingsBefore, err := st.Orch().ListSettings()
	if err != nil {
		t.Fatalf("ListSettings(): %v", err)
	}
	jobsBefore, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("ListCronJobs(): %v", err)
	}

	model, err := SetConfig(st, target, SetOptions{Key: "engine.model", Value: "acme/model-1", DryRun: true})
	if err != nil {
		t.Fatalf("SetConfig(model dry-run) error: %v", err)
	}
	if !model.DryRun || !model.Changed || model.OpencodeDiff == "" {
		t.Errorf("model result = %+v, want dry-run changed with diff", model)
	}
	heartbeat, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: "30", DryRun: true})
	if err != nil {
		t.Fatalf("SetConfig(heartbeat dry-run) error: %v", err)
	}
	if !heartbeat.DryRun || !heartbeat.Changed || len(heartbeat.Jobs) != 1 {
		t.Errorf("heartbeat result = %+v, want a planned job", heartbeat)
	}

	if after := readOpencode(t, ws); after != before {
		t.Errorf("dry-run rewrote opencode.jsonc:\n%s", after)
	}
	settingsAfter, err := st.Orch().ListSettings()
	if err != nil {
		t.Fatalf("ListSettings(): %v", err)
	}
	if !reflect.DeepEqual(settingsBefore, settingsAfter) {
		t.Errorf("dry-run changed settings: %v -> %v", settingsBefore, settingsAfter)
	}
	jobsAfter, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("ListCronJobs(): %v", err)
	}
	if !reflect.DeepEqual(jobsBefore, jobsAfter) {
		t.Errorf("dry-run changed jobs: %v -> %v", jobsBefore, jobsAfter)
	}
}

func TestSetDescriptionUpdatesRegistry(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "description", Value: "Night watch"}); err != nil {
		t.Fatalf("SetConfig(description) error: %v", err)
	}
	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	updated, err := reg.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName(): %v", err)
	}
	if updated.Description != "Night watch" {
		t.Errorf("description = %q", updated.Description)
	}
	if cfg, err := LoadConfig(st, runtimeAgent(t, "valet")); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	} else if cfg.Description != "Night watch" {
		t.Errorf("effective description = %q", cfg.Description)
	}
}

func TestSetTimezoneReTimesRoutineJobs(t *testing.T) {
	ws := createRuntimeAgent(t, `{
		"memory-learn": { "args": { "days": 3 } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	if _, err := SetConfig(st, target, SetOptions{Key: "schedule.timezone", Value: "America/Sao_Paulo"}); err != nil {
		t.Fatalf("SetConfig(timezone) error: %v", err)
	}
	setting, ok, err := st.Orch().GetSetting("schedule.timezone")
	if err != nil || !ok || setting != "America/Sao_Paulo" {
		t.Errorf("schedule.timezone = %q, %v, %v", setting, ok, err)
	}
	for _, title := range []string{"dream", "heartbeat"} {
		job, err := st.Orch().GetCronJobByTitle(title)
		if err != nil {
			t.Fatalf("get %s: %v", title, err)
		}
		if job.Timezone != "America/Sao_Paulo" {
			t.Errorf("%s timezone = %q", title, job.Timezone)
		}
		if job.NextRunAt == nil {
			t.Errorf("%s next_run_at is nil", title)
		}
	}
}

func TestSetInvalidValues(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")

	cases := []struct {
		key, value string
		want       string
	}{
		{"unknown.key", "1", "unsupported key"},
		{"", "1", "key is required"},
		{"engine.model", "", "must not be empty"},
		{"engine.model", "no-slash", "provider/model"},
		{"engine.agent", "", "must not be empty"},
		{"schedule.timezone", "Not/A/Zone", "invalid schedule.timezone"},
		{"schedule.heartbeat_minutes", "soon", "must be an integer"},
		{"schedule.heartbeat_minutes", "-5", "must be >= 0"},
		{"communication.channels", "telegram", "JSON array"},
		{"communication.telegram.allowed_users", "[1]", "JSON array"},
		{"memory-learn", `{"mode":"post-session"}`, "not available in v0"},
		{"memory-learn", `{"args":{"weeks":1}}`, "unsupported arg"},
		{"memory-load", `{"mode":"compact"}`, "not available in v0"},
		{"memory-load", `{"bogus":1}`, "unknown field"},
		{"memory-export", `[]`, "JSON object or null"},
		{"memory-learn", ``, "JSON object or null"},
	}
	for _, tc := range cases {
		_, err := SetConfig(st, target, SetOptions{Key: tc.key, Value: tc.value})
		if err == nil {
			t.Errorf("SetConfig(%q, %q) error = nil, want error", tc.key, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("SetConfig(%q, %q) error = %v, want containing %q", tc.key, tc.value, err, tc.want)
		}
	}
}

func TestMaskJSONC(t *testing.T) {
	raw := `{
  // a line comment with "model": "fake" and }
  "model": "acme/real", /* inline } comment */
  "instructions": ["// not a comment", "/* also not */"],
  "default_agent": "build", // trailing comma follows
}`
	masked, err := maskJSONC(raw)
	if err != nil {
		t.Fatalf("maskJSONC() error: %v", err)
	}
	var doc opencodeEngineDoc
	if err := parseJSONC(raw, &doc); err != nil {
		t.Fatalf("parseJSONC() error: %v", err)
	}
	if doc.Model != "acme/real" {
		t.Errorf("model = %q, want acme/real", doc.Model)
	}
	// The mask keeps byte offsets and only blanks comments: the real key is
	// still matchable at depth 1, the commented-out one is gone.
	if strings.Contains(masked, `"model": "fake"`) {
		t.Error("commented model key should be masked out")
	}
	index := strings.Index(masked, `"model": "acme/real"`)
	if index < 0 {
		t.Fatalf("real model key missing from mask:\n%s", masked)
	}
	if jsoncDepth(masked, index) != 1 {
		t.Errorf("real model key depth = %d, want 1", jsoncDepth(masked, index))
	}
	// A quoted key inside a string literal is not a top-level key.
	nested, err := maskJSONC(`{"instructions": ["x"], "model": "acme/real"}`)
	if err != nil {
		t.Fatalf("maskJSONC(nested) error: %v", err)
	}
	if got := jsoncDepth(nested, strings.Index(nested, `"model"`)); got != 1 {
		t.Errorf("top-level key depth = %d, want 1", got)
	}
	inString, err := maskJSONC(`{"note": "he said \"model\": \"fake\""}`)
	if err != nil {
		t.Fatalf("maskJSONC(inString) error: %v", err)
	}
	if got := jsoncDepth(inString, strings.Index(inString, `model`)); got != -1 {
		t.Errorf("in-string key depth = %d, want -1", got)
	}
}

// TestMaskJSONCRejectsMalformed pins the malformed-document rejections: an
// unterminated "/*" must never mask into valid JSON (that would let `edit`
// accept a broken opencode.jsonc instead of rolling it back).
func TestMaskJSONCRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // error substring; empty means the document must parse
	}{
		{name: "block comment at eof", raw: `{"a": 1} /*`, want: "unterminated block comment"},
		{name: "block comment never closes", raw: "{\n  \"a\": 1,\n  /* dangling\n}", want: "unterminated block comment"},
		{name: "unterminated string", raw: `{"a": "b`, want: "unterminated string"},
		{name: "unterminated string after comment", raw: "{ // ok\n  \"a\": \"x\n}", want: "unterminated string"},
		{name: "comment markers inside string", raw: `{"a": "/* not a comment */ // nope"}`, want: ""},
		{name: "valid block comment", raw: "{\n  /* c */ \"a\": 1 // line\n}", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			masked, err := maskJSONC(tc.raw)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("maskJSONC() error = %v, want nil", err)
				}
				var value map[string]any
				if err := json.Unmarshal([]byte(masked), &value); err != nil {
					t.Fatalf("masked output is not valid JSON: %v\n%s", err, masked)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("maskJSONC() error = %v, want containing %q", err, tc.want)
				}
			}
			var value map[string]any
			err = parseJSONC(tc.raw, &value)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("parseJSONC() error = %v, want nil", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseJSONC() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestEditOpencodeRejectsUnterminatedComment(t *testing.T) {
	ws := createRuntimeAgent(t, `{}`)
	original := readOpencode(t, ws)

	changed, err := EditOpencode(ws, func(path string) error {
		return os.WriteFile(path, []byte(original+"\n/* dangling\n"), 0644)
	})
	if err == nil || !strings.Contains(err.Error(), "unterminated block comment") {
		t.Fatalf("EditOpencode() error = %v, want unterminated-block-comment error", err)
	}
	if changed {
		t.Error("EditOpencode() changed = true, want false")
	}
	if restored := readOpencode(t, ws); restored != original {
		t.Errorf("invalid edit was not rolled back:\n%s", restored)
	}
}

func TestSetOpencodeStringInsertsMissingKey(t *testing.T) {
	raw := "// top comment\n{\n  \"instructions\": [\".acolyte/*.md\"] // keep\n}\n"
	updated, err := setOpencodeString(raw, "model", "acme/model-1")
	if err != nil {
		t.Fatalf("setOpencodeString() error: %v", err)
	}
	for _, want := range []string{"// top comment", "// keep", `"instructions"`, `"model": "acme/model-1"`} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated missing %q:\n%s", want, updated)
		}
	}
	var doc opencodeEngineDoc
	if err := parseJSONC(updated, &doc); err != nil {
		t.Fatalf("updated is not valid JSONC: %v\n%s", err, updated)
	}
	if doc.Model != "acme/model-1" {
		t.Errorf("model = %q", doc.Model)
	}
}

func TestSetOpencodeStringRefusesNonStringValue(t *testing.T) {
	_, err := setOpencodeString(`{"model": 42}`, "model", "acme/model-1")
	if err == nil || !strings.Contains(err.Error(), "is not a string value") {
		t.Fatalf("error = %v, want non-string error", err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	diff := unifiedDiff("opencode.jsonc", "opencode.jsonc", "a\nb\nc\n", "a\nB\nc\n")
	for _, want := range []string{"--- a/opencode.jsonc", "+++ b/opencode.jsonc", "@@", "-b", "+B"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	if unifiedDiff("x", "x", "same", "same") != "" {
		t.Error("identical texts should produce no diff")
	}
}

// addUserCronJob inserts a user-owned job (owner "user", like `acolyte cron`)
// and returns its stored snapshot for before/after comparisons.
func addUserCronJob(t *testing.T, st *store.Store, job orch.CronJob) orch.CronJob {
	t.Helper()
	if err := st.Orch().AddCronJob(job); err != nil {
		t.Fatalf("AddCronJob(%q): %v", job.Title, err)
	}
	stored, err := st.Orch().GetCronJobByTitle(job.Title)
	if err != nil {
		t.Fatalf("GetCronJobByTitle(%q): %v", job.Title, err)
	}
	return stored
}

func userCapabilityJobs(t *testing.T, st *store.Store) (orch.CronJob, orch.CronJob) {
	t.Helper()
	next := time.Now().UTC().Add(time.Hour)
	dream := addUserCronJob(t, st, orch.CronJob{
		Title: "dream", Owner: "user", Kind: "prompt", Prompt: "hello",
		Spec: "@every 1h", AgentTarget: "valet", Timezone: "UTC",
		Enabled: true, NextRunAt: &next,
	})
	heartbeat := addUserCronJob(t, st, orch.CronJob{
		Title: "heartbeat", Owner: "user", Kind: "routine", Routine: "heartbeat",
		RoutineArgs: `{"interval":5}`, Spec: "@every 5m", AgentTarget: "valet",
		Timezone: "UTC", Enabled: true, NextRunAt: &next,
	})
	return dream, heartbeat
}

// TestLoadConfigIgnoresUserJobTitles: a user job holding a capability title
// is not reported as the capability by `get`.
func TestLoadConfigIgnoresUserJobTitles(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")
	dreamBefore, heartbeatBefore := userCapabilityJobs(t, st)

	cfg, err := LoadConfig(st, target)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.MemoryLearn != nil {
		t.Errorf("memory-learn = %+v, want nil (user dream job is not the capability)", cfg.MemoryLearn)
	}
	if cfg.Schedule.HeartbeatMinutes != 0 {
		t.Errorf("heartbeat_minutes = %d, want 0 (user heartbeat job is not the capability)", cfg.Schedule.HeartbeatMinutes)
	}

	dreamAfter, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("get dream: %v", err)
	}
	heartbeatAfter, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("get heartbeat: %v", err)
	}
	if !reflect.DeepEqual(dreamBefore, dreamAfter) || !reflect.DeepEqual(heartbeatBefore, heartbeatAfter) {
		t.Error("LoadConfig modified the user jobs")
	}
}

// TestSetRefusesConflictingDreamJob: memory-learn mutations and timezone
// changes must reject — never delete or rewrite — a user job titled "dream".
func TestSetRefusesConflictingDreamJob(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")
	before := addUserCronJob(t, st, orch.CronJob{
		Title: "dream", Owner: "user", Kind: "prompt", Prompt: "hello",
		Spec: "@every 1h", AgentTarget: "valet", Timezone: "UTC",
		Enabled: true, NextRunAt: ptrTime(time.Now().UTC().Add(time.Hour)),
	})

	for _, value := range []string{"null", `{"args":{"days":3}}`} {
		_, err := SetConfig(st, target, SetOptions{Key: "memory-learn", Value: value})
		if err == nil {
			t.Fatalf("SetConfig(memory-learn %s) error = nil, want conflict error", value)
		}
		if !strings.Contains(err.Error(), `refusing to modify cron job "dream"`) ||
			!strings.Contains(err.Error(), `owner="user"`) {
			t.Fatalf("SetConfig(memory-learn %s) error = %v, want conflict error naming the job", value, err)
		}
	}

	_, err := SetConfig(st, target, SetOptions{Key: "schedule.timezone", Value: "America/Sao_Paulo"})
	if err == nil || !strings.Contains(err.Error(), `refusing to modify cron job "dream"`) {
		t.Fatalf("SetConfig(timezone) error = %v, want dream conflict error", err)
	}

	after, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("get dream: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("user dream job changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestSetRefusesConflictingHeartbeatJob: heartbeat_minutes and timezone must
// reject a user job titled "heartbeat" (even one running the heartbeat
// routine under owner "user") instead of deleting or rewriting it.
func TestSetRefusesConflictingHeartbeatJob(t *testing.T) {
	ws := createRuntimeAgent(t, `{"schedule": {"timezone": "UTC"}}`)
	st := openRuntimeStore(t, ws)
	target := runtimeAgent(t, "valet")
	before := addUserCronJob(t, st, orch.CronJob{
		Title: "heartbeat", Owner: "user", Kind: "routine", Routine: "heartbeat",
		RoutineArgs: `{"interval":5}`, Spec: "@every 5m", AgentTarget: "valet",
		Timezone: "UTC", Enabled: true, NextRunAt: ptrTime(time.Now().UTC().Add(time.Hour)),
	})

	for _, value := range []string{"0", "30"} {
		_, err := SetConfig(st, target, SetOptions{Key: "schedule.heartbeat_minutes", Value: value})
		if err == nil {
			t.Fatalf("SetConfig(heartbeat_minutes %s) error = nil, want conflict error", value)
		}
		if !strings.Contains(err.Error(), `refusing to modify cron job "heartbeat"`) ||
			!strings.Contains(err.Error(), `owner="user"`) {
			t.Fatalf("SetConfig(heartbeat_minutes %s) error = %v, want conflict error naming the job", value, err)
		}
	}

	_, err := SetConfig(st, target, SetOptions{Key: "schedule.timezone", Value: "America/Sao_Paulo"})
	if err == nil || !strings.Contains(err.Error(), `refusing to modify cron job "heartbeat"`) {
		t.Fatalf("SetConfig(timezone) error = %v, want heartbeat conflict error", err)
	}

	after, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("get heartbeat: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("user heartbeat job changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
