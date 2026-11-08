package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

func setup(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.HomeEnv, home)
	return home, filepath.Join(t.TempDir(), "agent")
}

func createTestConfig(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	return cfg
}

func fullTestConfig(t *testing.T) Config {
	t.Helper()
	return createTestConfig(t, `{
		"description": "Personal assistant",
		"engine": { "model": "minimax-coding-plan/MiniMax-M3", "agent": "build" },
		"memory-load": { "mode": "default" },
		"memory-access": { "mode": "native" },
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"memory-export": { "mode": "opencode-export" },
		"communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
}

func TestCreateFresh(t *testing.T) {
	home, ws := setup(t)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: fullTestConfig(t)})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.Adopted {
		t.Error("Adopted = true, want false")
	}
	wantWorkspace, err := paths.Canonical(ws)
	if err != nil {
		t.Fatalf("Canonical(workspace) error: %v", err)
	}
	if result.Agent.Name != "valet" || result.Agent.Workspace != wantWorkspace {
		t.Errorf("agent = %+v", result.Agent)
	}
	if result.Agent.Description != "Personal assistant" {
		t.Errorf("description = %q", result.Agent.Description)
	}
	if len(result.Jobs) != 2 {
		t.Fatalf("jobs = %v, want dream + heartbeat", result.Jobs)
	}

	for _, name := range []string{
		"AGENTS.md", "opencode.jsonc", ".acolyte/CONFIG.md", ".acolyte/SOUL.md",
		".acolyte.sqlite.db", ".gitignore",
	} {
		if _, err := os.Stat(filepath.Join(ws, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, ".agents", "skills")); err != nil {
		t.Errorf("missing .agents/skills: %v", err)
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	agents, err := reg.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "valet" {
		t.Errorf("registry agents = %+v", agents)
	}
	if _, err := os.Stat(filepath.Join(home, "registry.sqlite.db")); err != nil {
		t.Errorf("registry file missing: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("ListCronJobs() error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("stored jobs = %d, want 2", len(jobs))
	}
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.Title] = true
		if job.NextRunAt == nil {
			t.Errorf("job %s has nil next_run_at", job.Title)
		}
		if !job.Enabled {
			t.Errorf("job %s is disabled", job.Title)
		}
	}
	if !seen["dream"] || !seen["heartbeat"] {
		t.Errorf("jobs = %v, want dream + heartbeat", seen)
	}

	for _, key := range []string{"schedule.timezone", "memory.load", "memory.access", "memory.learn", "memory.export"} {
		if _, ok, err := st.Orch().GetSetting(key); err != nil || !ok {
			t.Errorf("setting %s = ok:%v err:%v", key, ok, err)
		}
	}
	value, ok, err := st.CommTG().GetSetting("telegram.allowed_users")
	if err != nil || !ok {
		t.Fatalf("telegram setting = ok:%v err:%v", ok, err)
	}
	if value != `["@you"]` {
		t.Errorf("allowed users = %s", value)
	}
}

func TestCreateDreamJobArgs(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 4 } },
		"schedule": { "timezone": "UTC" }
	}`)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].Title != "dream" {
		t.Fatalf("jobs = %+v, want dream", result.Jobs)
	}
	if result.Jobs[0].Spec != "@every 96h" {
		t.Errorf("spec = %q, want @every 96h", result.Jobs[0].Spec)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	var args map[string]int
	if err := json.Unmarshal([]byte(job.RoutineArgs), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["days"] != 4 {
		t.Errorf("args = %v, want days 4", args)
	}
	if job.Owner != "memory-learn" || job.Kind != "routine" || job.Routine != "dream" {
		t.Errorf("job = %+v", job)
	}
}

func TestCreateHeartbeatJobArgs(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{"schedule": {"timezone": "UTC", "heartbeat_minutes": 30}}`)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].Spec != "@every 30m" {
		t.Fatalf("jobs = %+v, want heartbeat @every 30m", result.Jobs)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("GetCronJobByTitle() error: %v", err)
	}
	if job.Owner != "heartbeat" || job.Routine != "heartbeat" {
		t.Errorf("job = %+v", job)
	}
}

func TestCreateAdopt(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{"description": "first"}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	soulPath := filepath.Join(ws, ".acolyte/SOUL.md")
	custom := "# Custom soul\n"
	if err := os.WriteFile(soulPath, []byte(custom), 0644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	second := createTestConfig(t, `{"description": "second"}`)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: second})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	if !result.Adopted {
		t.Error("Adopted = false, want true")
	}
	if result.Agent.Description != "second" {
		t.Errorf("description = %q, want second", result.Agent.Description)
	}
	raw, err := os.ReadFile(soulPath)
	if err != nil {
		t.Fatalf("read soul: %v", err)
	}
	if string(raw) != custom {
		t.Error("adopt overwrote the custom SOUL.md")
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	agents, err := reg.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("registry agents = %d, want 1", len(agents))
	}
}

func TestCreateAdoptKeepsExistingJobs(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
	first, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg})
	if err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}
	if len(first.Jobs) != 2 {
		t.Fatalf("first jobs = %v, want 2", first.Jobs)
	}

	second, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: cfg})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	for _, job := range second.Jobs {
		if job.Inserted {
			t.Errorf("job %s was inserted again on adopt", job.Title)
		}
	}
	if len(second.Jobs) != 2 {
		t.Errorf("second jobs = %v, want 2", second.Jobs)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("ListCronJobs() error: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("stored jobs = %d, want 2", len(jobs))
	}
}

func TestCreateAdoptPreservesDescription(t *testing.T) {
	_, ws := setup(t)
	if _, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config:    createTestConfig(t, `{"description": "first"}`),
	}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	result, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config:    createTestConfig(t, `{}`),
	})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	if result.Agent.Description != "first" {
		t.Errorf("description = %q, want first", result.Agent.Description)
	}
}

func TestCreateAdoptPreservesSettings(t *testing.T) {
	_, ws := setup(t)
	if _, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config: createTestConfig(t, `{
			"memory-export": { "mode": "opencode-export" },
			"communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
			"schedule": { "timezone": "UTC" }
		}`),
	}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	if _, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config: createTestConfig(t, `{
			"communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@other"] } },
			"schedule": { "timezone": "America/Sao_Paulo" }
		}`),
	}); err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	timezone, ok, err := st.Orch().GetSetting("schedule.timezone")
	if err != nil || !ok || timezone != "UTC" {
		t.Errorf("schedule.timezone = %q, %v, %v; want UTC", timezone, ok, err)
	}
	export, ok, err := st.Orch().GetSetting("memory.export")
	if err != nil || !ok || !strings.Contains(export, "opencode-export") {
		t.Errorf("memory.export = %q, %v, %v; want opencode-export", export, ok, err)
	}
	users, ok, err := st.CommTG().GetSetting("telegram.allowed_users")
	if err != nil || !ok || users != `["@you"]` {
		t.Errorf("allowed users = %q, %v, %v; want [\"@you\"]", users, ok, err)
	}
}

func TestCreateRejectsSymlinkedHome(t *testing.T) {
	home, _ := setup(t)
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: link,
		Config:    createTestConfig(t, `{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "ACOLYTE_HOME") {
		t.Fatalf("Create(symlinked home) error = %v, want ACOLYTE_HOME error", err)
	}
}

