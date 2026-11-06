package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestServiceURLFallback(t *testing.T) {
	orig := serviceStatusCmd
	t.Cleanup(func() { serviceStatusCmd = orig })
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return nil, fmt.Errorf("service stopped")
	}

	if got := ServiceURL(context.Background()); got != DefaultServiceURL {
		t.Errorf("ServiceURL = %q, want %q", got, DefaultServiceURL)
	}
}

func TestServiceURLFromStatus(t *testing.T) {
	orig := serviceStatusCmd
	t.Cleanup(func() { serviceStatusCmd = orig })
	serviceStatusCmd = func(ctx context.Context) ([]byte, error) {
		return []byte("http://127.0.0.1:12345\n"), nil
	}

	if got := ServiceURL(context.Background()); got != "http://127.0.0.1:12345" {
		t.Errorf("ServiceURL = %q, want http://127.0.0.1:12345", got)
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
