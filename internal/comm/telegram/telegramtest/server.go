// Package telegramtest provides an in-process Telegram Bot API emulator for
// regression tests. It implements the small subset of the Bot API the Acolyte
// Telegram adapter uses (getMe, getUpdates, sendMessage, getFile and file
// downloads) over a real HTTP server, so tests exercise the production bot
// client, polling, retries and media downloads without touching Telegram.
package telegramtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
)

// DefaultToken is the token every emulator accepts unless overridden.
const DefaultToken = "12345:acolyte-telegram-test"

const maxPollWait = time.Second

// SentMessage is one message accepted by sendMessage.
type SentMessage struct {
	ID      int64
	ChatID  int64
	Text    string
	ReplyTo *int64
}

// Failure scripts one method call to fail instead of succeeding.
type Failure struct {
	Status      int
	Description string
}

// Server is an in-process Telegram Bot API replacement.
type Server struct {
	// URL is the base URL to pass to telegram.Options.ServerURL.
	URL string
	// Token is the bot token the emulator accepts.
	Token string

	http *httptest.Server

	mu            sync.Mutex
	updates       []models.Update
	updateNotify  chan struct{}
	nextUpdateID  int64
	nextMessageID int64
	messages      []SentMessage
	messageNotify chan struct{}
	files         map[string]fileEntry
	filesByPath   map[string][]byte
	failures      map[string][]Failure
	failOnCall    map[string]map[int]Failure
	calls         map[string]int
}

type fileEntry struct {
	path string
	data []byte
}

// New starts an emulator with a random local listener.
func New() *Server {
	s := &Server{
		Token:         DefaultToken,
		updateNotify:  make(chan struct{}),
		messageNotify: make(chan struct{}),
		files:         map[string]fileEntry{},
		filesByPath:   map[string][]byte{},
		failures:      map[string][]Failure{},
		failOnCall:    map[string]map[int]Failure{},
		calls:         map[string]int{},
	}
	s.http = httptest.NewServer(http.HandlerFunc(s.handle))
	s.URL = s.http.URL
	return s
}

// Close shuts down the emulator.
func (s *Server) Close() {
	if s != nil && s.http != nil {
		s.http.Close()
	}
}

// SendUpdate queues an update for the next getUpdates poll. A zero ID is
// replaced with the next emulator update ID. The stored update is returned so
// callers can reference the generated IDs.
func (s *Server) SendUpdate(update models.Update) models.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.ID == 0 {
		s.nextUpdateID++
		update.ID = s.nextUpdateID
	} else if update.ID > s.nextUpdateID {
		s.nextUpdateID = update.ID
	}
	if update.Message != nil && update.Message.ID == 0 {
		s.nextMessageID++
		update.Message.ID = int(s.nextMessageID)
	}
	s.updates = append(s.updates, update)
	s.notifyUpdatesLocked()
	return update
}

// SendText queues a private text message from username and returns the update.
func (s *Server) SendText(chatID, userID int64, username, text string) models.Update {
	if chatID == 0 {
		chatID = userID
	}
	return s.SendUpdate(models.Update{Message: &models.Message{
		Date: int(time.Now().Unix()),
		From: &models.User{ID: userID, Username: username, FirstName: username},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate, Username: username},
		Text: text,
	}})
}

// SendPhoto queues a private photo message and registers the file for
// download. Photo files are always reported as .jpg, matching Telegram.
func (s *Server) SendPhoto(chatID, userID int64, username, caption string, data []byte) models.Update {
	if chatID == 0 {
		chatID = userID
	}
	fileID := fmt.Sprintf("photo-%d", time.Now().UnixNano())
	s.AddFile(fileID, fmt.Sprintf("photos/%s.jpg", fileID), data)
	return s.SendUpdate(models.Update{Message: &models.Message{
		Date:    int(time.Now().Unix()),
		From:    &models.User{ID: userID, Username: username, FirstName: username},
		Chat:    models.Chat{ID: chatID, Type: models.ChatTypePrivate, Username: username},
		Caption: caption,
		Photo:   []models.PhotoSize{{FileID: fileID, FileUniqueID: fileID, FileSize: len(data)}},
	}})
}