func TestCreateRejectsSymlinkedWorkspaceAlias(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "one", Workspace: ws, Config: cfg}); err != nil {
		t.Fatalf("Create(one) error: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(ws, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := Create(context.Background(), CreateOptions{Name: "two", Workspace: alias, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "already registered as") {
		t.Fatalf("Create(symlink alias) error = %v, want duplicate workspace error", err)
	}
}

func TestCreateCanonicalizesWorkspace(t *testing.T) {
	setup(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	result, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: alias,
		Config:    createTestConfig(t, `{}`),
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	want, err := paths.Canonical(target)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	if result.Agent.Workspace != want {
		t.Errorf("workspace = %q, want %q", result.Agent.Workspace, want)
	}
	if _, err := os.Stat(filepath.Join(target, "AGENTS.md")); err != nil {
		t.Errorf("target was not scaffolded: %v", err)
	}
}

func TestCreateAdoptPreservesExistingJobSettings(t *testing.T) {
	_, ws := setup(t)
	first := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 5 } },
		"schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 30 }
	}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: first}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	// Simulate an older/adopted workspace whose job exists but whose settings
	// were never written.
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM orch_settings`); err != nil {
		t.Fatalf("delete settings: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	second := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: second})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	for _, job := range result.Jobs {
		if job.Inserted {
			t.Errorf("job %s was inserted again on adopt", job.Title)
		}
	}

	st, err = store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	timezone, ok, err := st.Orch().GetSetting("schedule.timezone")
	if err != nil || !ok {
		t.Fatalf("schedule.timezone = %q, %v, %v", timezone, ok, err)
	}
	if timezone != "America/Sao_Paulo" {
		t.Errorf("schedule.timezone = %q, want the existing job timezone", timezone)
	}
	if _, ok, err := st.Orch().GetSetting("memory.learn"); err != nil || ok {
		t.Errorf("memory.learn = ok:%v err:%v, want absent (job is the source of truth)", ok, err)
	}
}

func TestCreateAdoptUsesPreservedTimezoneForNewJob(t *testing.T) {
	_, ws := setup(t)
	first := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"schedule": { "timezone": "America/Sao_Paulo" }
	}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: first}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	second := createTestConfig(t, `{
		"memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
		"schedule": { "timezone": "UTC", "heartbeat_minutes": 60 }
	}`)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: second})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	inserted := false
	for _, job := range result.Jobs {
		if job.Title == "heartbeat" && job.Inserted {
			inserted = true
		}
	}
	if !inserted {
		t.Fatalf("heartbeat was not inserted: %+v", result.Jobs)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("get heartbeat: %v", err)
	}
	if job.Timezone != "America/Sao_Paulo" {
		t.Errorf("heartbeat timezone = %q, want the preserved job timezone", job.Timezone)
	}
}

func TestCreateAdoptUsesExistingTimezoneSetting(t *testing.T) {
	_, ws := setup(t)
	if _, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config:    createTestConfig(t, `{"schedule": {"timezone": "UTC"}}`),
	}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}

	result, err := Create(context.Background(), CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config:    createTestConfig(t, `{"schedule": {"timezone": "America/Sao_Paulo", "heartbeat_minutes": 60}}`),
	})
	if err != nil {
		t.Fatalf("Create(second) error: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].Title != "heartbeat" || !result.Jobs[0].Inserted {
		t.Fatalf("jobs = %+v, want inserted heartbeat", result.Jobs)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("heartbeat")
	if err != nil {
		t.Fatalf("get heartbeat: %v", err)
	}
	if job.Timezone != "UTC" {
		t.Errorf("heartbeat timezone = %q, want the existing setting UTC", job.Timezone)
	}
	setting, ok, err := st.Orch().GetSetting("schedule.timezone")
	if err != nil || !ok || setting != "UTC" {
		t.Errorf("schedule.timezone = %q, %v, %v; want UTC", setting, ok, err)
	}
}

func TestCreateDuplicateName(t *testing.T) {
	_, first := setup(t)
	second := filepath.Join(t.TempDir(), "other")
	cfg := createTestConfig(t, `{}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: first, Config: cfg}); err != nil {
		t.Fatalf("Create(first) error: %v", err)
	}
	_, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: second, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "already registered at") {
		t.Fatalf("Create(duplicate name) error = %v", err)
	}
}

