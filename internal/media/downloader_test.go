package media

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestGetMediaType_Photo(t *testing.T) {
	msg := &models.Message{
		Photo: []models.PhotoSize{{FileID: "abc", Width: 100, Height: 100}},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypePhoto {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypePhoto)
	}
	if ext != ".jpg" {
		t.Errorf("ext = %q, want %q", ext, ".jpg")
	}
}

func TestGetMediaType_Audio(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{FileID: "abc"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeAudio {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeAudio)
	}
	if ext != ".mp3" {
		t.Errorf("ext = %q, want %q", ext, ".mp3")
	}
}

func TestGetMediaType_Voice(t *testing.T) {
	msg := &models.Message{
		Voice: &models.Voice{FileID: "abc"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeVoice {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeVoice)
	}
	if ext != ".ogg" {
		t.Errorf("ext = %q, want %q", ext, ".ogg")
	}
}

func TestGetMediaType_Document(t *testing.T) {
	msg := &models.Message{
		Document: &models.Document{FileID: "abc", FileName: "report.pdf"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeDocument {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeDocument)
	}
	if ext != ".pdf" {
		t.Errorf("ext = %q, want %q", ext, ".pdf")
	}
}

func TestGetMediaType_DocumentNoName(t *testing.T) {
	msg := &models.Message{
		Document: &models.Document{FileID: "abc"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeDocument {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeDocument)
	}
	if ext != ".bin" {
		t.Errorf("ext = %q, want %q", ext, ".bin")
	}
}

func TestGetMediaType_Video(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{FileID: "abc"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeVideo {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeVideo)
	}
	if ext != ".mp4" {
		t.Errorf("ext = %q, want %q", ext, ".mp4")
	}
}

func TestGetMediaType_AudioWithName(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{FileID: "abc", FileName: "song.flac"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeAudio {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeAudio)
	}
	if ext != ".flac" {
		t.Errorf("ext = %q, want %q", ext, ".flac")
	}
}

func TestGetMediaType_VideoWithName(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{FileID: "abc", FileName: "clip.mov"},
	}
	mt, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mt != MediaTypeVideo {
		t.Errorf("MediaType = %q, want %q", mt, MediaTypeVideo)
	}
	if ext != ".mov" {
		t.Errorf("ext = %q, want %q", ext, ".mov")
	}
}

func TestGetMediaType_AudioNoName(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{FileID: "abc"},
	}
	_, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext != ".mp3" {
		t.Errorf("ext = %q, want %q (fallback when FileName is empty)", ext, ".mp3")
	}
}

func TestGetMediaType_VideoNoName(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{FileID: "abc"},
	}
	_, ext, err := GetMediaType(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext != ".mp4" {
		t.Errorf("ext = %q, want %q (fallback when FileName is empty)", ext, ".mp4")
	}
}

func TestGetMediaType_Empty(t *testing.T) {
	msg := &models.Message{}
	_, _, err := GetMediaType(msg)
	if err == nil {
		t.Fatal("expected error for empty message, got nil")
	}
}

func TestGetFilePath(t *testing.T) {
	ws := t.TempDir()
	path := GetFilePath(ws, MediaTypePhoto, ".jpg")
	dir := filepath.Join(ws, "downloads", "images")
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Errorf("GetFilePath = %q, want prefix %q", path, dir)
	}
	if !strings.HasSuffix(path, ".jpg") {
		t.Errorf("GetFilePath = %q, want suffix .jpg", path)
	}
	re := regexp.MustCompile(`\d{8}_\d{6}\.jpg$`)
	if !re.MatchString(path) {
		t.Errorf("GetFilePath = %q, want to match timestamp pattern", path)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("downloads/images directory was not created: %v", err)
	}
}

