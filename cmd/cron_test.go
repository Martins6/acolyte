package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store"
)

func setupCronCLI(t *testing.T) string {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	ws := filepath.Join(t.TempDir(), "agent")
	cfg, err := agent.ParseConfig(`{}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{
		Name:      "valet",
		Workspace: ws,
		Config:    cfg,
	}); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return ws
}

func fixedCronNow(t *testing.T) time.Time {
	t.Helper()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	old := cronNow
	cronNow = func() time.Time { return now }
	t.Cleanup(func() { cronNow = old })
	return now
}

func TestRunCronCreateShell(t *testing.T) {
	ws := setupCronCLI(t)
	fixedCronNow(t)
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrue\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	var out bytes.Buffer
	if err := runCronCreate(&out, cronCreateOptions{
		Title:     "hello",
		Datetime:  "@every 1h",
		Shell:     script,
		AgentName: "valet",
	}); err != nil {
		t.Fatalf("runCronCreate() error: %v", err)
	}
	if !strings.Contains(out.String(), `Created cron job "hello"`) {
		t.Errorf("output = %q", out.String())
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	job, err := st.Orch().GetCronJobByTitle("hello")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Kind != "shell" || job.ShellPath != script || job.AgentTarget != "valet" {
		t.Errorf("job = %+v", job)
	}
	if job.NextRunAt == nil {
		t.Error("next_run_at is nil")
	}
	var meta cronJobMeta
	if err := json.Unmarshal([]byte(job.Meta), &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta.OnSuccess != "inbox" || meta.OnFailure != "notify-user" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestRunCronCreatePromptAndRoutine(t *testing.T) {
	ws := setupCronCLI(t)
	fixedCronNow(t)

	if err := runCronCreate(&bytes.Buffer{}, cronCreateOptions{
		Title:     "prompt",
		Datetime:  "0 9 * * *",
		Prompt:    "say hi",
		AgentName: "valet",
	}); err != nil {
		t.Fatalf("create prompt: %v", err)
	}
	if err := runCronCreate(&bytes.Buffer{}, cronCreateOptions{
		Title:       "dream",
		Datetime:    "@every 72h",
		Routine:     "dream",
		RoutineArgs: `{"days":5}`,
		AgentName:   "valet",
	}); err != nil {
		t.Fatalf("create routine: %v", err)
	}

	st, err := store.Open(ws)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	routineJob, err := st.Orch().GetCronJobByTitle("dream")
	if err != nil {
		t.Fatalf("get routine: %v", err)
	}
	if routineJob.Kind != "routine" || routineJob.Routine != "dream" || !strings.Contains(routineJob.RoutineArgs, "5") {
		t.Errorf("routine job = %+v", routineJob)
	}
}

func TestRunCronCreateValidation(t *testing.T) {
	setupCronCLI(t)
	fixedCronNow(t)
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrue\n"), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cases := []cronCreateOptions{
		{Title: "x", Datetime: "@every 1h", AgentName: "valet"},
		{Title: "x", Datetime: "@every 1h", Prompt: "p", Shell: script, AgentName: "valet"},
		{Title: "x", Datetime: "not a cron", Prompt: "p", AgentName: "valet"},
		{Title: "x", Datetime: "@every 1h", Routine: "missing", AgentName: "valet"},
		{Title: "x", Datetime: "@every 1h", Routine: "dream", RoutineArgs: `{"weeks":1}`, AgentName: "valet"},
		{Title: "x", Datetime: "@every 1h", Prompt: "p", AgentName: "valet", Workspace: "/tmp/other"},
		{Title: "x", Datetime: "@every 1h", Prompt: "p", AgentName: "valet", OnSuccess: "bad"},
	}
	for _, opts := range cases {
		if err := runCronCreate(&bytes.Buffer{}, opts); err == nil {
			t.Errorf("runCronCreate(%+v) error = nil, want error", opts)
		}
	}

	if err := runCronCreate(&bytes.Buffer{}, cronCreateOptions{
		Title: "dup", Datetime: "@every 1h", Prompt: "p", AgentName: "valet",
	}); err != nil {
		t.Fatalf("create dup: %v", err)
	}
	if err := runCronCreate(&bytes.Buffer{}, cronCreateOptions{
		Title: "dup", Datetime: "@every 2h", Prompt: "p", AgentName: "valet",
	}); err == nil {
		t.Fatal("duplicate title error = nil, want error")
	}
}

func TestRunCronListAndViewAndRemove(t *testing.T) {
	setupCronCLI(t)
	fixedCronNow(t)
	if err := runCronCreate(&bytes.Buffer{}, cronCreateOptions{
		Title: "hello", Datetime: "@every 1h", Prompt: "say hi", AgentName: "valet",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	var list bytes.Buffer
	var listErr bytes.Buffer
	if err := runCronList(&list, &listErr); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"| TITLE |", "hello", "valet", "prompt:say hi"} {
		if !strings.Contains(list.String(), want) {
			t.Errorf("list missing %q:\n%s", want, list.String())
		}
	}

	var view bytes.Buffer
	if err := runCronView(&view, "hello", "valet", ""); err != nil {
		t.Fatalf("view: %v", err)
	}
	if !strings.Contains(view.String(), `"title": "hello"`) {
		t.Errorf("view = %s", view.String())
	}

	var remove bytes.Buffer
	if err := runCronRemove(&remove, "hello", "valet", ""); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := runCronView(&view, "hello", "valet", ""); err == nil {
		t.Fatal("view after remove error = nil, want error")
	}
}

func TestCronAmbiguousTitleRequiresTarget(t *testing.T) {
	t.Setenv(paths.HomeEnv, t.TempDir())
	for _, name := range []string{"one", "two"} {
		ws := filepath.Join(t.TempDir(), name)
		cfg, err := agent.ParseConfig(`{"memory-learn":{"args":{"days":3}},"schedule":{"timezone":"UTC"}}`)
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		if _, err := agent.Create(context.Background(), agent.CreateOptions{
			Name: name, Workspace: ws, Config: cfg,
		}); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	if err := runCronView(&bytes.Buffer{}, "dream", "", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("view ambiguous error = %v, want ambiguous", err)
	}
	if err := runCronView(&bytes.Buffer{}, "dream", "one", ""); err != nil {
		t.Fatalf("view targeted: %v", err)
	}
}

func TestRunCronRoutineList(t *testing.T) {
	var out bytes.Buffer
	if err := runCronRoutineList(&out); err != nil {
		t.Fatalf("routine-list: %v", err)
	}
	for _, want := range []string{`"name": "dream"`, `"name": "heartbeat"`, `"days"`, `"interval"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("routine-list missing %q:\n%s", want, out.String())
		}
	}
}

func TestCronRemoveMissing(t *testing.T) {
	setupCronCLI(t)
	if err := runCronRemove(&bytes.Buffer{}, "missing", "valet", ""); err == nil {
		t.Fatal("remove missing error = nil, want error")
	}
}
