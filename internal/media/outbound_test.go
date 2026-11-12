package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestImage(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
}

func TestPrepareOutboundImageCopiesIntoOutbound(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "generated", "chart.png")
	writeTestImage(t, source, []byte("image-bytes"))

	got, err := PrepareOutboundImage(workspace, filepath.Join("generated", "chart.png"))
	if err != nil {
		t.Fatalf("PrepareOutboundImage() error: %v", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(workspace, "media", "outbound", "images"))
	if err != nil {
		t.Fatalf("resolve outbound dir: %v", err)
	}
	if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
		t.Errorf("path = %q, want prefix %q", got, dir)
	}
	if filepath.Base(got) == "chart.png" {
		t.Errorf("path = %q, want a timestamped copy", got)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != "image-bytes" {
		t.Errorf("copied data = %q, %v", data, err)
	}
}

func TestPrepareOutboundImageKeepsOutboundFile(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "media", "outbound", "images", "chart.jpg")
	writeTestImage(t, source, []byte("image-bytes"))

	got, err := PrepareOutboundImage(workspace, source)
	if err != nil {
		t.Fatalf("PrepareOutboundImage() error: %v", err)
	}
	want, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestPrepareOutboundImageRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeTestImage(t, outside, []byte("image-bytes"))

	cases := []struct {
		name   string
		source string
	}{
		{"outside workspace", outside},
		{"unsupported extension", filepath.Join(workspace, "notes.txt")},
		{"missing file", filepath.Join(workspace, "missing.png")},
	}
	writeTestImage(t, filepath.Join(workspace, "notes.txt"), []byte("not an image"))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PrepareOutboundImage(workspace, tc.source); err == nil {
				t.Fatalf("PrepareOutboundImage(%q) error = nil, want error", tc.source)
			}
		})
	}

	link := filepath.Join(workspace, "link.png")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := PrepareOutboundImage(workspace, link); err == nil {
			t.Fatal("PrepareOutboundImage(symlink) error = nil, want error")
		}
	}
}

func TestPrepareOutboundImageRejectsOversize(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "large.png")
	writeTestImage(t, source, nil)
	if err := os.Truncate(source, MaxOutboundPhotoBytes+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := PrepareOutboundImage(workspace, source); err == nil {
		t.Fatal("PrepareOutboundImage(oversize) error = nil, want error")
	}
}

func TestValidateOutboundCaption(t *testing.T) {
	if err := ValidateOutboundCaption(strings.Repeat("a", MaxOutboundCaptionRunes)); err != nil {
		t.Errorf("caption at limit error = %v", err)
	}
	if err := ValidateOutboundCaption(strings.Repeat("a", MaxOutboundCaptionRunes+1)); err == nil {
		t.Fatal("caption over limit error = nil, want error")
	}
}