func TestGenerateTimestampedFilename(t *testing.T) {
	dir := t.TempDir()
	re := regexp.MustCompile(`^\d{8}_\d{6}\.jpg$`)

	t.Run("basic", func(t *testing.T) {
		name := GenerateTimestampedFilename(dir, ".jpg")
		if !re.MatchString(name) {
			t.Errorf("GenerateTimestampedFilename = %q, want to match %s", name, re)
		}
	})

	t.Run("collision", func(t *testing.T) {
		name1 := GenerateTimestampedFilename(dir, ".jpg")
		if err := os.WriteFile(filepath.Join(dir, name1), []byte("x"), 0644); err != nil {
			t.Fatalf("failed to seed collision file: %v", err)
		}
		name2 := GenerateTimestampedFilename(dir, ".jpg")
		ext := ".jpg"
		expected := name1[:len(name1)-len(ext)] + "_1" + ext
		if name2 != expected {
			t.Errorf("GenerateTimestampedFilename on collision = %q, want %q", name2, expected)
		}
		if _, err := os.Stat(filepath.Join(dir, name2)); err == nil {
			t.Errorf("expected %s not to exist yet", name2)
		}
	})
}

func TestBuildPrompt(t *testing.T) {
	got := BuildPrompt("/tmp/x.jpg", MediaMetadata{}, "hi")
	want := "File located at: /tmp/x.jpg\nFile type: \nFile size: 0 bytes\n\nUser message: hi"
	if got != want {
		t.Errorf("BuildPrompt = %q, want %q", got, want)
	}
}

func TestBuildPrompt_EmptyCaption(t *testing.T) {
	got := BuildPrompt("/tmp/x.jpg", MediaMetadata{}, "")
	want := "File located at: /tmp/x.jpg\nFile type: \nFile size: 0 bytes\n\nUser message: "
	if got != want {
		t.Errorf("BuildPrompt = %q, want %q", got, want)
	}
}

func TestBuildPrompt_WithMetadata(t *testing.T) {
	meta := MediaMetadata{
		Kind:         "document",
		FileSize:     2345678,
		MimeType:     "application/pdf",
		OriginalName: "report.pdf",
	}
	got := BuildPrompt("/tmp/report.pdf", meta, "hi")
	want := "File located at: /tmp/report.pdf\n" +
		"File type: document\n" +
		"File size: 2345678 bytes\n" +
		"Original name: report.pdf\n" +
		"MIME type: application/pdf\n" +
		"\n" +
		"User message: hi"
	if got != want {
		t.Errorf("BuildPrompt = %q, want %q", got, want)
	}
}

func TestBuildPrompt_PartialMetadata(t *testing.T) {
	meta := MediaMetadata{Kind: "photo", FileSize: 12345}
	got := BuildPrompt("/tmp/x.jpg", meta, "look")
	if !strings.Contains(got, "File located at: /tmp/x.jpg") {
		t.Errorf("missing 'File located at' line in:\n%s", got)
	}
	if !strings.Contains(got, "File type: photo") {
		t.Errorf("missing 'File type' line in:\n%s", got)
	}
	if !strings.Contains(got, "File size: 12345 bytes") {
		t.Errorf("missing 'File size' line in:\n%s", got)
	}
	if strings.Contains(got, "Original name:") {
		t.Errorf("'Original name' line should be absent when OriginalName is empty:\n%s", got)
	}
	if strings.Contains(got, "MIME type:") {
		t.Errorf("'MIME type' line should be absent when MimeType is empty:\n%s", got)
	}
	if !strings.HasSuffix(got, "User message: look") {
		t.Errorf("expected trailing 'User message: look', got suffix in:\n%s", got)
	}
}

func TestBuildPrompt_ZeroFileSize(t *testing.T) {
	meta := MediaMetadata{Kind: "photo", FileSize: 0}
	got := BuildPrompt("/tmp/x.jpg", meta, "x")
	if !strings.Contains(got, "File size: 0 bytes") {
		t.Errorf("expected 'File size: 0 bytes' line, got:\n%s", got)
	}
}

func TestBuildPrompt_CapitalizedKind(t *testing.T) {
	for _, kind := range []string{"Photo", "Document", "Audio", "Voice", "Video"} {
		t.Run(kind, func(t *testing.T) {
			meta := MediaMetadata{Kind: kind, FileSize: 100}
			got := BuildPrompt("/tmp/x", meta, "hi")
			want := "File type: " + kind
			if !strings.Contains(got, want) {
				t.Errorf("expected prompt to contain %q, got:\n%s", want, got)
			}
		})
	}
}

