package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeServicePassword(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".config", "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write service.json: %v", err)
		}
	}
}

func TestLoadServicePassword(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"valid", `{"password":"secret"}`, "secret", false},
		{"missing file", "", "", true},
		{"empty password", `{"password":""}`, "", true},
		{"malformed", `not json`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeServicePassword(t, tt.content)

			got, err := LoadServicePassword()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadServicePassword: %v", err)
			}
			if got != tt.want {
				t.Errorf("password = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceURL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		out  string // status output; empty = status command fails
		want string
	}{
		{"status stopped falls back", "", "", DefaultServiceURL},
		{"loopback from status", "", "http://127.0.0.1:49374", "http://127.0.0.1:49374"},
		{"loopback extracted from labeled line", "", "status: running\nurl: http://127.0.0.1:12345\n", "http://127.0.0.1:12345"},
		{"plain http non-loopback never trusted", "", "url: http://example.com:8080", DefaultServiceURL},
		{"https from free text never trusted", "", "url: https://opencode.example.com:443", DefaultServiceURL},
		{"unrelated https before remote service url, no loopback", "",
			"docs: https://opencode.ai/docs\nservice: https://remote.example.com:8443", DefaultServiceURL},
		{"loopback preferred over earlier https", "",
			"docs: https://opencode.ai/docs\nurl: http://127.0.0.1:49374", "http://127.0.0.1:49374"},
		{"loopback preferred even with trailing punctuation", "",
			"see https://opencode.ai/docs. url: http://127.0.0.1:49374.", "http://127.0.0.1:49374"},
		{"trailing quote trimmed", "", `url: "http://127.0.0.1:49374"`, "http://127.0.0.1:49374"},
		{"configured remote endpoint allowlisted", "https://remote.example.com:8443",
			"docs: https://opencode.ai/docs", "https://remote.example.com:8443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(serviceURLEnv, tt.env)
			orig := serviceStatusCmd
			t.Cleanup(func() { serviceStatusCmd = orig })
			serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
				if tt.out == "" {
					return nil, fmt.Errorf("service stopped")
				}
				return []byte(tt.out), nil
			}

			got, err := ServiceURL(context.Background())
			if err != nil {
				t.Fatalf("ServiceURL: %v", err)
			}
			if got != tt.want {
				t.Errorf("ServiceURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceURLRejectsUntrustedConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{"plain http non-loopback", "http://example.com:8080"},
		{"unparseable", "://bad"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(serviceURLEnv, tt.env)

			if _, err := ServiceURL(context.Background()); err == nil {
				t.Fatal("expected an error for untrusted configuration")
			}
		})
	}
}

func TestEnsureAdoptsHealthyService(t *testing.T) {
	writeServicePassword(t, `{"password":"secret"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/info" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "opencode" || pass != "secret" {
			t.Errorf("bad auth: %q %q %v", user, pass, ok)
		}
		fmt.Fprint(w, `{"version":"2.2.5"}`)
	}))
	t.Cleanup(srv.Close)

	origStatus := serviceStatusCmd
	origStart := serviceStartCmd
	t.Cleanup(func() {
		serviceStatusCmd = origStatus
		serviceStartCmd = origStart
	})
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return []byte(srv.URL + "\n"), nil
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		t.Error("start must not be called for a healthy service")
		return nil, nil
	}

	client, err := Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if client.baseURL != srv.URL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, srv.URL)
	}
}

func TestEnsureStartsService(t *testing.T) {
	writeServicePassword(t, `{"password":"secret"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"2.2.5"}`)
	}))
	t.Cleanup(srv.Close)

	origStatus := serviceStatusCmd
	origStart := serviceStartCmd
	origTimeout := serviceStartTimeout
	origPoll := servicePollInterval
	t.Cleanup(func() {
		serviceStatusCmd = origStatus
		serviceStartCmd = origStart
		serviceStartTimeout = origTimeout
		servicePollInterval = origPoll
	})
	serviceStartTimeout = 5 * time.Second
	servicePollInterval = time.Millisecond

	healthy := false
	starts := 0
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		if !healthy {
			// Stopped: report a URL that refuses connections.
			return []byte("http://127.0.0.1:1\n"), nil
		}
		return []byte(srv.URL + "\n"), nil
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		starts++
		healthy = true
		return []byte("started"), nil
	}

	client, err := Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if starts != 1 {
		t.Errorf("start calls = %d, want 1", starts)
	}
	if client.baseURL != srv.URL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, srv.URL)
	}
}

func TestEnsureAuthFailFast(t *testing.T) {
	writeServicePassword(t, `{"password":"wrong"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"_tag":"UnauthorizedError","message":"bad password"}`)
	}))
	t.Cleanup(srv.Close)

	origStatus := serviceStatusCmd
	origStart := serviceStartCmd
	t.Cleanup(func() {
		serviceStatusCmd = origStatus
		serviceStartCmd = origStart
	})
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return []byte(srv.URL + "\n"), nil
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		t.Error("start must not be called when the service rejects credentials")
		return nil, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := Ensure(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an auth error")
		}
		if !strings.Contains(err.Error(), "rejected credentials") {
			t.Errorf("error %q does not mention rejected credentials", err.Error())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not fail fast on an auth error")
	}
}

func TestEnsureProbeTimeout(t *testing.T) {
	writeServicePassword(t, `{"password":"secret"}`)

	// A listener that accepts connections but never responds (wedged daemon).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	origStatus := serviceStatusCmd
	origStart := serviceStartCmd
	origTimeout := serviceStartTimeout
	origPoll := servicePollInterval
	origProbe := serviceProbeTimeout
	t.Cleanup(func() {
		serviceStatusCmd = origStatus
		serviceStartCmd = origStart
		serviceStartTimeout = origTimeout
		servicePollInterval = origPoll
		serviceProbeTimeout = origProbe
	})
	serviceProbeTimeout = 50 * time.Millisecond
	serviceStartTimeout = 200 * time.Millisecond
	servicePollInterval = time.Millisecond

	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return []byte(srv.URL + "\n"), nil
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		return []byte("started"), nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := Ensure(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from a wedged service")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure hung on a wedged health endpoint")
	}
}

func TestEnsureFailsWhenServiceNeverHealthy(t *testing.T) {
	writeServicePassword(t, `{"password":"secret"}`)

	origStatus := serviceStatusCmd
	origStart := serviceStartCmd
	origTimeout := serviceStartTimeout
	origPoll := servicePollInterval
	t.Cleanup(func() {
		serviceStatusCmd = origStatus
		serviceStartCmd = origStart
		serviceStartTimeout = origTimeout
		servicePollInterval = origPoll
	})
	serviceStartTimeout = 50 * time.Millisecond
	servicePollInterval = time.Millisecond

	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		// Always a dead URL: connection refused.
		return []byte("http://127.0.0.1:1\n"), nil
	}
	serviceStartCmd = func(ctx context.Context) ([]byte, error) {
		return []byte("started"), nil
	}

	if _, err := Ensure(context.Background()); err == nil {
		t.Fatal("expected error when service never becomes healthy")
	}
}
