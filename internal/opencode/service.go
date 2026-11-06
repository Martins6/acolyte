package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	// serviceProbeTimeout bounds a single health probe so a wedged listener
	// cannot block Ensure (and its caller) indefinitely.
	serviceProbeTimeout = 5 * time.Second
)

// urlPattern finds candidate URLs inside `opencode service status` output,
// whatever the surrounding line format is.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

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

// serviceURLEnv is the explicit allowlist override for non-local services.
const serviceURLEnv = "ACOLYTE_OPENCODE_URL"

// ServiceURL resolves the OpenCode service URL: an explicitly configured
// ACOLYTE_OPENCODE_URL wins, then the first loopback URL reported by
// `opencode service status`, then DefaultServiceURL. Non-loopback URLs are
// never trusted from free text (we would send the Basic-auth password and
// turn content there): a remote endpoint must be configured explicitly.
func ServiceURL(ctx context.Context) (string, error) {
	if raw := strings.TrimSpace(os.Getenv(serviceURLEnv)); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
			return "", fmt.Errorf("opencode: invalid %s %q: only https or loopback http is allowed", serviceURLEnv, raw)
		}
		return strings.TrimRight(raw, "/"), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := serviceStatusCmd(ctx)
	if err == nil {
		if u := pickServiceURL(urlPattern.FindAllString(string(out), -1)); u != "" {
			return u, nil
		}
	}
	return DefaultServiceURL, nil
}

// pickServiceURL returns the first loopback URL among the candidates found in
// the status output, with trailing punctuation trimmed. Returns "" when no
// loopback candidate exists.
func pickServiceURL(candidates []string) string {
	for _, raw := range candidates {
		raw = strings.TrimRight(raw, ".,;:)'\"")
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if isLoopbackHost(u.Hostname()) {
			return raw
		}
	}
	return ""
}

// isLoopbackHost reports whether host is a loopback address.
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// Ensure discovers the OpenCode background service, starts it when it is not
// reachable, and returns an authenticated client. It re-discovers the service
// URL after starting (the daemon registers its port on startup).
func Ensure(ctx context.Context) (*Client, error) {
	password, err := LoadServicePassword()
	if err != nil {
		return nil, err
	}

	baseURL, err := ServiceURL(ctx)
	if err != nil {
		return nil, err
	}

	c := NewClient(baseURL, password)
	err = probeHealth(ctx, c)
	if err == nil {
		return c, nil
	}
	// A reachable service that rejects our credentials will not become
	// healthy by starting another one: fail fast with the auth error.
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		return nil, fmt.Errorf("opencode: background service rejected credentials (check ~/.config/opencode/service.json): %w", err)
	}

	if err := startService(ctx); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(serviceStartTimeout)
	for {
		baseURL, err := ServiceURL(ctx)
		if err != nil {
			return nil, err
		}
		c := NewClient(baseURL, password)
		err = probeHealth(ctx, c)
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

// probeHealth runs one health probe under a short deadline so a wedged
// listener cannot block the caller.
func probeHealth(ctx context.Context, c *Client) error {
	ctx, cancel := context.WithTimeout(ctx, serviceProbeTimeout)
	defer cancel()
	return c.HealthCheck(ctx)
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