func TestExtractFileRef_Photo(t *testing.T) {
	msg := &models.Message{
		Photo: []models.PhotoSize{
			{FileID: "small", Width: 100, Height: 100, FileSize: 1000},
			{FileID: "large", Width: 800, Height: 600, FileSize: 5000},
		},
	}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for photo message")
	}
	if fileID != "large" {
		t.Errorf("fileID = %q, want %q (largest)", fileID, "large")
	}
	if fileSize != 5000 {
		t.Errorf("fileSize = %d, want %d", fileSize, 5000)
	}
	if mimeType != "" {
		t.Errorf("mimeType = %q, want empty for photo", mimeType)
	}
	if originalName != "" {
		t.Errorf("originalName = %q, want empty for photo", originalName)
	}
	if mediaType != MediaTypePhoto {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypePhoto)
	}
	if ext != ".jpg" {
		t.Errorf("ext = %q, want %q", ext, ".jpg")
	}
}

func TestExtractFileRef_Audio(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{
			FileID:    "aud",
			FileSize:  1000,
			MimeType:  "audio/mpeg",
			FileName:  "song.mp3",
		},
	}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for audio message")
	}
	if fileID != "aud" {
		t.Errorf("fileID = %q, want %q", fileID, "aud")
	}
	if fileSize != 1000 {
		t.Errorf("fileSize = %d, want %d", fileSize, 1000)
	}
	if mimeType != "audio/mpeg" {
		t.Errorf("mimeType = %q, want %q", mimeType, "audio/mpeg")
	}
	if originalName != "song.mp3" {
		t.Errorf("originalName = %q, want %q", originalName, "song.mp3")
	}
	if mediaType != MediaTypeAudio {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypeAudio)
	}
	if ext != ".mp3" {
		t.Errorf("ext = %q, want %q", ext, ".mp3")
	}
}

func TestExtractFileRef_Voice(t *testing.T) {
	msg := &models.Message{
		Voice: &models.Voice{
			FileID:   "vox",
			FileSize: 500,
			MimeType: "audio/ogg",
		},
	}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for voice message")
	}
	if fileID != "vox" {
		t.Errorf("fileID = %q, want %q", fileID, "vox")
	}
	if fileSize != 500 {
		t.Errorf("fileSize = %d, want %d", fileSize, 500)
	}
	if mimeType != "audio/ogg" {
		t.Errorf("mimeType = %q, want %q", mimeType, "audio/ogg")
	}
	if originalName != "" {
		t.Errorf("originalName = %q, want empty for voice (no FileName field)", originalName)
	}
	if mediaType != MediaTypeVoice {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypeVoice)
	}
	if ext != ".ogg" {
		t.Errorf("ext = %q, want %q", ext, ".ogg")
	}
}

func TestExtractFileRef_Document(t *testing.T) {
	msg := &models.Message{
		Document: &models.Document{
			FileID:    "doc",
			FileSize:  2000,
			MimeType:  "application/pdf",
			FileName:  "report.pdf",
		},
	}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for document message")
	}
	if fileID != "doc" {
		t.Errorf("fileID = %q, want %q", fileID, "doc")
	}
	if fileSize != 2000 {
		t.Errorf("fileSize = %d, want %d", fileSize, 2000)
	}
	if mimeType != "application/pdf" {
		t.Errorf("mimeType = %q, want %q", mimeType, "application/pdf")
	}
	if originalName != "report.pdf" {
		t.Errorf("originalName = %q, want %q", originalName, "report.pdf")
	}
	if mediaType != MediaTypeDocument {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypeDocument)
	}
	if ext != ".pdf" {
		t.Errorf("ext = %q, want %q", ext, ".pdf")
	}
}

