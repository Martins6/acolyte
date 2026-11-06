package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Discovery and lifecycle for the OpenCode managed background service
// (the `opencode serve --service` daemon), per SPEC §5.1. Mirrors the
// Service.ensure semantics of @opencode/client: discover the running service,
// health-probe /api/info, and start it when it is not reachable.

const (
	// DefaultServiceURL is the documented default of the OpenCode V2
	// background service.
	DefaultServiceURL = "http://127.0.0.1:49374"
)

// Vars (not consts) so tests can shorten them.
var (
	// serviceStartTimeout bounds `opencode service start` and the wait for
	// the service to become healthy.
	serviceStartTimeout = 60 * time.Second

	// servicePollInterval is the health-probe interval while starting.
	servicePollInterval = 500 * time.Millisecond
)

// Command seams, replaced in tests.
var (
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "opencode", "service", "status").Output()
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "opencode", "service", "start").CombinedOutput()
	}
)

// LoadServicePassword reads the HTTP Basic auth password (user "opencode")
// the background service requires, from ~/.config/opencode/service.json.
func LoadServicePassword() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("opencode: home dir: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "service.json"))
	if err != nil {
		return "", fmt.Errorf("opencode: read service.json: %w", err)
	}
	var cfg struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("opencode: parse service.json: %w", err)
	}
	if cfg.Password == "" {
		return "", fmt.Errorf("opencode: service.json has no password")
	}
	return cfg.Password, nil
}

// ServiceURL returns the URL reported by `opencode service status`, falling
// back to DefaultServiceURL when the service reports nothing (e.g. stopped).
func ServiceURL(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := serviceStatusCmd(ctx)
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
				return line
			}
		}
	}
	return DefaultServiceURL
}

// Ensure discovers the OpenCode background service, starts it when it is not
// reachable, and returns an authenticated client. It re-discovers the service
// URL after starting (the daemon registers its port on startup).
func Ensure(ctx context.Context) (*Client, error) {
	password, err := LoadServicePassword()
	if err != nil {
		return nil, err
	}

	c := NewClient(ServiceURL(ctx), password)
	if err := c.HealthCheck(ctx); err == nil {
		return c, nil
	}

	if err := startService(ctx); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(serviceStartTimeout)
	for {
		c = NewClient(ServiceURL(ctx), password)
		err := c.HealthCheck(ctx)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("opencode: background service did not become healthy: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(servicePollInterval):
		}
	}
}

// startService starts the managed background service via the OpenCode CLI.
func startService(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, serviceStartTimeout)
	defer cancel()

	out, err := serviceStartCmd(ctx)
	if err != nil {
		return fmt.Errorf("opencode: start background service: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
