package traceimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// manifest mirrors the subset of the export manifest that an import needs.
// It is parsed independently from traceexport so the two packages can evolve
// separately; unknown fields from future exports are ignored.
type manifest struct {
	Format     string            `json:"format"`
	Agent      manifestAgent     `json:"agent"`
	ExportedAt time.Time         `json:"exported_at"`
	Sessions   []manifestSession `json:"sessions"`
	Counts     manifestCounts    `json:"counts"`
}

type manifestAgent struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

type manifestSession struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Messages   int    `json:"messages"`
	Markdown   string `json:"markdown"`
	NativeJSON string `json:"native_json,omitempty"`
}

type manifestCounts struct {
	Sessions     int `json:"sessions"`
	Messages     int `json:"messages"`
	Logs         int `json:"logs"`
	PersonaFiles int `json:"persona_files"`
}

const manifestFormatPrefix = "acolyte-memory-trace"

func parseManifest(raw []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("traceimport: parse manifest.json: %w", err)
	}
	if !strings.HasPrefix(m.Format, manifestFormatPrefix) {
		return nil, fmt.Errorf("traceimport: unsupported trace format %q (want %s/...)", m.Format, manifestFormatPrefix)
	}
	return &m, nil
}