// AddFile registers a downloadable file returned by getFile.
func (s *Server) AddFile(fileID, filePath string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[fileID] = fileEntry{path: strings.TrimLeft(filePath, "/"), data: append([]byte(nil), data...)}
	s.filesByPath[strings.TrimLeft(filePath, "/")] = append([]byte(nil), data...)
}

// FailNext makes the next call to method fail with a Telegram error response.
func (s *Server) FailNext(method string, status int, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[method] = append(s.failures[method], Failure{Status: status, Description: description})
}

// FailOnCall makes the call-th (1-based) request to method fail. This is used
// to exercise progress resumption, for example a failure between text chunks.
func (s *Server) FailOnCall(method string, call int, status int, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOnCall[method] == nil {
		s.failOnCall[method] = map[int]Failure{}
	}
	s.failOnCall[method][call] = Failure{Status: status, Description: description}
}

// Messages returns every accepted sendMessage call in order.
func (s *Server) Messages() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SentMessage(nil), s.messages...)
}

// MessagesFor returns accepted messages for one chat in order.
func (s *Server) MessagesFor(chatID int64) []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SentMessage
	for _, message := range s.messages {
		if message.ChatID == chatID {
			out = append(out, message)
		}
	}
	return out
}

// WaitForMessages waits until at least want sendMessage calls have been
// accepted. It returns a snapshot of all accepted messages.
func (s *Server) WaitForMessages(ctx context.Context, want int) ([]SentMessage, error) {
	for {
		s.mu.Lock()
		if len(s.messages) >= want {
			out := append([]SentMessage(nil), s.messages...)
			s.mu.Unlock()
			return out, nil
		}
		notify := s.messageNotify
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("telegramtest: waiting for %d message(s): %w", want, ctx.Err())
		case <-notify:
		}
	}
}

// WaitForChatMessages is WaitForMessages scoped to one chat.
func (s *Server) WaitForChatMessages(ctx context.Context, chatID int64, want int) ([]SentMessage, error) {
	for {
		s.mu.Lock()
		var out []SentMessage
		for _, message := range s.messages {
			if message.ChatID == chatID {
				out = append(out, message)
			}
		}
		if len(out) >= want {
			s.mu.Unlock()
			return out, nil
		}
		notify := s.messageNotify
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("telegramtest: waiting for %d message(s) in chat %d: %w", want, chatID, ctx.Err())
		case <-notify:
		}
	}
}

// Calls reports how many times a Bot API method was requested.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// WaitForCalls waits until a Bot API method has been requested want times.
func (s *Server) WaitForCalls(ctx context.Context, method string, want int) error {
	for {
		if s.Calls(method) >= want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("telegramtest: waiting for %d %s call(s): %w", want, method, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(path, "file/bot") {
		s.handleFile(w, r)
		return
	}
	if !strings.HasPrefix(path, "bot") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "bot")
	token, method, ok := strings.Cut(rest, "/")
	if !ok || method == "" {
		http.NotFound(w, r)
		return
	}
	if token != s.Token {
		writeError(w, http.StatusUnauthorized, "Unauthorized: invalid bot token")
		return
	}

	form, err := multipartForm(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}

	s.mu.Lock()
	s.calls[method]++
	call := s.calls[method]
	failure, hasFailure := s.failOnCall[method][call]
	if hasFailure {
		delete(s.failOnCall[method], call)
	} else {
		failure, hasFailure = s.popFailureLocked(method)
	}
	s.mu.Unlock()
	if hasFailure {
		writeError(w, failure.Status, failure.Description)
		return
	}

	switch method {
	case "getMe":
		writeOK(w, models.User{ID: 12345, IsBot: true, FirstName: "Acolyte", Username: "acolyte_test_bot"})
	case "getUpdates":
		s.handleGetUpdates(w, r, form)
	case "sendMessage":
		s.handleSendMessage(w, form)
	case "getFile":
		s.handleGetFile(w, form)
	default:
		writeError(w, http.StatusNotFound, "Not Found: method "+method)
	}
}

func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request, form map[string][]string) {
	offset := int64(formInt(form, "offset"))
	timeout := time.Duration(formInt(form, "timeout")) * time.Second
	// Cap the wait so shutdown never blocks on a production-sized long poll.
	if timeout <= 0 || timeout > maxPollWait {
		timeout = maxPollWait
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		if offset > 0 {
			// Telegram confirms (and forgets) updates below offset.
			cut := 0
			for cut < len(s.updates) && s.updates[cut].ID < offset {
				cut++
			}
			if cut > 0 {
				s.updates = append([]models.Update(nil), s.updates[cut:]...)
			}
		}
		var pending []models.Update
		for _, update := range s.updates {
			if offset == 0 || update.ID >= offset {
				pending = append(pending, update)
			}
		}
		notify := s.updateNotify
		s.mu.Unlock()
		if len(pending) > 0 {
			writeOK(w, pending)
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeOK(w, []models.Update{})
			return
		case <-notify:
		}
	}
}

