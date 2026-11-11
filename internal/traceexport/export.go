// Package traceexport builds a portable, self-contained memory trace for one
// agent: each OpenCode session as Markdown, the persona/skills files, the
// orchestrator audit log, and the communication routing state. The result is a
// zip that another agent (or a human) can read without Acolyte.
package traceexport

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/commtg"
	"github.com/martins6/acolyte/internal/store/orch"
)

// OpenCode is the service surface used by an export. It is intentionally
// narrower than the full client so tests and future transports can fake it.
type OpenCode interface {
	ListSessions(ctx context.Context) ([]opencode.SessionInfo, error)
	SessionMessages(ctx context.Context, sessionID string, maxPages int) ([]opencode.Message, bool, error)
}

// nativeExporter is implemented by the real client. Native session JSON is
// optional because the endpoint is experimental; Markdown remains the source
// of truth.
type nativeExporter interface {
	ExportSession(ctx context.Context, sessionID string) (json.RawMessage, error)
}

// sessionGetter lets the export recover a session projection that was not in
// ListSessions (for example, a mapped session from an older service view).
type sessionGetter interface {
	GetSession(ctx context.Context, sessionID string) (*opencode.SessionInfo, error)
}

// Request is one `acolyte memory export` run.
type Request struct {
	Agent    registry.Agent
	Store    *store.Store
	OpenCode OpenCode // nil exports store/persona data only

	OutPath   string
	SessionID string
	MaxPages  int
	Force     bool
	// Sanitize redacts common secret patterns from every written entry.
	// It is best-effort: raw traces may still contain secrets.
	Sanitize bool

	AcolyteVersion string
	Now            time.Time
}

// Result summarizes the written bundle.
type Result struct {
	Path         string   `json:"path"`
	Sessions     int      `json:"sessions"`
	Messages     int      `json:"messages"`
	Logs         int      `json:"logs"`
	PersonaFiles int      `json:"persona_files"`
	Warnings     []string `json:"warnings,omitempty"`
}

type manifest struct {
	Format         string            `json:"format"`
	Agent          agentManifest     `json:"agent"`
	ExportedAt     time.Time         `json:"exported_at"`
	AcolyteVersion string            `json:"acolyte_version,omitempty"`
	Sanitized      bool              `json:"sanitized"`
	ScopedSession  string            `json:"scoped_session,omitempty"`
	Sessions       []sessionManifest `json:"sessions"`
	Counts         counts            `json:"counts"`
	Warnings       []string          `json:"warnings,omitempty"`
}

type agentManifest struct {
	Name         string    `json:"name"`
	Workspace    string    `json:"workspace"`
	Description  string    `json:"description,omitempty"`
	Engine       string    `json:"engine,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`
	LastSeenAt   time.Time `json:"last_seen_at,omitempty"`
}

