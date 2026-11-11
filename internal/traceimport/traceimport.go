// Package traceimport imports a portable memory trace zip (produced by
// `acolyte memory export`) into an agent workspace. The trace mode safely
// extracts the bundle under downloads/memory-traces/ and maintains the
// managed .acolyte/MEMORY-TRACES.md index; the sessions mode replays the
// OpenCode-native session payloads through POST /api/experimental/session/import
// and maps the imported sessions to memory:<trace-id>:<source-session-id>
// threads in the agent store.
package traceimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

// Mode selects what an import run does.
type Mode string

const (
	// ModeTrace extracts the trace bundle and updates the trace index.
	ModeTrace Mode = "trace"
	// ModeSessions imports the OpenCode-native session payloads.
	ModeSessions Mode = "sessions"
	// ModeAll runs the trace and sessions modes.
	ModeAll Mode = "all"
)

// ParseMode validates a --mode flag value.
func ParseMode(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ModeTrace:
		return ModeTrace, nil
	case ModeSessions:
		return ModeSessions, nil
	case ModeAll:
		return ModeAll, nil
	default:
		return "", fmt.Errorf("traceimport: invalid mode %q (want trace, sessions, or all)", raw)
	}
}

func (m Mode) includes(other Mode) bool {
	if m == ModeAll {
		return true
	}
	return m == other
}

// OpenCode is the service surface used by the sessions mode. It is
// intentionally narrow so tests can fake the import endpoint.
type OpenCode interface {
	ImportSession(ctx context.Context, raw json.RawMessage, workspace string) (*opencode.SessionInfo, error)
}

// Request is one `acolyte memory import` run.
type Request struct {
	Agent    registry.Agent
	Store    *store.Store // required for the sessions mode (thread mappings)
	OpenCode OpenCode     // required for the sessions mode outside dry-run

	ZipPath string
	Mode    Mode
	DryRun  bool
	Force   bool

	Now time.Time
}

// SessionResult reports one imported session.
type SessionResult struct {
	SourceSessionID string `json:"source_session_id"`
	SessionID       string `json:"session_id"`
	Thread          string `json:"thread"`
	Title           string `json:"title,omitempty"`
}

