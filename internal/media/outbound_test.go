package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testWebPBase64 is a known valid 1x1 white WebP image.
const testWebPBase64 = "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoBAAEAAgA0JaQAA3AA/vuUAAA="

func writeTestImage(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
}

// encodeTestImage returns a real image encoded in the format implied by the
// given extension.
func encodeTestImage(t *testing.T, extension string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	var err error
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case ".png":
		err = png.Encode(&buf, img)
	default:
		t.Fatalf("encodeTestImage: unsupported extension %q", extension)
	}
	if err != nil {
		t.Fatalf("encode %s image: %v", extension, err)
	}
	return buf.Bytes()
}

func decodeTestWebP(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(testWebPBase64)
	if err != nil {
		t.Fatalf("decode webp fixture: %v", err)
	}
	return data
}

func TestPrepareOutboundImageCopiesIntoOutbound(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "generated", "chart.png")
	want := encodeTestImage(t, ".png", 2, 2)
	writeTestImage(t, source, want)

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
	if err != nil || !bytes.Equal(data, want) {
		t.Errorf("copied data differs from source: len=%d err=%v", len(data), err)
	}
}

func TestPrepareOutboundImageKeepsOutboundFile(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "media", "outbound", "images", "chart.jpg")
	writeTestImage(t, source, encodeTestImage(t, ".jpg", 2, 2))

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

func TestPrepareOutboundImageAcceptsWebP(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "generated", "chart.webp")
	want := decodeTestWebP(t)
	writeTestImage(t, source, want)

	got, err := PrepareOutboundImage(workspace, source)
	if err != nil {
		t.Fatalf("PrepareOutboundImage() error: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil || !bytes.Equal(data, want) {
		t.Errorf("copied webp differs from source: len=%d err=%v", len(data), err)
	}
}

func TestPrepareOutboundImageRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeTestImage(t, outside, encodeTestImage(t, ".png", 2, 2))
	writeTestImage(t, filepath.Join(workspace, "notes.txt"), []byte("not an image"))

	cases := []struct {
		name   string
		source string
	}{
		{"outside workspace", outside},
		{"unsupported extension", filepath.Join(workspace, "notes.txt")},
		{"missing file", filepath.Join(workspace, "missing.png")},
	}
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

func TestPrepareOutboundImageRejectsInvalidContent(t *testing.T) {
	workspace := t.TempDir()
	pngData := encodeTestImage(t, ".png", 2, 2)
	corruptPNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 8)...)

	cases := []struct {
		name string
		path string
		data []byte
	}{
		{"text named png", filepath.Join(workspace, "text.png"), []byte("not an image")},
		{"png named jpg", filepath.Join(workspace, "mismatch.jpg"), pngData},
		{"truncated png", filepath.Join(workspace, "truncated.png"), pngData[:16]},
		{"corrupt png", filepath.Join(workspace, "corrupt.png"), corruptPNG},
		{"fake file already outbound", filepath.Join(workspace, "media", "outbound", "images", "fake.png"), []byte("not an image")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTestImage(t, tc.path, tc.data)
			if _, err := PrepareOutboundImage(workspace, tc.path); err == nil {
				t.Fatalf("PrepareOutboundImage(%q) error = nil, want error", tc.path)
			}
		})
	}
}

func TestPrepareOutboundImageRejectsOversize(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "large.png")
	writeTestImage(t, source, encodeTestImage(t, ".png", 2, 2))
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