func TestExtractFileRef_DocumentNoName(t *testing.T) {
	msg := &models.Message{
		Document: &models.Document{
			FileID: "doc",
		},
	}
	fileID, _, _, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for document message")
	}
	if fileID != "doc" {
		t.Errorf("fileID = %q, want %q", fileID, "doc")
	}
	if originalName != "" {
		t.Errorf("originalName = %q, want empty", originalName)
	}
	if mediaType != MediaTypeDocument {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypeDocument)
	}
	if ext != ".bin" {
		t.Errorf("ext = %q, want %q", ext, ".bin")
	}
}

func TestExtractFileRef_Video(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{
			FileID:   "vid",
			FileSize: 5000000,
			MimeType: "video/mp4",
			FileName: "clip.mp4",
		},
	}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for video message")
	}
	if fileID != "vid" {
		t.Errorf("fileID = %q, want %q", fileID, "vid")
	}
	if fileSize != 5000000 {
		t.Errorf("fileSize = %d, want %d", fileSize, 5000000)
	}
	if mimeType != "video/mp4" {
		t.Errorf("mimeType = %q, want %q", mimeType, "video/mp4")
	}
	if originalName != "clip.mp4" {
		t.Errorf("originalName = %q, want %q", originalName, "clip.mp4")
	}
	if mediaType != MediaTypeVideo {
		t.Errorf("mediaType = %q, want %q", mediaType, MediaTypeVideo)
	}
	if ext != ".mp4" {
		t.Errorf("ext = %q, want %q", ext, ".mp4")
	}
}

func TestExtractFileRef_AudioWithName(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{
			FileID:   "aud",
			FileSize: 4096,
			MimeType: "audio/flac",
			FileName: "song.flac",
		},
	}
	fileID, _, _, originalName, _, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for audio message")
	}
	if fileID != "aud" {
		t.Errorf("fileID = %q, want %q", fileID, "aud")
	}
	if originalName != "song.flac" {
		t.Errorf("originalName = %q, want %q", originalName, "song.flac")
	}
	if ext != ".flac" {
		t.Errorf("ext = %q, want %q (derived from FileName)", ext, ".flac")
	}
}

func TestExtractFileRef_VideoWithName(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{
			FileID:   "vid",
			FileSize: 8192,
			MimeType: "video/quicktime",
			FileName: "clip.mov",
		},
	}
	fileID, _, _, originalName, _, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for video message")
	}
	if fileID != "vid" {
		t.Errorf("fileID = %q, want %q", fileID, "vid")
	}
	if originalName != "clip.mov" {
		t.Errorf("originalName = %q, want %q", originalName, "clip.mov")
	}
	if ext != ".mov" {
		t.Errorf("ext = %q, want %q (derived from FileName)", ext, ".mov")
	}
}

func TestExtractFileRef_AudioNoName(t *testing.T) {
	msg := &models.Message{
		Audio: &models.Audio{FileID: "aud"},
	}
	_, _, _, _, _, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for audio message")
	}
	if ext != ".mp3" {
		t.Errorf("ext = %q, want %q (fallback when FileName is empty)", ext, ".mp3")
	}
}

func TestExtractFileRef_VideoNoName(t *testing.T) {
	msg := &models.Message{
		Video: &models.Video{FileID: "vid"},
	}
	_, _, _, _, _, ext, ok := ExtractFileRef(msg)
	if !ok {
		t.Fatal("expected ok=true for video message")
	}
	if ext != ".mp4" {
		t.Errorf("ext = %q, want %q (fallback when FileName is empty)", ext, ".mp4")
	}
}

func TestExtractFileRef_NoMedia(t *testing.T) {
	msg := &models.Message{}
	fileID, fileSize, mimeType, originalName, mediaType, ext, ok := ExtractFileRef(msg)
	if ok {
		t.Fatal("expected ok=false for empty message")
	}
	if fileID != "" || fileSize != 0 || mimeType != "" || originalName != "" || mediaType != "" || ext != "" {
		t.Errorf("expected all zero values, got fileID=%q fileSize=%d mimeType=%q originalName=%q mediaType=%q ext=%q",
			fileID, fileSize, mimeType, originalName, mediaType, ext)
	}
}
