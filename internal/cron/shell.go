package cron

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/secrets"
	"github.com/martins6/acolyte/internal/store/orch"
)

// limitedBuffer retains at most limit bytes while still draining the writer.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		take := len(p)
		if take > remaining {
			take = remaining
			b.truncated = true
		}
		_, _ = b.buf.Write(p[:take])
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.buf.String()
	if b.truncated {
		text += "\n[output truncated]"
	}
	return text
}

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

	output := &limitedBuffer{limit: maxOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	if err != nil {
		return output.String(), fmt.Errorf("cron: shell job %s: %w", job.Title, err)
	}
	return output.String(), nil
}
