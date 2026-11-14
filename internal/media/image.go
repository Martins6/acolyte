package media

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"

	_ "golang.org/x/image/webp"
)

// imageFormat is the detected on-disk content format of an outbound image.
type imageFormat string

const (
	imageFormatJPEG imageFormat = "jpeg"
	imageFormatPNG  imageFormat = "png"
	imageFormatWebP imageFormat = "webp"
)

// maxOutboundPixels bounds the decoded pixel count before a full decode so a
// crafted image cannot trigger huge allocations.
const maxOutboundPixels int64 = 50_000_000

func (f imageFormat) label() string {
	switch f {
	case imageFormatJPEG:
		return "JPEG"
	case imageFormatPNG:
		return "PNG"
	case imageFormatWebP:
		return "WebP"
	default:
		return string(f)
	}
}

// detectImageFormat sniffs the leading magic bytes of an image.
func detectImageFormat(header []byte) (imageFormat, bool) {
	switch {
	case bytes.HasPrefix(header, []byte{0xFF, 0xD8, 0xFF}):
		return imageFormatJPEG, true
	case bytes.HasPrefix(header, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return imageFormatPNG, true
	case len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return imageFormatWebP, true
	default:
		return "", false
	}
}

// imageFormatForExtension maps a supported file extension to its required
// content format.
func imageFormatForExtension(extension string) (imageFormat, bool) {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return imageFormatJPEG, true
	case ".png":
		return imageFormatPNG, true
	case ".webp":
		return imageFormatWebP, true
	default:
		return "", false
	}
}

// validateImageFile confirms that path contains a decodable JPEG, PNG, or WebP
// image whose content format matches extension. It parses the header for
// dimensions, enforces a pixel-count bound, then fully decodes the image.
func validateImageFile(path, extension string) error {
	want, ok := imageFormatForExtension(extension)
	if !ok {
		return fmt.Errorf("media: unsupported image type %q (use jpg, jpeg, png, or webp)", extension)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("media: open image %s: %w", path, err)
	}
	defer file.Close()

	var header [12]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil {
		return fmt.Errorf("media: image %s is not a valid JPEG, PNG, or WebP file: %w", path, err)
	}
	got, ok := detectImageFormat(header[:n])
	if !ok {
		return fmt.Errorf("media: image %s is not a valid JPEG, PNG, or WebP file", path)
	}
	if got != want {
		return fmt.Errorf("media: image %s contains %s data but extension %q requires %s", path, got.label(), extension, want.label())
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("media: rewind image %s: %w", path, err)
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return fmt.Errorf("media: decode image %s: %w", path, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return fmt.Errorf("media: image %s has invalid dimensions %dx%d", path, config.Width, config.Height)
	}
	if width, height := int64(config.Width), int64(config.Height); width > maxOutboundPixels/height {
		return fmt.Errorf("media: image %s is %dx%d, exceeding the %d-pixel limit", path, config.Width, config.Height, maxOutboundPixels)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("media: rewind image %s: %w", path, err)
	}
	if _, _, err := image.Decode(file); err != nil {
		return fmt.Errorf("media: decode image %s: %w", path, err)
	}
	return nil
}