func (s *Server) handleSendMessage(w http.ResponseWriter, form map[string][]string) {
	chatID, err := parseChatID(formString(form, "chat_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Bad Request: chat not found")
		return
	}
	text := formString(form, "text")
	if text == "" {
		writeError(w, http.StatusBadRequest, "Bad Request: message text is empty")
		return
	}
	var replyTo *int64
	if raw := formString(form, "reply_parameters"); raw != "" {
		var params models.ReplyParameters
		if err := json.Unmarshal([]byte(raw), &params); err == nil && params.MessageID != 0 {
			id := int64(params.MessageID)
			replyTo = &id
		}
	}

	s.mu.Lock()
	s.nextMessageID++
	id := s.nextMessageID
	s.messages = append(s.messages, SentMessage{ID: id, ChatID: chatID, Text: text, ReplyTo: replyTo})
	s.notifyMessagesLocked()
	s.mu.Unlock()

	writeOK(w, models.Message{
		ID:   int(id),
		Date: int(time.Now().Unix()),
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
	})
}

func (s *Server) handleGetFile(w http.ResponseWriter, form map[string][]string) {
	fileID := formString(form, "file_id")
	s.mu.Lock()
	file, ok := s.files[fileID]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "Bad Request: file not found")
		return
	}
	writeOK(w, models.File{FileID: fileID, FileUniqueID: fileID, FileSize: int64(len(file.data)), FilePath: file.path})
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/file/bot")
	token, filePath, ok := strings.Cut(rest, "/")
	if !ok || token != s.Token {
		http.NotFound(w, r)
		return
	}
	decoded, err := url.PathUnescape(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	data, ok := s.filesByPath[decoded]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) popFailureLocked(method string) (Failure, bool) {
	queue := s.failures[method]
	if len(queue) == 0 {
		return Failure{}, false
	}
	failure := queue[0]
	s.failures[method] = queue[1:]
	return failure, true
}

func (s *Server) notifyUpdatesLocked() {
	close(s.updateNotify)
	s.updateNotify = make(chan struct{})
}

func (s *Server) notifyMessagesLocked() {
	close(s.messageNotify)
	s.messageNotify = make(chan struct{})
}

func multipartForm(r *http.Request) (map[string][]string, error) {
	values := map[string][]string{}
	reader, err := r.MultipartReader()
	if err != nil {
		if errors.Is(err, http.ErrNotMultipart) {
			return values, nil
		}
		return nil, err
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return values, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		if name := part.FormName(); name != "" {
			values[name] = append(values[name], string(data))
		}
	}
}

func formString(form map[string][]string, key string) string {
	if values := form[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func formInt(form map[string][]string, key string) int {
	raw := strings.TrimSpace(formString(form, key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func parseChatID(raw string) (int64, error) {
	return strconv.ParseInt(strings.Trim(strings.TrimSpace(raw), `"`), 10, 64)
}

func writeOK(w http.ResponseWriter, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeEnvelope(w, http.StatusOK, map[string]any{"ok": true, "result": json.RawMessage(raw)})
}

func writeError(w http.ResponseWriter, status int, description string) {
	writeEnvelope(w, status, map[string]any{
		"ok":          false,
		"error_code":  status,
		"description": description,
	})
}

func writeEnvelope(w http.ResponseWriter, status int, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
