package board

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Actor kinds and identifiers.
type ActorKind string

const (
	ActorUser  ActorKind = "user"
	ActorAgent ActorKind = "agent"
)

// UserActorID is the single local user.
const UserActorID = "user"

const (
	PostIDPrefix  = "brd_"
	EntryIDPrefix = "ent_"

	MaxFilesPerEntry      = 20
	MaxTagsPerPost        = 10
	MaxTagRunes           = 32
	MaxTitleRunes         = 80
	MaxExplicitTitleRunes = 200
	MaxEntryRunes         = 200_000
)

// Actor is the author or recipient of a board entry.
type Actor struct {
	Kind ActorKind `json:"kind"`
	ID   string    `json:"id"`
}

// UserActor returns the local user actor.
func UserActor() Actor { return Actor{Kind: ActorUser, ID: UserActorID} }

// AgentActor returns an agent actor for a registered agent name.
func AgentActor(name string) Actor { return Actor{Kind: ActorAgent, ID: strings.TrimSpace(name)} }

// Validate checks the actor identity.
func (a Actor) Validate() error {
	switch a.Kind {
	case ActorUser:
		if a.ID != UserActorID {
			return fmt.Errorf("%w: user id must be %q", ErrInvalid, UserActorID)
		}
	case ActorAgent:
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("%w: agent id is required", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: invalid actor kind %q", ErrInvalid, a.Kind)
	}
	return nil
}

// IsUser reports whether the actor is the local user.
func (a Actor) IsUser() bool { return a.Kind == ActorUser }

// IsAgent reports whether the actor is an agent.
func (a Actor) IsAgent() bool { return a.Kind == ActorAgent }

// String renders the actor as kind:id.
func (a Actor) String() string { return string(a.Kind) + ":" + a.ID }

// EntryKind distinguishes the opening post from its messages.
type EntryKind string

const (
	EntryPost    EntryKind = "post"
	EntryMessage EntryKind = "message"
)

// FileMode selects whether an attachment is referenced in place or copied.
type FileMode string

const (
	FileReference FileMode = "reference"
	FileCopy      FileMode = "copy"
)

// File is one attachment on an entry.
type File struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Mode       FileMode `json:"mode"`
	SourcePath string   `json:"source_path,omitempty"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256,omitempty"`
}

// Post is one board conversation: an opening post plus ordered messages.
type Post struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedBy    Actor     `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Tags         []string  `json:"tags,omitempty"`
	Participants []Actor   `json:"participants,omitempty"`
}

// Entry is the opening post (seq 0) or one message (seq 1..n).
type Entry struct {
	ID         string    `json:"id"`
	EnvelopeID string    `json:"envelope_id,omitempty"`
	PostID     string    `json:"post_id"`
	Seq        int       `json:"seq"`
	Kind       EntryKind `json:"kind"`
	Author     Actor     `json:"author"`
	Text       string    `json:"text"`
	ReplyToID  string    `json:"reply_to_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Files      []File    `json:"files,omitempty"`
}

// Participant is one agent or the user in a post.
type Participant struct {
	Actor
	AddedAt time.Time `json:"added_at"`
	AddedBy Actor     `json:"added_by"`
}

// Subscription controls whether an agent gets board inbox pings.
type Subscription struct {
	AgentID   string    `json:"agent_id"`
	PostID    string    `json:"post_id,omitempty"` // "" = all posts
	Aware     bool      `json:"aware"`
	CreatedAt time.Time `json:"created_at"`
}

// SubscriptionState is one agent's subscription view.
type SubscriptionState struct {
	AgentID string         `json:"agent_id"`
	All     *Subscription  `json:"all,omitempty"`
	Posts   []Subscription `json:"posts,omitempty"`
	Mutes   []string       `json:"mutes,omitempty"`
}

