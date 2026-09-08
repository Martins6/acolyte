package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type MediaType string

const (
	MediaTypePhoto    MediaType = "images"
	MediaTypeAudio    MediaType = "audio"
	MediaTypeVoice    MediaType = "audio"
	MediaTypeDocument MediaType = "documents"
	MediaTypeVideo    MediaType = "videos"
)

type MediaMetadata struct {
	Kind         string
	FileSize     int64
	MimeType     string
	OriginalName string
}

func GetMediaType(message *models.Message) (MediaType, string, error) {
	switch {
	case message.Photo != nil:
		return MediaTypePhoto, ".jpg", nil
	case message.Audio != nil:
		ext := ".mp3"
		if message.Audio.FileName != "" {
			ext = filepath.Ext(message.Audio.FileName)
		}
		return MediaTypeAudio, ext, nil
	case message.Voice != nil:
		return MediaTypeVoice, ".ogg", nil
	case message.Document != nil:
		ext := ".bin"
		if message.Document.FileName != "" {
			ext = filepath.Ext(message.Document.FileName)
		}
		return MediaTypeDocument, ext, nil
	case message.Video != nil:
		ext := ".mp4"
		if message.Video.FileName != "" {
			ext = filepath.Ext(message.Video.FileName)
		}
		return MediaTypeVideo, ext, nil
	default:
		return "", "", fmt.Errorf("unknown media type")
	}
}

func GetFilePath(workspace string, mediaType MediaType, ext string) string {
	downloadsDir := filepath.Join(workspace, "downloads", string(mediaType))
	os.MkdirAll(downloadsDir, 0755)
	return filepath.Join(downloadsDir, GenerateTimestampedFilename(downloadsDir, ext))
}

func DownloadFile(ctx context.Context, b *bot.Bot, fileID string) ([]byte, string, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", fmt.Errorf("failed to get file: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.Token(), file.FilePath)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read file body: %w", err)
	}

	return data, file.FilePath, nil
}

func SaveFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

func BuildPrompt(mediaPath string, meta MediaMetadata, userMessage string) string {
	var b strings.Builder
	b.WriteString("File located at: ")
	b.WriteString(mediaPath)
	b.WriteByte('\n')
	b.WriteString("File type: ")
	b.WriteString(meta.Kind)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "File size: %d bytes\n", meta.FileSize)
	if meta.OriginalName != "" {
		b.WriteString("Original name: ")
		b.WriteString(meta.OriginalName)
		b.WriteByte('\n')
	}
	if meta.MimeType != "" {
		b.WriteString("MIME type: ")
		b.WriteString(meta.MimeType)
		b.WriteByte('\n')
	}
	b.WriteString("\nUser message: ")
	b.WriteString(userMessage)
	return b.String()
}

// ExtractFileRef returns the Telegram file ref for a message. The case order
// is intentionally kept in sync with mediaKindLabel in internal/bot/handlers.go
// (Photo, Document, Audio, Voice, Video) so the two helpers stay paired.
func ExtractFileRef(message *models.Message) (fileID string, fileSize int64, mimeType string, originalName string, mediaType MediaType, ext string, ok bool) {
	switch {
	case len(message.Photo) > 0:
		largest := message.Photo[len(message.Photo)-1]
		return largest.FileID, int64(largest.FileSize), "", "", MediaTypePhoto, ".jpg", true
	case message.Document != nil:
		ext := ".bin"
		if message.Document.FileName != "" {
			ext = filepath.Ext(message.Document.FileName)
		}
		return message.Document.FileID, message.Document.FileSize, message.Document.MimeType, message.Document.FileName, MediaTypeDocument, ext, true
	case message.Audio != nil:
		ext := ".mp3"
		if message.Audio.FileName != "" {
			ext = filepath.Ext(message.Audio.FileName)
		}
		return message.Audio.FileID, message.Audio.FileSize, message.Audio.MimeType, message.Audio.FileName, MediaTypeAudio, ext, true
	case message.Voice != nil:
		return message.Voice.FileID, message.Voice.FileSize, message.Voice.MimeType, "", MediaTypeVoice, ".ogg", true
	case message.Video != nil:
		ext := ".mp4"
		if message.Video.FileName != "" {
			ext = filepath.Ext(message.Video.FileName)
		}
		return message.Video.FileID, message.Video.FileSize, message.Video.MimeType, message.Video.FileName, MediaTypeVideo, ext, true
	default:
		return "", 0, "", "", "", "", false
	}
}

func GenerateTimestampedFilename(dir, ext string) string {
	base := time.Now().UTC().Format("20060102_150405")
	name := base + ext
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return name
		}
		name = fmt.Sprintf("%s_%d%s", base, i, ext)
	}
}
