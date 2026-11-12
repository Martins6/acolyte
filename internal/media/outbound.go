package media

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// MaxOutboundPhotoBytes is Telegram's cloud Bot API photo limit.
	MaxOutboundPhotoBytes int64 = 10 << 20
	// MaxOutboundCaptionRunes is Telegram's caption limit.
	MaxOutboundCaptionRunes = 1024
)

// PrepareOutboundImage validates a single image and canonicalizes it under
// <workspace>/media/outbound/images/. A source already in that directory is
// accepted unchanged; any other workspace file is copied there. The function
// rejects symlinks, paths outside the workspace, non-image extensions, and
// oversized files.
func PrepareOutboundImage(workspace, source string) (string, error) {
	workspace, err := filepath.EvalSymlinks(strings.TrimSpace(workspace))
	if err != nil {
		return "", fmt.Errorf("media: resolve workspace: %w", err)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("media: image path is required")
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(workspace, source)
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("media: resolve image path: %w", err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("media: inspect image %s: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("media: image %s is a symlink", source)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("media: image %s is not a regular file", source)
	}

	real, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("media: resolve image %s: %w", source, err)
	}
	relative, err := filepath.Rel(workspace, real)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("media: image %s is outside the workspace", source)
	}
	info, err = os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("media: inspect image %s: %w", real, err)
	}
	if info.Size() > MaxOutboundPhotoBytes {
		return "", fmt.Errorf("media: image %s exceeds the %d-byte photo limit", source, MaxOutboundPhotoBytes)
	}

	extension := strings.ToLower(filepath.Ext(real))
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		return "", fmt.Errorf("media: unsupported image type %q (use jpg, jpeg, png, or webp)", extension)
	}

	outboundDir := filepath.Join(workspace, "media", "outbound", "images")
	if insideDir(outboundDir, real) {
		return real, nil
	}
	destination := GetOutboundFilePath(workspace, extension)
	if err := copyImage(real, destination); err != nil {
		return "", err
	}
	return destination, nil
}

// ValidateOutboundCaption checks Telegram's caption limit for one image.
func ValidateOutboundCaption(caption string) error {
	if utf8.RuneCountInString(strings.TrimSpace(caption)) > MaxOutboundCaptionRunes {
		return fmt.Errorf("media: caption exceeds %d characters", MaxOutboundCaptionRunes)
	}
	return nil
}

func insideDir(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyImage(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("media: open image %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("media: create outbound image: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(destination)
		return fmt.Errorf("media: copy outbound image: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("media: close outbound image: %w", err)
	}
	return nil
}
