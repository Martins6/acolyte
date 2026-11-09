package cron

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

type fakeSink struct {
	notified []string
	inboxed  []string
}

func (f *fakeSink) NotifyUser(_ context.Context, _ registry.Agent, text string) error {
	f.notified = append(f.notified, text)
	return nil
}

func (f *fakeSink) Inbox(_ context.Context, _ registry.Agent, _ string, text string, _ map[string]any) (string, error) {
	f.inboxed = append(f.inboxed, text)
	return text, nil
}

type fakeTurn struct {
	calls    int
	response string
	events   []opencode.SyntheticEvent
}

func (f *fakeTurn) RunSyntheticTurn(_ context.Context, sessionID string, ev opencode.SyntheticEvent, _ opencode.TurnConfig) (*opencode.TurnResult, error) {
	f.calls++
	f.events = append(f.events, ev)
	return &opencode.TurnResult{SessionID: "ses_fake", ResponseText: f.response}, nil
}

type fakeRoutine struct {
	name string
	out  string
}

func (f fakeRoutine) Name() string        { return f.name }
func (f fakeRoutine) Owner() string       { return "test" }
func (f fakeRoutine) Description() string { return "fake routine" }
func (f fakeRoutine) Args() []routine.ArgSpec {
	return nil
}
func (f fakeRoutine) Run(context.Context, routine.Env, map[string]any) (string, error) {
	return f.out, nil
}

