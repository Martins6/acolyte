package traceimport

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// patchCentralDir applies mutate to the central-directory header (the fixed
// 46-byte record before the entry name) of the named entry, so tests can
// forge headers: understated sizes or wrong CRCs.
func patchCentralDir(t *testing.T, zipPath, name string, mutate func(hdr []byte)) {
	t.Helper()
	raw, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	sig := []byte{'P', 'K', 1, 2} // central directory file header signature
	for i := 0; i+46 <= len(raw); {
		j := bytes.Index(raw[i:], sig)
		if j < 0 {
			break
		}
		off := i + j
		nameLen := int(binary.LittleEndian.Uint16(raw[off+28 : off+30]))
		if off+46+nameLen <= len(raw) && string(raw[off+46:off+46+nameLen]) == name {
			mutate(raw[off : off+46])
			if err := os.WriteFile(zipPath, raw, 0o644); err != nil {
				t.Fatalf("write zip: %v", err)
			}
			return
		}
		i = off + len(sig)
	}
	t.Fatalf("central directory entry %q not found", name)
}

// TestValidateStreamsActualEntrySizes ensures dry-run validation counts the
// bytes actually decompressed: a zip whose central directory understates an
// entry's size must be rejected even though the declared size fits the cap.
func TestValidateStreamsActualEntrySizes(t *testing.T) {
	// Not parallel: this test mutates the package-level caps.
	origFile, origTotal := maxFileBytes, maxTotalBytes
	t.Cleanup(func() { maxFileBytes, maxTotalBytes = origFile, origTotal })
	maxFileBytes, maxTotalBytes = 400, 1<<20

	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json": manifestJSON(),
		"big.txt":       strings.Repeat("x", 4000),
	}, nil)
	// Understate the declared size so the header-based check alone would
	// let this over-limit entry through: declared 100 fits under the 400
	// byte cap, but the entry really holds 4000 bytes.
	patchCentralDir(t, zipPath, "big.txt", func(hdr []byte) {
		binary.LittleEndian.PutUint32(hdr[24:], 100)
	})

	bundle, err := openTrace(zipPath)
	if err != nil {
		t.Fatalf("openTrace: %v", err)
	}
	defer bundle.Close()
	patched := bundle.locate("big.txt")
	if patched == nil {
		t.Fatal("big.txt not found after patching")
	}
	if declared := patched.UncompressedSize64; declared != 100 {
		t.Fatalf("declared size = %d, want patched 100", declared)
	}
	if _, err := bundle.validate(); err == nil {
		t.Fatal("validate() error = nil, want rejection of understated over-limit entry")
	}

	// The same bundle must fail --dry-run before anything is written.
	ws := tempWorkspace(t)
	if _, err := Import(context.Background(), Request{
		Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace, DryRun: true,
	}); err == nil {
		t.Fatal("Import(dry-run) error = nil, want rejection")
	}
	if _, err := os.Stat(filepath.Join(ws, "downloads", "memory-traces")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote a trace directory (stat: %v)", err)
	}
}

// TestValidateDetectsCorruptEntries ensures dry-run validation opens every
// entry, so CRC/read errors surface before extraction instead of after a
// dry-run promised success.
func TestValidateDetectsCorruptEntries(t *testing.T) {
	// Not parallel: validate reads the package-level caps, which the
	// sequential cap-mutating tests in this package adjust.
	zipPath := filepath.Join(t.TempDir(), "trace.zip")
	buildZip(t, zipPath, map[string]string{
		"manifest.json": manifestJSON(),
		"data.txt":      strings.Repeat("y", 500),
	}, nil)
	// Corrupt data.txt's stored CRC so reading it fails checksum
	// verification. The manifest is untouched, so openTrace still succeeds.
	patchCentralDir(t, zipPath, "data.txt", func(hdr []byte) {
		crc := binary.LittleEndian.Uint32(hdr[16:])
		bad := crc ^ 0xFFFFFFFF
		if bad == 0 {
			bad = 1
		}
		binary.LittleEndian.PutUint32(hdr[16:], bad)
	})

	bundle, err := openTrace(zipPath)
	if err != nil {
		t.Fatalf("openTrace: %v", err)
	}
	defer bundle.Close()
	if _, err := bundle.validate(); err == nil {
		t.Fatal("validate() error = nil, want CRC error")
	}

	ws := tempWorkspace(t)
	if _, err := Import(context.Background(), Request{
		Agent: agentFor(ws), ZipPath: zipPath, Mode: ModeTrace, DryRun: true,
	}); err == nil {
		t.Fatal("Import(dry-run) error = nil, want CRC error")
	}
}
