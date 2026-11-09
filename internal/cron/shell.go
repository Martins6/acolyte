package cron

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/secrets"
	"github.com/martins6/acolyte/internal/store/orch"
)

func (s *Service) runShell(ctx context.Context, agent registry.Agent, job orch.CronJob, meta jobMeta) (string, error) {
	shell := strings.TrimSpace(s.shell)
	if shell == "" {
		return "", fmt.Errorf("cron: $SHELL is not set")
	}
	script := strings.TrimSpace(job.ShellPath)
	if script == "" {
		return "", fmt.Errorf("cron: shell path is empty")
	}
	if !filepath.IsAbs(script) {
		script = filepath.Join(agent.Workspace, script)
	}
	info, err := os.Stat(script)
	if err != nil {
		return "", fmt.Errorf("cron: stat shell script %s: %w", script, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("cron: shell path %s is a directory", script)
	}

	timeout := time.Duration(meta.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, script)
	cmd.Dir = agent.Workspace
	loaded, err := secrets.Load(agent.Workspace)
	if err != nil {
		return "", err
	}
	cmd.Env = loaded.Environ()

	output, err := cmd.CombinedOutput()
	text := string(output)
	if len(text) > maxOutputBytes {
		text = text[:maxOutputBytes] + "\n[output truncated]"
	}
	if err != nil {
		return text, fmt.Errorf("cron: shell job %s: %w", job.Title, err)
	}
	return text, nil
}