func TestCreateDuplicateWorkspace(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "one", Workspace: ws, Config: cfg}); err != nil {
		t.Fatalf("Create(one) error: %v", err)
	}
	_, err := Create(context.Background(), CreateOptions{Name: "two", Workspace: ws, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "already registered as") {
		t.Fatalf("Create(duplicate workspace) error = %v", err)
	}
}

func TestCreateRejectsHomeWorkspace(t *testing.T) {
	home, _ := setup(t)
	cfg := createTestConfig(t, `{}`)
	_, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: home, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "ACOLYTE_HOME") {
		t.Fatalf("Create(home) error = %v", err)
	}
}

func TestCreateInvalidName(t *testing.T) {
	_, ws := setup(t)
	cfg := createTestConfig(t, `{}`)
	if _, err := Create(context.Background(), CreateOptions{Name: "bad/name", Workspace: ws, Config: cfg}); err == nil {
		t.Fatal("Create(invalid name) error = nil, want error")
	}
}

func TestCreateNoScheduling(t *testing.T) {
	_, ws := setup(t)
	result, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: createTestConfig(t, `{}`)})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if len(result.Jobs) != 0 {
		t.Errorf("jobs = %v, want none", result.Jobs)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if jobs, err := st.Orch().ListCronJobs(); err != nil || len(jobs) != 0 {
		t.Errorf("stored jobs = %v, %v; want none", jobs, err)
	}
	if _, ok, err := st.Orch().GetSetting("schedule.timezone"); err != nil || ok {
		t.Errorf("timezone setting = ok:%v err:%v, want absent", ok, err)
	}
}

func TestCreateRejectsLegacyWorkspace(t *testing.T) {
	_, ws := setup(t)
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "opencode.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	_, err := Create(context.Background(), CreateOptions{Name: "valet", Workspace: ws, Config: createTestConfig(t, `{}`)})
	if err == nil || !strings.Contains(err.Error(), "legacy opencode.json") {
		t.Fatalf("Create(legacy) error = %v", err)
	}
}