type sessionManifest struct {
	ID             string    `json:"id"`
	Thread         string    `json:"thread,omitempty"`
	Title          string    `json:"title,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	Messages       int       `json:"messages"`
	Truncated      bool      `json:"truncated,omitempty"`
	Markdown       string    `json:"markdown"`
	NativeJSON     string    `json:"native_json,omitempty"`
	MappingWarning string    `json:"mapping_warning,omitempty"`
}

type counts struct {
	Sessions     int `json:"sessions"`
	Messages     int `json:"messages"`
	Logs         int `json:"logs"`
	PersonaFiles int `json:"persona_files"`
	CommInbox    int `json:"comm_inbox"`
	CommOutbox   int `json:"comm_outbox"`
	CommMedia    int `json:"comm_media"`
	MemoryDreams int `json:"memory_dreams"`
}

type bundle struct {
	request   Request
	zip       *zip.Writer
	now       time.Time
	files     int
	warnings  []string
	sessions  []sessionManifest
	messages  int
	logs      int
	persona   int
	commIn    int
	commOut   int
	commMedia int
	dreams    int
}

// Export writes the zip at req.OutPath atomically and returns its summary.
func Export(ctx context.Context, req Request) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Store == nil {
		return nil, fmt.Errorf("traceexport: store is required")
	}
	outPath := strings.TrimSpace(req.OutPath)
	if outPath == "" {
		return nil, fmt.Errorf("traceexport: output path is required")
	}
	outPath, err := paths.Abs(outPath)
	if err != nil {
		return nil, err
	}
	if req.MaxPages <= 0 {
		req.MaxPages = 1000
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	req.Now = req.Now.UTC()

	if !req.Force {
		if _, err := os.Lstat(outPath); err == nil {
			return nil, fmt.Errorf("traceexport: %s already exists (use --force to overwrite)", outPath)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("traceexport: check output path: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return nil, fmt.Errorf("traceexport: create output directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".acolyte-memory-*.zip")
	if err != nil {
		return nil, fmt.Errorf("traceexport: create temporary zip: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("traceexport: protect temporary zip: %w", err)
	}

	b := &bundle{
		request:  req,
		zip:      zip.NewWriter(tmp),
		now:      req.Now,
		warnings: append([]string(nil), reqWarnings(req)...),
	}

	writeErr := b.writeBundle(ctx)
	closeErr := b.zip.Close()
	fileErr := tmp.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("traceexport: close zip: %w", closeErr)
	}
	if fileErr != nil {
		return nil, fmt.Errorf("traceexport: close zip file: %w", fileErr)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return nil, fmt.Errorf("traceexport: finalize zip: %w", err)
	}

	return &Result{
		Path:         outPath,
		Sessions:     len(b.sessions),
		Messages:     b.messages,
		Logs:         b.logs,
		PersonaFiles: b.persona,
		Warnings:     b.warnings,
	}, nil
}

func reqWarnings(req Request) []string {
	var warnings []string
	if req.OpenCode == nil {
		warnings = append(warnings, "OpenCode service was not read; session transcripts are omitted")
	}
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		warnings = append(warnings, fmt.Sprintf(
			"single-session export scoped to %s: unrelated communication rows and global orchestrator/memory data are omitted; persona and skills are included as agent identity",
			sessionID))
	}
	return warnings
}

func (b *bundle) writeBundle(ctx context.Context) error {
	if err := b.exportPersona(); err != nil {
		return err
	}
	if err := b.exportSessions(ctx); err != nil {
		return err
	}
	if err := b.exportOrchestrator(); err != nil {
		return err
	}
	if err := b.exportCommunication(); err != nil {
		return err
	}
	if err := b.exportMemory(); err != nil {
		return err
	}

	manifest := manifest{
		Format:         "acolyte-memory-trace/v1",
		Agent:          agentManifestFrom(b.request.Agent),
		ExportedAt:     b.now,
		AcolyteVersion: b.request.AcolyteVersion,
		Sanitized:      b.request.Sanitize,
		ScopedSession:  strings.TrimSpace(b.request.SessionID),
		Sessions:       b.sessions,
		Counts: counts{
			Sessions:     len(b.sessions),
			Messages:     b.messages,
			Logs:         b.logs,
			PersonaFiles: b.persona,
			CommInbox:    b.commIn,
			CommOutbox:   b.commOut,
			CommMedia:    b.commMedia,
			MemoryDreams: b.dreams,
		},
		Warnings: b.warnings,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("traceexport: marshal manifest: %w", err)
	}
	raw = append(raw, '\n')
	if err := b.write("manifest.json", raw); err != nil {
		return err
	}
	if err := b.write("README.md", []byte(b.readme(manifest))); err != nil {
		return err
	}
	return nil
}

func (b *bundle) write(name string, data []byte) error {
	clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(name), "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("traceexport: unsafe zip entry %q", name)
	}
	if b.request.Sanitize {
		data = sanitizeEntry(clean, data)
	}
	header := &zip.FileHeader{Name: clean, Method: zip.Deflate, Modified: b.now}
	header.SetMode(fs.FileMode(0644))
	writer, err := b.zip.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("traceexport: create zip entry %q: %w", clean, err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("traceexport: write zip entry %q: %w", clean, err)
	}
	b.files++
	return nil
}

func (b *bundle) warn(format string, args ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, args...))
}

// singleSession reports whether this export is scoped to one session. A
// scoped bundle must not disclose anything beyond that session: no unrelated
// conversations, no global orchestrator state, no global memory rows.
func (b *bundle) singleSession() bool {
	return strings.TrimSpace(b.request.SessionID) != ""
}

func (b *bundle) exportPersona() error {
	workspace := b.request.Agent.Workspace
	files, warnings, err := personaFiles(workspace)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		b.warn("%s", warning)
	}
	for _, rel := range files {
		data, ok, err := readRegularFile(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := b.write("persona/"+rel, data); err != nil {
			return err
		}
		b.persona++
	}
	return nil
}

func personaFiles(workspace string) ([]string, []string, error) {
	var files []string
	var warnings []string
	for _, rel := range []string{"AGENTS.md", "opencode.jsonc"} {
		files = appendIfRegular(files, filepath.Join(workspace, rel), rel, &warnings)
	}

	entries, err := os.ReadDir(filepath.Join(workspace, ".acolyte"))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("traceexport: read persona directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(".acolyte", entry.Name()))
		files = appendIfRegular(files, filepath.Join(workspace, filepath.FromSlash(rel)), rel, &warnings)
	}

	for _, root := range []string{".agents/skills", ".opencode/agents"} {
		base := filepath.Join(workspace, filepath.FromSlash(root))
		err := filepath.WalkDir(base, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(workspace, filePath)
			if err != nil {
				return err
			}
			files = appendIfRegular(files, filePath, filepath.ToSlash(rel), &warnings)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("traceexport: walk %s: %w", root, err)
		}
	}
	sort.Strings(files)
	return files, warnings, nil
}

func appendIfRegular(files []string, filePath, rel string, warnings *[]string) []string {
	info, err := os.Lstat(filePath)
	if os.IsNotExist(err) {
		return files
	}
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("stat %s: %v", rel, err))
		return files
	}
	if info.Mode()&os.ModeSymlink != 0 {
		*warnings = append(*warnings, fmt.Sprintf("skipped symlink %s", rel))
		return files
	}
	if !info.Mode().IsRegular() {
		*warnings = append(*warnings, fmt.Sprintf("skipped non-regular file %s", rel))
		return files
	}
	return append(files, rel)
}

func readRegularFile(filePath string) ([]byte, bool, error) {
	info, err := os.Lstat(filePath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("traceexport: stat %s: %w", filePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false, fmt.Errorf("traceexport: read %s: %w", filePath, err)
	}
	return data, true, nil
}

func (b *bundle) exportSessions(ctx context.Context) error {
	if b.request.OpenCode == nil {
		return b.writeSessionIndex(nil)
	}

	mappings, err := b.request.Store.Orch().ListSessions()
	if err != nil {
		return err
	}
	mappingByID := make(map[string]orch.Session, len(mappings))
	for _, mapping := range mappings {
		mappingByID[mapping.OpenCodeSessionID] = mapping
	}

	infos, err := b.request.OpenCode.ListSessions(ctx)
	if err != nil {
		if b.request.SessionID == "" {
			return fmt.Errorf("traceexport: list OpenCode sessions: %w", err)
		}
		infos = nil
	}
	infoByID := make(map[string]opencode.SessionInfo, len(infos))
	for _, info := range infos {
		infoByID[info.ID] = info
	}

	selected := make(map[string]opencode.SessionInfo)
	for _, info := range infos {
		if b.request.SessionID != "" && info.ID != b.request.SessionID {
			continue
		}
		_, mapped := mappingByID[info.ID]
		if !mapped && !sameWorkspace(info.Location.Directory, b.request.Agent.Workspace) {
			continue
		}
		selected[info.ID] = info
	}

	getter, hasGetter := b.request.OpenCode.(sessionGetter)
	for sessionID, mapping := range mappingByID {
		if b.request.SessionID != "" && sessionID != b.request.SessionID {
			continue
		}
		if _, ok := selected[sessionID]; ok {
			continue
		}
		if hasGetter {
			info, getErr := getter.GetSession(ctx, sessionID)
			if getErr == nil && info != nil {
				selected[sessionID] = *info
				continue
			}
		}
		// Keep the mapping even when the service cannot project the session;
		// SessionMessages below may still return its transcript.
		selected[sessionID] = opencode.SessionInfo{ID: sessionID}
		b.warn("session %s is mapped but missing from the OpenCode session list", mapping.OpenCodeSessionID)
	}

	if b.request.SessionID != "" {
		if _, ok := selected[b.request.SessionID]; !ok {
			return fmt.Errorf("traceexport: session %q is not mapped to agent %q", b.request.SessionID, b.request.Agent.Name)
		}
	}

	ids := make([]string, 0, len(selected))
	for sessionID := range selected {
		ids = append(ids, sessionID)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := selected[ids[i]], selected[ids[j]]
		if left.Time.Created != right.Time.Created {
			return left.Time.Created < right.Time.Created
		}
		return ids[i] < ids[j]
	})

	inbox, outbox, err := b.communicationBySession()
	if err != nil {
		return err
	}

	for _, sessionID := range ids {
		if err := b.exportSession(ctx, selected[sessionID], mappingByID[sessionID], inbox[sessionID], outbox[sessionID]); err != nil {
			return err
		}
	}
	return b.writeSessionIndex(b.sessions)
}

func (b *bundle) exportSession(ctx context.Context, info opencode.SessionInfo, mapping orch.Session, inbox []commtg.InboxItem, outbox []commtg.OutboxItem) error {
	sessionID := info.ID
	messages, truncated, err := b.request.OpenCode.SessionMessages(ctx, sessionID, b.request.MaxPages)
	if err != nil {
		b.warn("read messages for session %s: %v", sessionID, err)
		messages = nil
	}
	native, nativeOK := b.nativeSession(ctx, sessionID)
	doc := sessionDoc{
		Info:      info,
		Mapping:   mapping,
		Messages:  messages,
		Truncated: truncated,
		Inbox:     inbox,
		Outbox:    outbox,
	}
	body := renderSession(doc)
	safeID := safeSessionID(sessionID)
	markdownPath := "sessions/" + safeID + ".md"
	if err := b.write(markdownPath, []byte(body)); err != nil {
		return err
	}
	entry := sessionManifest{
		ID:        sessionID,
		Thread:    mapping.Thread,
		Title:     info.Title,
		CreatedAt: sessionTime(info.Time.Created),
		UpdatedAt: sessionTime(info.Time.Updated),
		Messages:  len(messages),
		Truncated: truncated,
		Markdown:  markdownPath,
	}
	if mapping.OpenCodeSessionID != "" && mapping.OpenCodeSessionID != sessionID {
		entry.MappingWarning = "mapping points at a different OpenCode session"
	}
	if nativeOK {
		entry.NativeJSON = "sessions/" + safeID + ".json"
		if err := b.write(entry.NativeJSON, native); err != nil {
			return err
		}
	}
	b.sessions = append(b.sessions, entry)
	b.messages += len(messages)
	return nil
}

func (b *bundle) nativeSession(ctx context.Context, sessionID string) ([]byte, bool) {
	exporter, ok := b.request.OpenCode.(nativeExporter)
	if !ok {
		return nil, false
	}
	raw, err := exporter.ExportSession(ctx, sessionID)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return append(raw, '\n'), true
}

func (b *bundle) communicationBySession() (map[string][]commtg.InboxItem, map[string][]commtg.OutboxItem, error) {
	inboxBySession := make(map[string][]commtg.InboxItem)
	outboxBySession := make(map[string][]commtg.OutboxItem)

	inbox, err := b.request.Store.CommTG().ListAllInbox()
	if err != nil {
		return nil, nil, err
	}
	for _, item := range inbox {
		if item.OpenCodeSessionID == "" {
			continue
		}
		inboxBySession[item.OpenCodeSessionID] = append(inboxBySession[item.OpenCodeSessionID], item)
	}
	outbox, err := b.request.Store.CommTG().ListAllOutbox()
	if err != nil {
		return nil, nil, err
	}
	for _, item := range outbox {
		if item.OpenCodeSessionID == "" {
			continue
		}
		outboxBySession[item.OpenCodeSessionID] = append(outboxBySession[item.OpenCodeSessionID], item)
	}
	// Counts are owned by exportCommunication, which is the single writer of
	// the communication/ files and therefore of the manifest counts.
	return inboxBySession, outboxBySession, nil
}

func (b *bundle) exportOrchestrator() error {
	sessions, err := b.request.Store.Orch().ListSessions()
	if err != nil {
		return err
	}
	if b.singleSession() {
		// Keep only the selected session's mapping. Logs, cron jobs/routes,
		// and settings are global to the agent and would disclose unrelated
		// activity, so a scoped bundle omits them entirely.
		sessions = filterSessionsForMapping(sessions, b.request.SessionID)
	}
	if err := b.writeJSON("orchestrator/sessions.json", sessions); err != nil {
		return err
	}
	if b.singleSession() {
		return nil
	}

	logs, err := b.request.Store.Orch().ListLogs(0)
	if err != nil {
		return err
	}
	b.logs = len(logs)
	if err := b.write("orchestrator/logs.md", []byte(renderLogs(logs))); err != nil {
		return err
	}

	jobs, err := b.request.Store.Orch().ListCronJobs()
	if err != nil {
		return err
	}
	if err := b.writeJSON("orchestrator/cron-jobs.json", jobs); err != nil {
		return err
	}

	routes, err := b.request.Store.Orch().ListCronRoutes()
	if err != nil {
		return err
	}
	if err := b.writeJSON("orchestrator/cron-routes.json", routes); err != nil {
		return err
	}

	settings, err := b.request.Store.Orch().ListSettings()
	if err != nil {
		return err
	}
	if err := b.writeJSON("orchestrator/settings.json", settings); err != nil {
		return err
	}
	return nil
}

func (b *bundle) exportCommunication() error {
	inbox, err := b.request.Store.CommTG().ListAllInbox()
	if err != nil {
		return err
	}
	outbox, err := b.request.Store.CommTG().ListAllOutbox()
	if err != nil {
		return err
	}
	chats, err := b.request.Store.CommTG().ListChats()
	if err != nil {
		return err
	}
	media, err := b.request.Store.CommTG().ListMedia()
	if err != nil {
		return err
	}

	single := b.singleSession()
	var chatIDs map[int64]bool
	if single {
		// Scope every communication slice to the selected session: its rows,
		// the chats those rows reference, and the media for those chats.
		inbox = filterInboxForSession(inbox, b.request.SessionID)
		outbox = filterOutboxForSession(outbox, b.request.SessionID)
		chatIDs = make(map[int64]bool)
		for _, item := range inbox {
			chatIDs[item.ChatID] = true
		}
		for _, item := range outbox {
			chatIDs[item.ChatID] = true
		}
		chats = filterChatsByIDs(chats, chatIDs)
		if len(chatIDs) == 0 {
			media = nil // no chat is known for this session: omit the index
		} else {
			media = filterMediaByIDs(media, chatIDs)
		}
	}
	b.commIn = len(inbox)
	b.commOut = len(outbox)
	b.commMedia = len(media)

	if err := b.writeJSON("communication/chats.json", chats); err != nil {
		return err
	}
	if err := b.writeJSON("communication/inbox.json", inbox); err != nil {
		return err
	}
	if err := b.writeJSON("communication/outbox.json", outbox); err != nil {
		return err
	}
	if !single || len(chatIDs) > 0 {
		if err := b.writeJSON("communication/media.json", media); err != nil {
			return err
		}
	}
	if single {
		// Channel settings are global to the agent, not to one conversation.
		return nil
	}
	settings, err := b.request.Store.CommTG().ListSettings()
	if err != nil {
		return err
	}
	return b.writeJSON("communication/settings.json", settings)
}

// filterInboxForSession keeps only inbound rows produced by sessionID.
func filterInboxForSession(items []commtg.InboxItem, sessionID string) []commtg.InboxItem {
	filtered := make([]commtg.InboxItem, 0, len(items))
	for _, item := range items {
		if item.OpenCodeSessionID == sessionID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// filterOutboxForSession keeps only outbound rows produced by sessionID.
func filterOutboxForSession(items []commtg.OutboxItem, sessionID string) []commtg.OutboxItem {
	filtered := make([]commtg.OutboxItem, 0, len(items))
	for _, item := range items {
		if item.OpenCodeSessionID == sessionID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// filterChatsByIDs keeps only chats referenced by the exported rows.
func filterChatsByIDs(chats []commtg.Chat, chatIDs map[int64]bool) []commtg.Chat {
	filtered := make([]commtg.Chat, 0, len(chats))
	for _, chat := range chats {
		if chatIDs[chat.ChatID] {
			filtered = append(filtered, chat)
		}
	}
	return filtered
}

// filterMediaByIDs keeps only media rows saved for the referenced chats.
func filterMediaByIDs(media []commtg.MediaItem, chatIDs map[int64]bool) []commtg.MediaItem {
	filtered := make([]commtg.MediaItem, 0, len(media))
	for _, item := range media {
		if chatIDs[item.ChatID] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// filterSessionsForMapping keeps only the selected session's thread mapping.
func filterSessionsForMapping(sessions []orch.Session, sessionID string) []orch.Session {
	filtered := make([]orch.Session, 0, len(sessions))
	for _, mapping := range sessions {
		if mapping.OpenCodeSessionID == sessionID {
			filtered = append(filtered, mapping)
		}
	}
	return filtered
}

func (b *bundle) exportMemory() error {
	if b.singleSession() {
		// Dream audit rows are global memory, unrelated to one conversation.
		return nil
	}
	dreams, err := b.request.Store.Mem().ListDreams(0)
	if err != nil {
		return err
	}
	b.dreams = len(dreams)
	return b.writeJSON("memory/dreams.json", dreams)
}

func (b *bundle) writeJSON(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("traceexport: marshal %s: %w", name, err)
	}
	raw = append(raw, '\n')
	return b.write(name, raw)
}

func (b *bundle) writeSessionIndex(sessions []sessionManifest) error {
	var builder strings.Builder
	builder.WriteString("# Session index\n\n")
	if len(sessions) == 0 {
		builder.WriteString("_No OpenCode sessions were exported._\n")
		return b.write("session-index.md", []byte(builder.String()))
	}
	builder.WriteString("| # | Session | Thread | Title | Messages | File |\n")
	builder.WriteString("| - | ------- | ------ | ----- | -------- | ---- |\n")
	for index, session := range sessions {
		title := strings.ReplaceAll(session.Title, "|", "\\|")
		thread := strings.ReplaceAll(session.Thread, "|", "\\|")
		fmt.Fprintf(&builder, "| %d | `%s` | `%s` | %s | %d | [%s](%s) |\n",
			index+1, session.ID, thread, title, session.Messages, session.Markdown, session.Markdown)
	}
	return b.write("session-index.md", []byte(builder.String()))
}

func (b *bundle) readme(m manifest) string {
	var builder strings.Builder
	builder.WriteString("# Acolyte memory trace — ")
	builder.WriteString(m.Agent.Name)
	builder.WriteString("\n\n")
	fmt.Fprintf(&builder, "Exported %s from `%s` by Acolyte", m.ExportedAt.Format(time.RFC3339), m.Agent.Workspace)
	if m.AcolyteVersion != "" {
		builder.WriteString(" " + m.AcolyteVersion)
	}
	builder.WriteString(".\n\n")
	builder.WriteString("This bundle is a portable trace of the agent. It is intentionally self-contained: ")
	builder.WriteString("a model can read the Markdown directly, without Acolyte or OpenCode.\n\n")
	if m.ScopedSession != "" {
		fmt.Fprintf(&builder, "This is a **single-session export** scoped to `%s`: `communication/` holds only that session's inbox/outbox rows, the chats they reference, and the media for those chats (channel settings are omitted); `orchestrator/` keeps only its session mapping (no logs, cron jobs, routes, or settings); and `memory/dreams.json` is omitted. Persona and skills are still included because they are agent identity, not conversation content.\n\n", m.ScopedSession)
	}
	builder.WriteString("## Contents\n\n")
	builder.WriteString("- `session-index.md` — every exported session, with links.\n")
	builder.WriteString("- `sessions/<id>.md` — one full timeline per session: messages, reasoning, tool calls, and communication routing events.\n")
	builder.WriteString("- `sessions/<id>.json` — the OpenCode-native session transfer payload, when the service supports it. Import with `POST /api/experimental/session/import`.\n")
	builder.WriteString("- `persona/` — `AGENTS.md`, `opencode.jsonc`, `.acolyte/*.md`, `.agents/skills/**`, and `.opencode/agents/**`.\n")
	builder.WriteString("- `orchestrator/` — audit logs, cron jobs, session mappings, delivery routes, and capability settings.\n")
	builder.WriteString("- `communication/` — Telegram chats, inbox, outbox, media index, and channel settings.\n")
	builder.WriteString("- `memory/` — memory-learn (dream) audit rows.\n")
	builder.WriteString("- `manifest.json` — machine-readable index with counts and warnings.\n\n")
	builder.WriteString("## Using this as memory\n\n")
	builder.WriteString("Point another agent at the unzipped folder and ask it to read `README.md`, `manifest.json`, and the session files. ")
	builder.WriteString("The Markdown files are the primary, human- and model-readable trace; the JSON files preserve exact routing and OpenCode data.\n\n")
	builder.WriteString("For an OpenCode agent, the native `sessions/<id>.json` files can be imported through the experimental session import API. ")
	builder.WriteString("The persona and skills can be copied into another workspace under `.acolyte/` and `.agents/skills/`.\n\n")
	builder.WriteString("## Secrets\n\n")
	builder.WriteString("Raw traces may contain secrets from the source workspace (environment values, tokens, keys) embedded in conversation text and tool output. ")
	if m.Sanitized {
		builder.WriteString("This bundle was exported with `--sanitize`: common secret patterns were redacted on a best-effort basis, but sanitization cannot catch every secret — review before sharing.\n\n")
	} else {
		builder.WriteString("This bundle was exported without `--sanitize`, so nothing was redacted. Re-export with `--sanitize` to redact common secret patterns on a best-effort basis — it cannot catch every secret either.\n\n")
	}
	fmt.Fprintf(&builder, "## Counts\n\n- Sessions: %d\n- Messages: %d\n- Orchestrator log entries: %d\n- Persona/skill files: %d\n- Communication inbox/outbox: %d/%d\n- Memory dreams: %d\n",
		m.Counts.Sessions, m.Counts.Messages, m.Counts.Logs, m.Counts.PersonaFiles,
		m.Counts.CommInbox, m.Counts.CommOutbox, m.Counts.MemoryDreams)
	if len(m.Warnings) > 0 {
		builder.WriteString("\n## Warnings\n\n")
		for _, warning := range m.Warnings {
			builder.WriteString("- " + warning + "\n")
		}
	}
	return builder.String()
}

func agentManifestFrom(agent registry.Agent) agentManifest {
	return agentManifest{
		Name:         agent.Name,
		Workspace:    agent.Workspace,
		Description:  agent.Description,
		Engine:       agent.Engine,
		RegisteredAt: agent.RegisteredAt,
		LastSeenAt:   agent.LastSeenAt,
	}
}

func safeSessionID(sessionID string) string {
	var builder strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "session"
	}
	return builder.String()
}

func sameWorkspace(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	canonicalLeft, err := paths.Canonical(left)
	if err != nil {
		canonicalLeft = filepath.Clean(left)
	}
	canonicalRight, err := paths.Canonical(right)
	if err != nil {
		canonicalRight = filepath.Clean(right)
	}
	return canonicalLeft == canonicalRight
}

func sessionTime(milliseconds float64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(milliseconds)).UTC()
}
