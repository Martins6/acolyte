package traceimport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Managed block markers for .acolyte/MEMORY-TRACES.md. Everything between the
// markers is owned by acolyte; everything outside is preserved untouched.
const (
	indexStartMark = "<!-- acolyte:memory-traces:start -->"
	indexEndMark   = "<!-- acolyte:memory-traces:end -->"
	indexRelPath   = ".acolyte/MEMORY-TRACES.md"
)

const indexTableHeader = "| Trace | Source agent | Exported at | Sessions | Messages | Folder |"
const indexTableSep = "| --- | --- | --- | --- | --- | --- |"

// indexRow is one trace's entry in the managed index table.
type indexRow struct {
	TraceID     string
	SourceAgent string
	ExportedAt  time.Time
	Sessions    int
	Messages    int
	Folder      string // workspace-relative, slash-separated
}

// indexPath returns the absolute path of the trace index for a workspace.
func indexPath(workspace string) string {
	return filepath.Join(workspace, filepath.FromSlash(indexRelPath))
}

// updateIndex upserts row into the managed block of the workspace trace
// index, preserving all user content outside the block. It reports whether
// the file changed.
func updateIndex(workspace string, row indexRow) (bool, error) {
	path := indexPath(workspace)
	if err := rejectSymlinkAncestors(workspace, filepath.Dir(path)); err != nil {
		return false, err
	}
	existing, err := readIndexFile(path)
	if err != nil {
		return false, err
	}
	merged, err := mergeIndex(existing, row)
	if err != nil {
		return false, err
	}
	if merged == existing {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("traceimport: create .acolyte directory: %w", err)
	}
	if err := writeFileAtomic(workspace, path, []byte(merged), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// readIndexFile reads an existing index. A symlinked index is refused so an
// import can never write through a link out of the workspace.
func readIndexFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("traceimport: stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("traceimport: refusing symlinked file %s", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("traceimport: %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("traceimport: read %s: %w", path, err)
	}
	return string(raw), nil
}

// mergeIndex returns existing with row upserted into the managed block.
func mergeIndex(existing string, row indexRow) (string, error) {
	start := strings.Index(existing, indexStartMark)
	end := strings.Index(existing, indexEndMark)
	switch {
	case start >= 0 && end > start:
		prefix := existing[:start+len(indexStartMark)]
		suffix := existing[end:]
		block := existing[start+len(indexStartMark) : end]
		return prefix + mergeBlock(block, row) + suffix, nil
	case start < 0 && end < 0:
		block := mergeBlock("", row)
		if strings.TrimSpace(existing) == "" {
			return indexFileIntro + indexStartMark + block + indexEndMark + "\n", nil
		}
		return strings.TrimRight(existing, "\n") + "\n\n" + indexStartMark + block + indexEndMark + "\n", nil
	default:
		return "", fmt.Errorf("traceimport: malformed memory-traces block (unbalanced %s markers)", indexStartMark)
	}
}

// indexFileIntro is written only when the index file is created.
const indexFileIntro = "# Memory traces\n\nTrace bundles imported by `acolyte memory import` under `downloads/memory-traces/`.\n\n"

// mergeBlock rebuilds the table inside a managed block: existing data rows
// for other traces are kept (replaced in place for this trace), the header
// and separator are regenerated, and any other content inside the block is
// dropped because the block is machine-managed.
func mergeBlock(block string, row indexRow) string {
	var rows []string
	replaced := false
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		id, ok := tableRowID(trimmed)
		if !ok {
			continue
		}
		if id == row.TraceID {
			rows = append(rows, renderRow(row))
			replaced = true
			continue
		}
		rows = append(rows, trimmed)
	}
	if !replaced {
		rows = append(rows, renderRow(row))
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(indexTableHeader + "\n")
	b.WriteString(indexTableSep + "\n")
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	return b.String()
}

// tableRowID extracts the backticked trace id from a table data row.
func tableRowID(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "|")
	if !ok {
		return "", false
	}
	cell, _, _ := strings.Cut(rest, "|")
	cell = strings.TrimSpace(cell)
	if len(cell) < 2 || !strings.HasPrefix(cell, "`") || !strings.HasSuffix(cell, "`") {
		return "", false
	}
	return strings.Trim(cell, "`"), true
}

// renderRow formats one index table row.
func renderRow(row indexRow) string {
	exported := "-"
	if !row.ExportedAt.IsZero() {
		exported = row.ExportedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("| `%s` | %s | %s | %d | %d | `%s` |",
		row.TraceID,
		escapeCell(row.SourceAgent),
		exported,
		row.Sessions,
		row.Messages,
		row.Folder)
}

// escapeCell keeps table cells single-line and pipe-safe.
func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory followed by a rename. workspace anchors the ancestor check so the
// temp file can never be created through a symlinked parent.
func writeFileAtomic(workspace, path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := rejectSymlinkAncestors(workspace, dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("traceimport: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("traceimport: write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("traceimport: protect %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("traceimport: close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("traceimport: finalize %s: %w", path, err)
	}
	return nil
}