// Result summarizes an import run.
type Result struct {
	TraceID      string          `json:"trace_id"`
	Mode         string          `json:"mode"`
	DryRun       bool            `json:"dry_run"`
	SourceAgent  string          `json:"source_agent,omitempty"`
	ExportedAt   time.Time       `json:"exported_at,omitempty"`
	TraceDir     string          `json:"trace_dir,omitempty"`
	Files        int             `json:"files,omitempty"`
	IndexUpdated bool            `json:"index_updated,omitempty"`
	IndexPath    string          `json:"index_path,omitempty"`
	Sessions     []SessionResult `json:"sessions,omitempty"`
	Skipped      []string        `json:"skipped,omitempty"`
	Actions      []string        `json:"actions,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
}

// Import runs one memory trace import.
func Import(ctx context.Context, req Request) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	zipPath := strings.TrimSpace(req.ZipPath)
	if zipPath == "" {
		return nil, fmt.Errorf("traceimport: zip path is required")
	}
	if req.Agent.Workspace == "" {
		return nil, fmt.Errorf("traceimport: agent workspace is required")
	}
	mode := req.Mode
	if mode == "" {
		mode = ModeTrace
	}
	if mode != ModeTrace && mode != ModeSessions && mode != ModeAll {
		return nil, fmt.Errorf("traceimport: invalid mode %q (want trace, sessions, or all)", req.Mode)
	}
	if mode.includes(ModeSessions) && !req.DryRun {
		if req.Store == nil {
			return nil, fmt.Errorf("traceimport: store is required for session import")
		}
		if req.OpenCode == nil {
			return nil, fmt.Errorf("traceimport: OpenCode client is required for session import")
		}
	}

	traceID, err := traceIDFor(zipPath)
	if err != nil {
		return nil, err
	}
	bundle, err := openTrace(zipPath)
	if err != nil {
		return nil, err
	}
	defer bundle.Close()

	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	res := &Result{
		TraceID:     traceID,
		Mode:        string(mode),
		DryRun:      req.DryRun,
		SourceAgent: bundle.manifest.Agent.Name,
		ExportedAt:  bundle.manifest.ExportedAt,
	}
	if mode.includes(ModeSessions) && req.DryRun && req.Store == nil {
		// Dry-run without a store cannot know which threads are mapped; the
		// caller passes nil only when the agent has no store yet.
		res.Warnings = append(res.Warnings, "agent store not found; assuming no sessions are mapped")
	}

	workspace := req.Agent.Workspace
	if mode.includes(ModeTrace) {
		if err := runTrace(ctx, bundle, req, workspace, traceID, res); err != nil {
			return nil, err
		}
	}
	if mode.includes(ModeSessions) {
		if err := runSessions(ctx, bundle, req, workspace, traceID, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func runTrace(ctx context.Context, bundle *traceBundle, req Request, workspace, traceID string, res *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dest := filepath.Join(workspace, "downloads", "memory-traces", traceID)
	relDest := filepath.ToSlash(filepath.Join("downloads", "memory-traces", traceID))
	res.TraceDir = dest

	if req.DryRun {
		files, err := bundle.validate()
		if err != nil {
			return err
		}
		res.Files = files
		if err := checkDestination(dest, req.Force); err != nil {
			return err
		}
		res.Actions = append(res.Actions,
			fmt.Sprintf("extract %d files to %s", files, relDest),
			"update "+indexPath(workspace))
		return nil
	}

	files, err := extractTrace(workspace, bundle.reader, dest, req.Force)
	if err != nil {
		return err
	}
	res.Files = files
	updated, err := updateIndex(workspace, indexRow{
		TraceID:     traceID,
		SourceAgent: bundle.manifest.Agent.Name,
		ExportedAt:  bundle.manifest.ExportedAt,
		Sessions:    bundle.manifest.Counts.Sessions,
		Messages:    bundle.manifest.Counts.Messages,
		Folder:      relDest,
	})
	if err != nil {
		return err
	}
	res.IndexUpdated = updated
	res.IndexPath = indexPath(workspace)
	return nil
}

func runSessions(ctx context.Context, bundle *traceBundle, req Request, workspace, traceID string, res *Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plans, warnings, err := planSessions(bundle.manifest, traceID, req.Store, req.Force)
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, warnings...)

	// Preflight: stream every payload that would be imported to enforce the
	// per-file and aggregate size caps on actual decompressed bytes, then
	// decode them to verify ids — all before promising (dry-run) or issuing
	// any import request.
	var payloads []string
	for _, plan := range plans {
		if plan.skip {
			continue
		}
		payloads = append(payloads, plan.nativeJSON)
	}
	if err := bundle.requirePayloadBudget(payloads); err != nil {
		return err
	}
	if err := bundle.validatePayloads(plans); err != nil {
		return err
	}

	if req.DryRun {
		for _, plan := range plans {
			if plan.skip {
				res.Skipped = append(res.Skipped, plan.sourceID)
				res.Actions = append(res.Actions, fmt.Sprintf("skip session %s (already mapped to %s)", plan.sourceID, plan.mappedTo))
				continue
			}
			res.Actions = append(res.Actions, fmt.Sprintf("import session %s -> thread %s", plan.sourceID, plan.thread))
		}
		return nil
	}

	importer := &sessionImporter{
		client:    req.OpenCode,
		workspace: workspace,
		imported:  map[string]string{},
	}
	for _, plan := range plans {
		if plan.skip {
			res.Skipped = append(res.Skipped, plan.sourceID)
			continue
		}
		raw, err := bundle.readEntry(plan.nativeJSON)
		if err != nil {
			return err
		}
		result, err := importer.importOne(ctx, plan, raw)
		if err != nil {
			return err
		}
		if _, err := req.Store.Orch().UpsertSession(plan.thread, result.SessionID, req.Now); err != nil {
			return fmt.Errorf("traceimport: map session %s: %w", plan.sourceID, err)
		}
		res.Sessions = append(res.Sessions, *result)
	}
	return nil
}

// traceIDFor derives a stable trace id from the zip content: the same zip
// always maps to the same trace folder and thread keys, while a re-export
// produces a new id.
func traceIDFor(zipPath string) (string, error) {
	file, err := os.Open(zipPath)
	if err != nil {
		return "", fmt.Errorf("traceimport: open zip: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("traceimport: hash zip: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