// Automation is the per-agent, per-post background processing state.
type Automation struct {
	AgentID     string    `json:"agent_id"`
	PostID      string    `json:"post_id"`
	SessionID   string    `json:"session_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastTurnAt  time.Time `json:"last_turn_at,omitempty"`
	Turns       int       `json:"turns"`
	PausedAt    time.Time `json:"paused_at,omitempty"`
	PauseReason string    `json:"pause_reason,omitempty"`
}

// Paused reports whether automation is paused.
func (a *Automation) Paused() bool { return a != nil && !a.PausedAt.IsZero() }

// InboxItem is one queued board ping for an agent.
type InboxItem struct {
	AgentID            string     `json:"agent_id"`
	EntryID            string     `json:"entry_id"`
	PostID             string     `json:"post_id"`
	CreatedAt          time.Time  `json:"created_at"`
	ReadAt             *time.Time `json:"read_at,omitempty"`
	ProcessedAt        *time.Time `json:"processed_at,omitempty"`
	Mode               string     `json:"mode"` // manual | automated
	Aware              bool       `json:"aware"`
	SessionID          string     `json:"session_id,omitempty"`
	MessageID          string     `json:"message_id,omitempty"`
	Attempts           int        `json:"attempts"`
	Error              string     `json:"error,omitempty"`
	ClaimedAt          *time.Time `json:"claimed_at,omitempty"`
	PromotionSessionID string     `json:"promotion_session_id,omitempty"`
}

// InboxView adds display fields from the post and entry.
type InboxView struct {
	InboxItem
	PostTitle   string    `json:"post_title"`
	EntrySeq    int       `json:"entry_seq"`
	EntryKind   EntryKind `json:"entry_kind"`
	EntryText   string    `json:"entry_text"`
	EntryAuthor Actor     `json:"entry_author"`
}

// Alert is a user-facing automation failure/pause notice.
type Alert struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	PostID     string    `json:"post_id"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
	NotifiedAt time.Time `json:"notified_at,omitempty"`
}

// ListFilter narrows a board list query.
type ListFilter struct {
	Tags   []string
	Search string
	Since  *time.Time
	Until  *time.Time
	Unread bool
	Limit  int
}

// CreatePostInput is the opening post payload.
type CreatePostInput struct {
	Title   string
	Text    string
	Files   []File
	With    []string
	Tags    []string
	NoInbox bool
}

// AppendMessageInput is one message payload.
type AppendMessageInput struct {
	Text      string
	Files     []File
	ReplyToID string
}

// PostSummary is one row in a board list.
type PostSummary struct {
	Post
	MessageCount int `json:"message_count"`
	Unread       int `json:"unread"`
}

// PostView is a post plus its entries and the actor's state.
type PostView struct {
	Post
	Entries    []Entry     `json:"entries"`
	Subscribed bool        `json:"subscribed"`
	Aware      bool        `json:"aware"`
	Automation *Automation `json:"automation,omitempty"`
	Unread     int         `json:"unread"`
}

// Delivery is one recipient resolved for an entry.
type Delivery struct {
	AgentID   string `json:"agent_id"`
	Mode      string `json:"mode"` // manual | automated
	Aware     bool   `json:"aware"`
	SessionID string `json:"session_id,omitempty"`
}

func newID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return prefix + hex.EncodeToString(raw[:])
}

// NewClaimToken returns a fresh token for an automation inbox claim.
func NewClaimToken() string { return newID("clm_") }

func deriveTitle(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) > MaxTitleRunes {
		return string(runes[:MaxTitleRunes]) + "…"
	}
	return line
}

func normalizeTags(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	tags := make([]string, 0, len(raw))
	for _, item := range raw {
		tag, err := normalizeTag(item)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > MaxTagsPerPost {
		return nil, fmt.Errorf("%w: at most %d tags per post", ErrInvalid, MaxTagsPerPost)
	}
	return tags, nil
}

func normalizeTag(raw string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if tag == "" {
		return "", fmt.Errorf("%w: empty tag", ErrInvalid)
	}
	if len([]rune(tag)) > MaxTagRunes {
		return "", fmt.Errorf("%w: tag %q is longer than %d characters", ErrInvalid, raw, MaxTagRunes)
	}
	for _, r := range tag {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("%w: tag %q may only contain letters, digits, '.', '_' and '-'", ErrInvalid, raw)
	}
	return tag, nil
}

func validateEntryPayload(text string, files []File) error {
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return fmt.Errorf("%w: text or file is required", ErrInvalid)
	}
	if len([]rune(text)) > MaxEntryRunes {
		return fmt.Errorf("%w: text is too long", ErrInvalid)
	}
	if len(files) > MaxFilesPerEntry {
		return fmt.Errorf("%w: at most %d files per entry", ErrInvalid, MaxFilesPerEntry)
	}
	for _, file := range files {
		if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("%w: file name and path are required", ErrInvalid)
		}
		if !filepath.IsAbs(file.Path) {
			return fmt.Errorf("%w: file path %q must be absolute", ErrInvalid, file.Path)
		}
		switch file.Mode {
		case FileReference, FileCopy:
		default:
			return fmt.Errorf("%w: invalid file mode %q", ErrInvalid, file.Mode)
		}
		if file.Size < 0 {
			return fmt.Errorf("%w: invalid file size", ErrInvalid)
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
