package traceimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeIndexPreservesUserContent(t *testing.T) {
	t.Parallel()

	existing := "# My notes\n\nUser-owned prose before the block.\n\n" +
		indexStartMark + "\n" + indexTableHeader + "\n" + indexTableSep + "\n" +
		"| `oldtrace` | other | 2026-01-01T00:00:00Z | 1 | 2 | `downloads/memory-traces/oldtrace` |\n" +
		indexEndMark + "\n\nUser-owned prose after the block.\n"

	merged, err := mergeIndex(existing, indexRow{
		TraceID:     "newtrace",
		SourceAgent: "valet",
		ExportedAt:  exportedAt,
		Sessions:    3,
		Messages:    42,
		Folder:      "downloads/memory-traces/newtrace",
	})
	if err != nil {
		t.Fatalf("mergeIndex() error: %v", err)
	}
	for _, want := range []string{
		"# My notes",
		"User-owned prose before the block.",
		"User-owned prose after the block.",
		"`oldtrace`",
		"`newtrace`",
		indexStartMark,
		indexEndMark,
	} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged index missing %q:\n%s", want, merged)
		}
	}
	// Exactly one block.
	if got := countOccurrences(merged, indexStartMark); got != 1 {
		t.Errorf("start markers = %d, want 1", got)
	}
	if got := countOccurrences(merged, indexEndMark); got != 1 {
		t.Errorf("end markers = %d, want 1", got)
	}
	// New row after the old one.
	if strings.Index(merged, "`oldtrace`") > strings.Index(merged, "`newtrace`") {
		t.Error("new row should be appended after existing rows")
	}
}

func TestMergeIndexUpdatesSameTraceInPlace(t *testing.T) {
	t.Parallel()

	row := indexRow{
		TraceID:     "abc123",
		SourceAgent: "valet",
		ExportedAt:  exportedAt,
		Sessions:    1,
		Messages:    2,
		Folder:      "downloads/memory-traces/abc123",
	}
	merged, err := mergeIndex("", row)
	if err != nil {
		t.Fatalf("mergeIndex() error: %v", err)
	}
	// Same trace, updated counts (a --force re-import of a newer export
	// with the same id must replace, not duplicate).
	row.Sessions = 9
	row.Messages = 99
	updated, err := mergeIndex(merged, row)
	if err != nil {
		t.Fatalf("mergeIndex() error: %v", err)
	}
	if got := countOccurrences(updated, "`abc123`"); got != 1 {
		t.Errorf("trace rows = %d, want 1:\n%s", got, updated)
	}
	if !strings.Contains(updated, "| 9 | 99 |") {
		t.Errorf("counts not updated:\n%s", updated)
	}
}

func TestMergeIndexCreatesFile(t *testing.T) {
	t.Parallel()

	merged, err := mergeIndex("", indexRow{
		TraceID: "t1", SourceAgent: "valet", ExportedAt: exportedAt,
		Folder: "downloads/memory-traces/t1",
	})
	if err != nil {
		t.Fatalf("mergeIndex() error: %v", err)
	}
	if !strings.HasPrefix(merged, "# Memory traces") {
		t.Errorf("new file should start with a heading:\n%s", merged)
	}
	if !strings.Contains(merged, indexStartMark) || !strings.Contains(merged, indexEndMark) {
		t.Errorf("new file missing markers:\n%s", merged)
	}
}

func TestMergeIndexAppendsToMarkerlessFile(t *testing.T) {
	t.Parallel()

	merged, err := mergeIndex("# Hand-written\n\nMy own content.\n", indexRow{
		TraceID: "t1", SourceAgent: "valet", ExportedAt: exportedAt,
		Folder: "downloads/memory-traces/t1",
	})
	if err != nil {
		t.Fatalf("mergeIndex() error: %v", err)
	}
	if !strings.Contains(merged, "# Hand-written") || !strings.Contains(merged, "My own content.") {
		t.Errorf("user content lost:\n%s", merged)
	}
	if strings.Index(merged, "My own content.") > strings.Index(merged, indexStartMark) {
		t.Error("block should be appended after user content")
	}
}

func TestMergeIndexRejectsUnbalancedMarkers(t *testing.T) {
	t.Parallel()

	if _, err := mergeIndex(indexStartMark+"\n| row |\n", indexRow{TraceID: "t1"}); err == nil {
		t.Fatal("mergeIndex() error = nil, want malformed block error")
	}
	if _, err := mergeIndex(indexEndMark+"\n", indexRow{TraceID: "t1"}); err == nil {
		t.Fatal("mergeIndex() error = nil, want malformed block error")
	}
}

func TestUpdateIndexWritesFile(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	changed, err := updateIndex(ws, indexRow{
		TraceID: "t1", SourceAgent: "valet", ExportedAt: exportedAt,
		Folder: "downloads/memory-traces/t1",
	})
	if err != nil {
		t.Fatalf("updateIndex() error: %v", err)
	}
	if !changed {
		t.Error("changed = false for a new index, want true")
	}
	content := readAll(t, indexFile(ws))
	if !strings.Contains(content, "`t1`") {
		t.Errorf("index missing row:\n%s", content)
	}
	// Second write with the same row is a no-op.
	changed, err = updateIndex(ws, indexRow{
		TraceID: "t1", SourceAgent: "valet", ExportedAt: exportedAt,
		Folder: "downloads/memory-traces/t1",
	})
	if err != nil {
		t.Fatalf("updateIndex() error: %v", err)
	}
	if changed {
		t.Error("changed = true for an identical row, want false")
	}
}

func TestUpdateIndexRefusesSymlink(t *testing.T) {
	t.Parallel()

	ws := tempWorkspace(t)
	acolyte := filepath.Join(ws, ".acolyte")
	if err := os.MkdirAll(acolyte, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, indexFile(ws)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := updateIndex(ws, indexRow{TraceID: "t1", SourceAgent: "valet"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("updateIndex() error = %v, want symlink refusal", err)
	}
	if got := readAll(t, outside); got != "outside\n" {
		t.Errorf("symlink target was modified: %q", got)
	}
}

func TestRenderRowEscapesCellContent(t *testing.T) {
	t.Parallel()

	row := renderRow(indexRow{
		TraceID:     "t1",
		SourceAgent: "evil | name\nwith newline",
		ExportedAt:  time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		Folder:      "downloads/memory-traces/t1",
	})
	if strings.Count(row, "\n") != 0 {
		t.Errorf("row contains a newline: %q", row)
	}
	if !strings.Contains(row, `evil \| name with newline`) {
		t.Errorf("pipe not escaped: %q", row)
	}
	if !strings.Contains(row, "2026-11-10T12:00:00Z") {
		t.Errorf("timestamp missing: %q", row)
	}
}