func setupCron(t *testing.T) (*registry.Registry, string, time.Time) {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	ws := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reg, err := registry.OpenDefault()
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Register(registry.Agent{Name: "valet", Workspace: ws}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg, ws, time.Now().UTC()
}

func addJob(t *testing.T, ws string, job orch.CronJob) {
	t.Helper()
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Orch().AddCronJob(job); err != nil {
		t.Fatalf("add job: %v", err)
	}
}

func startTestService(t *testing.T, reg *registry.Registry, routines *routine.Registry, sink Sink, turn TurnRunner, shell string, now time.Time) *Service {
	t.Helper()
	service, err := Start(context.Background(), Options{
		Registry:        reg,
		Routines:        routines,
		Sink:            sink,
		OpenCode:        turn,
		Shell:           shell,
		Interval:        time.Hour,
		RefreshInterval: time.Hour,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return service
}

func TestShellJobUsesConfiguredShell(t *testing.T) {
	reg, ws, now := setupCron(t)
	dir := t.TempDir()
	shellUsed := filepath.Join(dir, "shell-used")
	marker := filepath.Join(dir, "marker")
	wrapper := filepath.Join(dir, "shell.sh")
	jobScript := filepath.Join(dir, "job.sh")

	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s' \"$0\" > "+shellUsed+"\nexec /bin/sh \"$1\"\n"), 0755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if err := os.WriteFile(jobScript, []byte("#!/bin/sh\necho ran > "+marker+"\n"), 0644); err != nil {
		t.Fatalf("write job: %v", err)
	}

	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "shell", Kind: "shell", Spec: "@every 1h", ShellPath: jobScript,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, wrapper, now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "ran" {
		t.Errorf("marker = %q, want ran", raw)
	}
	raw, err = os.ReadFile(shellUsed)
	if err != nil {
		t.Fatalf("read shell marker: %v", err)
	}
	if string(raw) != wrapper {
		t.Errorf("shell used = %q, want %q", raw, wrapper)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("shell")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.LastRunAt == nil {
		t.Error("last_run_at was not set")
	}
	if job.NextRunAt == nil || !job.NextRunAt.After(now) {
		t.Errorf("next_run_at = %v, want future", job.NextRunAt)
	}
}

func TestShellOutputIsBounded(t *testing.T) {
	_, ws, _ := setupCron(t)
	script := filepath.Join(t.TempDir(), "noisy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' x\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	service := &Service{shell: "/bin/sh"}
	out, err := service.runShell(context.Background(), registry.Agent{Name: "valet", Workspace: ws}, orch.CronJob{
		Title: "noisy", ShellPath: script,
	}, jobMeta{TimeoutSeconds: 10})
	if err != nil {
		t.Fatalf("runShell() error: %v", err)
	}
	if len(out) > maxOutputBytes+len("\n[output truncated]") {
		t.Errorf("output length = %d, want bounded", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Error("output was not marked truncated")
	}
}

func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	_, ws, _ := setupCron(t)
	script := filepath.Join(t.TempDir(), "hang.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n(sleep 5; echo late) &\nsleep 5\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	service := &Service{shell: "/bin/sh"}
	start := time.Now()
	out, err := service.runShell(context.Background(), registry.Agent{Name: "valet", Workspace: ws}, orch.CronJob{
		Title: "hang", ShellPath: script,
	}, jobMeta{TimeoutSeconds: 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("runShell() error = nil, want timeout error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("runShell() took %s, process group was not killed", elapsed)
	}
	if strings.Contains(out, "late") {
		t.Errorf("output = %q, descendant survived the timeout", out)
	}
}

func TestShellFailureRoutesOutput(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boom\nexit 3\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "fail", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_failure":"notify-user"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.NewRegistry(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || !strings.Contains(sink.notified[0], "boom") {
		t.Errorf("notified = %v, want failure output", sink.notified)
	}
}

func TestRefreshRetiresStoreWithoutClosingRunningJob(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.25\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "slow", Kind: "shell", Spec: "@every 1h", ShellPath: script,
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	time.Sleep(20 * time.Millisecond)
	if _, err := reg.Delete("valet"); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	service.refresh()
	service.Wait()

	// The retired store must be closed as soon as its last job releases it.
	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("retired store was not released: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	service.Stop()

	st, err = store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("slow")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.LastRunAt == nil {
		t.Error("running job lost its store before completion")
	}
}

func TestDispatchIsBoundedByConcurrency(t *testing.T) {
	reg, ws, now := setupCron(t)
	script := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.2\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	past := now.Add(-time.Minute)
	for _, title := range []string{"one", "two"} {
		addJob(t, ws, orch.CronJob{
			Title: title, Kind: "shell", Spec: "@every 1h", ShellPath: script,
			AgentTarget: "valet", Enabled: true, NextRunAt: &past,
			Meta: `{"on_success":"log"}`,
		})
	}

	service, err := Start(context.Background(), Options{
		Registry:        reg,
		Routines:        routine.NewRegistry(),
		Sink:            &fakeSink{},
		Shell:           "/bin/sh",
		MaxConcurrency:  1,
		Interval:        time.Hour,
		RefreshInterval: time.Hour,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	jobs, err := st.Orch().ListCronJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	ran := 0
	for _, job := range jobs {
		if job.LastRunAt != nil {
			ran++
		}
	}
	if ran != 1 {
		t.Errorf("ran jobs = %d, want 1 with MaxConcurrency=1", ran)
	}
}

func TestPromptJobRoutesOutputAndStoresSession(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "prompt", Kind: "prompt", Spec: "@every 1h", Prompt: "hello",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	sink := &fakeSink{}
	turn := &fakeTurn{response: "reply"}
	service := startTestService(t, reg, routine.NewRegistry(), sink, turn, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if turn.calls != 1 {
		t.Fatalf("turn calls = %d, want 1", turn.calls)
	}
	if len(turn.events) != 1 || turn.events[0].Text != "hello" {
		t.Errorf("events = %+v", turn.events)
	}
	if len(sink.notified) != 1 || sink.notified[0] != "reply" {
		t.Errorf("notified = %v, want reply", sink.notified)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("prompt")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	session, ok, err := st.Orch().GetSessionByThread("cron:" + job.ID)
	if err != nil || !ok {
		t.Fatalf("session = %+v, %v, %v", session, ok, err)
	}
	if session.OpenCodeSessionID != "ses_fake" {
		t.Errorf("session id = %q, want ses_fake", session.OpenCodeSessionID)
	}
}

func TestRoutineJobRoutesOutput(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "routine", Kind: "routine", Spec: "@every 1h", Routine: "fake",
		RoutineArgs: `{}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	routines := routine.NewRegistry()
	if err := routines.Register(fakeRoutine{name: "fake", out: "routine-output"}); err != nil {
		t.Fatalf("register routine: %v", err)
	}
	sink := &fakeSink{}
	service := startTestService(t, reg, routines, sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 1 || sink.notified[0] != "routine-output" {
		t.Errorf("notified = %v, want routine-output", sink.notified)
	}
}

func TestPendingRoutineDoesNotNotify(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "dream", Kind: "routine", Spec: "@every 72h", Routine: "dream",
		RoutineArgs: `{"days":3}`, AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"notify-user"}`,
	})

	sink := &fakeSink{}
	service := startTestService(t, reg, routine.Default(), sink, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	if len(sink.notified) != 0 {
		t.Errorf("notified = %v, want empty", sink.notified)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	var message string
	if err := st.DB().QueryRow(`SELECT message FROM orch_logs ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(message, "pending Phase 4") {
		t.Errorf("log message = %q, want pending Phase 4", message)
	}
}

func TestInvalidSpecDisablesJob(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "bad", Kind: "shell", Spec: "not a cron", ShellPath: "/bin/true",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
	})

	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "/bin/sh", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("bad")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Enabled {
		t.Error("job enabled = true, want disabled after invalid spec")
	}
}

func TestShellWithoutSHELLFails(t *testing.T) {
	reg, ws, now := setupCron(t)
	past := now.Add(-time.Minute)
	addJob(t, ws, orch.CronJob{
		Title: "shell", Kind: "shell", Spec: "@every 1h", ShellPath: "/bin/true",
		AgentTarget: "valet", Enabled: true, NextRunAt: &past,
		Meta: `{"on_success":"log"}`,
	})

	t.Setenv("SHELL", "")
	service := startTestService(t, reg, routine.NewRegistry(), &fakeSink{}, nil, "", now)
	service.Tick(context.Background())
	service.Wait()
	service.Stop()

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	var message string
	if err := st.DB().QueryRow(`SELECT message FROM orch_logs ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(message, "$SHELL is not set") {
		t.Errorf("log message = %q, want $SHELL error", message)
	}
}
