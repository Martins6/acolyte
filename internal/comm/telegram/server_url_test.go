package telegram

import "testing"

func TestResolveServerURL(t *testing.T) {
	t.Setenv(ServerURLEnv, "")

	cases := []struct {
		name     string
		explicit string
		want     string
		wantErr  bool
	}{
		{name: "default", explicit: "", want: ""},
		{name: "loopback http", explicit: "http://127.0.0.1:8081/", want: "http://127.0.0.1:8081"},
		{name: "localhost http", explicit: "http://localhost:8081", want: "http://localhost:8081"},
		{name: "https", explicit: "https://telegram.example/", want: "https://telegram.example"},
		{name: "remote http", explicit: "http://telegram.example", wantErr: true},
		{name: "unsupported scheme", explicit: "ftp://127.0.0.1:21", wantErr: true},
		{name: "missing host", explicit: "http:///bot", wantErr: true},
		{name: "garbage", explicit: "not a url", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveServerURL(tc.explicit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveServerURL(%q) error = nil, want error", tc.explicit)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveServerURL(%q) error = %v", tc.explicit, err)
			}
			if got != tc.want {
				t.Errorf("resolveServerURL(%q) = %q, want %q", tc.explicit, got, tc.want)
			}
		})
	}
}

func TestResolveServerURLFromEnv(t *testing.T) {
	t.Setenv(ServerURLEnv, "http://127.0.0.1:9999")
	got, err := resolveServerURL("")
	if err != nil {
		t.Fatalf("resolveServerURL() error = %v", err)
	}
	if got != "http://127.0.0.1:9999" {
		t.Errorf("resolveServerURL() = %q, want env override", got)
	}
}
