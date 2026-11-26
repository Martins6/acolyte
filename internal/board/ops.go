package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/envelope"
)

// CreatePost creates a board conversation and its opening post atomically.
func (s *Store) CreatePost(ctx context.Context, actor Actor, in CreatePostInput) (Post, Entry, error) {
	if err := actor.Validate(); err != nil {
		return Post{}, Entry{}, err
	}
	text := strings.TrimSpace(in.Text)
	if err := validateEntryPayload(text, in.Files); err != nil {
		return Post{}, Entry{}, err
	}
	tags, err := normalizeTags(in.Tags)
	if err != nil {
		return Post{}, Entry{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = deriveTitle(text)
	} else if len([]rune(title)) > MaxExplicitTitleRunes {
		return Post{}, Entry{}, fmt.Errorf("%w: title is longer than %d characters", ErrInvalid, MaxExplicitTitleRunes)
	}

	participants := make([]Actor, 0, len(in.With)+2)
	seen := make(map[string]struct{}, len(in.With)+2)
	add := func(actor Actor) {
		if _, ok := seen[actor.String()]; ok {
			return
		}
		seen[actor.String()] = struct{}{}
		participants = append(participants, actor)
	}
	add(actor)
	add(UserActor())
	for _, raw := range in.With {
		name := strings.TrimSpace(raw)
		if name == "" || name == UserActorID {
			continue
		}
		add(AgentActor(name))
	}

	now := s.now()
	post := Post{
		ID:           newID(PostIDPrefix),
		Title:        title,
		CreatedBy:    actor,
		CreatedAt:    now,
		UpdatedAt:    now,
		Tags:         tags,
		Participants: participants,
	}
	entry := Entry{
		ID:         newID(EntryIDPrefix),
		EnvelopeID: envelope.NewID(),
		PostID:     post.ID,
		Seq:        0,
		Kind:       EntryPost,
		Author:     actor,
		Text:       text,
		CreatedAt:  now,
		Files:      in.Files,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Post{}, Entry{}, fmt.Errorf("board: begin create post: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO brd_posts (id, title, created_by_kind, created_by_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		post.ID, post.Title, string(actor.Kind), actor.ID, post.CreatedAt, post.UpdatedAt,
	); err != nil {
		return Post{}, Entry{}, fmt.Errorf("board: insert post: %w", err)
	}
	if err := insertEntry(ctx, tx, entry); err != nil {
		return Post{}, Entry{}, err
	}
	for _, participant := range participants {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO brd_participants
			 (post_id, kind, id, added_at, added_by_kind, added_by_id)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			post.ID, string(participant.Kind), participant.ID, now, string(actor.Kind), actor.ID,
		); err != nil {
			return Post{}, Entry{}, fmt.Errorf("board: insert participant: %w", err)
		}
	}
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO brd_tags (post_id, tag) VALUES (?, ?)`, post.ID, tag,
		); err != nil {
			return Post{}, Entry{}, fmt.Errorf("board: insert tag: %w", err)
		}
	}
	if actor.IsAgent() {
		if in.NoInbox {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO brd_mutes (agent_id, post_id, created_at) VALUES (?, ?, ?)`,
				actor.ID, post.ID, now,
			); err != nil {
				return Post{}, Entry{}, fmt.Errorf("board: mute creator: %w", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO brd_subscriptions (agent_id, post_id, aware, created_at)
				 VALUES (?, ?, 1, ?)
				 ON CONFLICT(agent_id, post_id) DO UPDATE SET aware = 1`,
				actor.ID, post.ID, now,
			); err != nil {
				return Post{}, Entry{}, fmt.Errorf("board: subscribe creator: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return Post{}, Entry{}, fmt.Errorf("board: commit create post: %w", err)
	}
	return post, entry, nil
}

// AppendMessage adds an ordered message to a post.
func (s *Store) AppendMessage(ctx context.Context, actor Actor, postRef string, in AppendMessageInput) (Entry, error) {
	if err := actor.Validate(); err != nil {
		return Entry{}, err
	}
	text := strings.TrimSpace(in.Text)
	if err := validateEntryPayload(text, in.Files); err != nil {
		return Entry{}, err
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return Entry{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("board: begin append message: %w", err)
	}
	defer tx.Rollback()

	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return Entry{}, err
	}

	replyTo := strings.TrimSpace(in.ReplyToID)
	if replyTo != "" {
		target, err := resolveEntryTx(ctx, tx, post.ID, replyTo)
		if err != nil {
			return Entry{}, err
		}
		replyTo = target.ID
	}

	var maxSeq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM brd_entries WHERE post_id = ?`, post.ID,
	).Scan(&maxSeq); err != nil {
		return Entry{}, fmt.Errorf("board: read message seq: %w", err)
	}

	now := s.now()
	entry := Entry{
		ID:         newID(EntryIDPrefix),
		EnvelopeID: envelope.NewID(),
		PostID:     post.ID,
		Seq:        maxSeq + 1,
		Kind:       EntryMessage,
		Author:     actor,
		Text:       text,
		ReplyToID:  replyTo,
		CreatedAt:  now,
		Files:      in.Files,
	}
	if err := insertEntry(ctx, tx, entry); err != nil {
		return Entry{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE brd_posts SET updated_at = ? WHERE id = ?`, now, post.ID,
	); err != nil {
		return Entry{}, fmt.Errorf("board: touch post: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("board: commit append message: %w", err)
	}
	return entry, nil
}

func insertEntry(ctx context.Context, q queryer, entry Entry) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO brd_entries
		 (id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.EnvelopeID, entry.PostID, entry.Seq, string(entry.Kind),
		string(entry.Author.Kind), entry.Author.ID, entry.Text, entry.ReplyToID, entry.CreatedAt,
	); err != nil {
		return fmt.Errorf("board: insert entry: %w", err)
	}
	for i, file := range entry.Files {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO brd_files
			 (entry_id, seq, name, path, mode, source_path, size, sha256)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.ID, i, file.Name, file.Path, string(file.Mode), file.SourcePath, file.Size, file.SHA256,
		); err != nil {
			return fmt.Errorf("board: insert file: %w", err)
		}
	}
	return nil
}

func resolveEntryTx(ctx context.Context, q queryer, postID, ref string) (Entry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Entry{}, fmt.Errorf("%w: empty entry reference", ErrInvalid)
	}
	if strings.HasPrefix(ref, "#") {
		seq, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
		if err != nil || seq < 0 {
			return Entry{}, fmt.Errorf("%w: invalid entry reference %q", ErrInvalid, ref)
		}
		return getEntryBySeqTx(ctx, q, postID, seq)
	}
	entry, err := getEntryTx(ctx, q, postID, ref)
	if err == nil {
		return entry, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Entry{}, err
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM brd_entries WHERE post_id = ? AND id LIKE ? ESCAPE '\' ORDER BY id LIMIT 5`,
		postID, escapeLike(ref)+"%")
	if err != nil {
		return Entry{}, fmt.Errorf("board: resolve entry %q: %w", ref, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Entry{}, fmt.Errorf("board: resolve entry %q: %w", ref, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return Entry{}, err
	}
	switch len(ids) {
	case 0:
		return Entry{}, fmt.Errorf("%w: entry %q", ErrNotFound, ref)
	case 1:
		return getEntryTx(ctx, q, postID, ids[0])
	default:
		return Entry{}, fmt.Errorf("%w: entry prefix %q matches %d entries", ErrAmbiguous, ref, len(ids))
	}
}

func getEntryTx(ctx context.Context, q queryer, postID, id string) (Entry, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at
		 FROM brd_entries WHERE post_id = ? AND id = ?`, postID, id)
	entry, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: entry %q", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("board: load entry %q: %w", id, err)
	}
	return entry, nil
}

func getEntryBySeqTx(ctx context.Context, q queryer, postID string, seq int) (Entry, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at
		 FROM brd_entries WHERE post_id = ? AND seq = ?`, postID, seq)
	entry, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: entry #%d", ErrNotFound, seq)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("board: load entry #%d: %w", seq, err)
	}
	return entry, nil
}

// Show returns a post, its entries, and the actor's state.
func (s *Store) Show(ctx context.Context, actor Actor, ref string) (PostView, error) {
	post, err := s.ResolvePost(ctx, ref)
	if err != nil {
		return PostView{}, err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return PostView{}, err
	}
	entries, err := s.loadEntries(ctx, s.db, post.ID)
	if err != nil {
		return PostView{}, err
	}
	unread, err := s.unreadCount(ctx, s.db, post.ID, actor)
	if err != nil {
		return PostView{}, err
	}
	view := PostView{Post: post, Entries: entries, Unread: unread}
	if actor.IsAgent() {
		sub, ok, err := s.subscriptionFor(ctx, s.db, actor.ID, post.ID)
		if err != nil {
			return PostView{}, err
		}
		muted, err := s.muted(ctx, s.db, actor.ID, post.ID)
		if err != nil {
			return PostView{}, err
		}
		view.Subscribed = ok && !muted
		view.Aware = view.Subscribed && sub.Aware
		if view.Automation, err = s.getAutomation(ctx, s.db, actor.ID, post.ID); err != nil {
			return PostView{}, err
		}
	}
	return view, nil
}

// Invite adds agents to a post and returns the newly added names.
func (s *Store) Invite(ctx context.Context, actor Actor, postRef string, agents []string) ([]string, error) {
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("board: begin invite: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return nil, err
	}

	now := s.now()
	var added []string
	for _, raw := range agents {
		name := strings.TrimSpace(raw)
		if name == "" || name == UserActorID {
			continue
		}
		result, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO brd_participants
			 (post_id, kind, id, added_at, added_by_kind, added_by_id)
			 VALUES (?, 'agent', ?, ?, ?, ?)`,
			post.ID, name, now, string(actor.Kind), actor.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("board: invite %q: %w", name, err)
		}
		if count, err := result.RowsAffected(); err == nil && count > 0 {
			added = append(added, name)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("board: commit invite: %w", err)
	}
	return added, nil
}

// Leave removes an agent from a post. The user cannot leave.
func (s *Store) Leave(ctx context.Context, actor Actor, postRef string) error {
	if actor.IsUser() {
		return fmt.Errorf("%w: the user is always a participant", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin leave: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM brd_inbox WHERE agent_id = ? AND post_id = ?`,
		`DELETE FROM brd_subscriptions WHERE agent_id = ? AND post_id = ?`,
		`DELETE FROM brd_mutes WHERE agent_id = ? AND post_id = ?`,
		`DELETE FROM brd_automations WHERE agent_id = ? AND post_id = ?`,
		`DELETE FROM brd_alerts WHERE agent_id = ? AND post_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, actor.ID, post.ID); err != nil {
			return fmt.Errorf("board: clear participant state: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_reads WHERE post_id = ? AND kind = 'agent' AND id = ?`,
		post.ID, actor.ID,
	); err != nil {
		return fmt.Errorf("board: clear read state: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_participants WHERE post_id = ? AND kind = 'agent' AND id = ?`,
		post.ID, actor.ID,
	); err != nil {
		return fmt.Errorf("board: leave post: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit leave: %w", err)
	}
	return nil
}

// AddTags adds tags to a post and returns the full tag list.
func (s *Store) AddTags(ctx context.Context, actor Actor, postRef string, tags []string) ([]string, error) {
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return nil, err
	}
	add, err := normalizeTags(tags)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("board: begin add tags: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return nil, err
	}
	current, err := s.loadTags(ctx, tx, post.ID)
	if err != nil {
		return nil, err
	}
	merged := append([]string{}, current...)
	seen := make(map[string]struct{}, len(merged))
	for _, tag := range merged {
		seen[tag] = struct{}{}
	}
	for _, tag := range add {
		if _, ok := seen[tag]; ok {
			continue
		}
		merged = append(merged, tag)
		seen[tag] = struct{}{}
	}
	if len(merged) > MaxTagsPerPost {
		return nil, fmt.Errorf("%w: at most %d tags per post", ErrInvalid, MaxTagsPerPost)
	}
	for _, tag := range add {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO brd_tags (post_id, tag) VALUES (?, ?)`, post.ID, tag,
		); err != nil {
			return nil, fmt.Errorf("board: add tag: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("board: commit add tags: %w", err)
	}
	return merged, nil
}

// RemoveTags removes tags from a post and returns the remaining tags.
func (s *Store) RemoveTags(ctx context.Context, actor Actor, postRef string, tags []string) ([]string, error) {
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return nil, err
	}
	remove, err := normalizeTags(tags)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("board: begin remove tags: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return nil, err
	}
	for _, tag := range remove {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM brd_tags WHERE post_id = ? AND tag = ?`, post.ID, tag,
		); err != nil {
			return nil, fmt.Errorf("board: remove tag: %w", err)
		}
	}
	remaining, err := s.loadTags(ctx, tx, post.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("board: commit remove tags: %w", err)
	}
	return remaining, nil
}

// MarkRead records the highest entry seq the actor has seen.
func (s *Store) MarkRead(ctx context.Context, actor Actor, postRef string, seq int) error {
	if seq < 0 {
		return fmt.Errorf("%w: seq must be >= 0", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return err
	}
	var maxSeq int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM brd_entries WHERE post_id = ?`, post.ID,
	).Scan(&maxSeq); err != nil {
		return fmt.Errorf("board: read max seq: %w", err)
	}
	if seq > maxSeq {
		seq = maxSeq
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO brd_reads (post_id, kind, id, last_read_seq)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(post_id, kind, id)
		 DO UPDATE SET last_read_seq = MAX(last_read_seq, excluded.last_read_seq)`,
		post.ID, string(actor.Kind), actor.ID, seq,
	); err != nil {
		return fmt.Errorf("board: mark read: %w", err)
	}
	return nil
}

// Subscribe opts an agent into a post's board inbox.
func (s *Store) Subscribe(ctx context.Context, actor Actor, postRef string, aware bool) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents subscribe", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin subscribe: %w", err)
	}
	defer tx.Rollback()
	if err := upsertSubscription(ctx, tx, actor.ID, post.ID, aware, s.now()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_mutes WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear mute: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit subscribe: %w", err)
	}
	return nil
}

// SubscribeAll opts an agent into every post it is invited to.
func (s *Store) SubscribeAll(ctx context.Context, actor Actor, aware bool) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents subscribe", ErrInvalid)
	}
	if err := upsertSubscription(ctx, s.db, actor.ID, "", aware, s.now()); err != nil {
		return err
	}
	return nil
}

// Unsubscribe removes a post subscription and mutes the post.
func (s *Store) Unsubscribe(ctx context.Context, actor Actor, postRef string) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents subscribe", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin unsubscribe: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_subscriptions WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: delete subscription: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO brd_mutes (agent_id, post_id, created_at) VALUES (?, ?, ?)`,
		actor.ID, post.ID, s.now(),
	); err != nil {
		return fmt.Errorf("board: mute post: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_automations WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear automation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit unsubscribe: %w", err)
	}
	return nil
}

// UnsubscribeAll removes every subscription for an agent. Mutes are kept.
func (s *Store) UnsubscribeAll(ctx context.Context, actor Actor) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents subscribe", ErrInvalid)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM brd_subscriptions WHERE agent_id = ?`, actor.ID,
	); err != nil {
		return fmt.Errorf("board: drop subscriptions: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM brd_automations WHERE agent_id = ?`, actor.ID,
	); err != nil {
		return fmt.Errorf("board: drop automations: %w", err)
	}
	return nil
}

// Mute turns off board pings and automation for one post.
func (s *Store) Mute(ctx context.Context, actor Actor, postRef string) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents can mute a post", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin mute: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_subscriptions WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: delete subscription: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO brd_mutes (agent_id, post_id, created_at) VALUES (?, ?, ?)`,
		actor.ID, post.ID, s.now(),
	); err != nil {
		return fmt.Errorf("board: mute post: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_automations WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear automation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit mute: %w", err)
	}
	return nil
}

// SetAutomation enables background processing for one agent+post.
func (s *Store) SetAutomation(ctx context.Context, actor Actor, postRef string) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents automate", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	if err := s.requireParticipant(ctx, s.db, post.ID, actor); err != nil {
		return err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin automation: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return err
	}

	var existing SessionState
	var paused sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT session_id, turns, paused_at FROM brd_automations
		 WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	).Scan(&existing.SessionID, &existing.Turns, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO brd_automations
			 (agent_id, post_id, session_id, created_at, updated_at, turns)
			 VALUES (?, ?, '', ?, ?, 0)`,
			actor.ID, post.ID, now, now,
		); err != nil {
			return fmt.Errorf("board: insert automation: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("board: load automation: %w", err)
	} else {
		turns := existing.Turns
		if paused.Valid {
			turns = 0
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE brd_automations
			 SET updated_at = ?, turns = ?, paused_at = NULL, pause_reason = ''
			 WHERE agent_id = ? AND post_id = ?`,
			now, turns, actor.ID, post.ID,
		); err != nil {
			return fmt.Errorf("board: update automation: %w", err)
		}
	}

	if err := upsertSubscription(ctx, tx, actor.ID, post.ID, false, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_mutes WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear mute: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE brd_alerts SET notified_at = ?
		 WHERE agent_id = ? AND post_id = ? AND notified_at IS NULL`,
		now, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear alerts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit automation: %w", err)
	}
	return nil
}

// ClearAutomation stops background processing but keeps the subscription.
func (s *Store) ClearAutomation(ctx context.Context, actor Actor, postRef string) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents automate", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin clear automation: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM brd_automations WHERE agent_id = ? AND post_id = ?`, actor.ID, post.ID,
	); err != nil {
		return fmt.Errorf("board: clear automation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit clear automation: %w", err)
	}
	return nil
}

// ResetAutomation clears the automation session and turn counter.
func (s *Store) ResetAutomation(ctx context.Context, actor Actor, postRef string) error {
	if !actor.IsAgent() {
		return fmt.Errorf("%w: only agents automate", ErrInvalid)
	}
	post, err := s.ResolvePost(ctx, postRef)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: begin reset automation: %w", err)
	}
	defer tx.Rollback()
	if err := s.requireParticipant(ctx, tx, post.ID, actor); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE brd_automations
		 SET session_id = '', turns = 0, paused_at = NULL, pause_reason = '', updated_at = ?
		 WHERE agent_id = ? AND post_id = ?`,
		s.now(), actor.ID, post.ID,
	)
	if err != nil {
		return fmt.Errorf("board: reset automation: %w", err)
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return fmt.Errorf("%w: automation for post %q", ErrNotFound, post.ID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: commit reset automation: %w", err)
	}
	return nil
}

// Automation returns the automation state for an agent+post.
func (s *Store) Automation(ctx context.Context, agentID, postID string) (*Automation, error) {
	return s.getAutomation(ctx, s.db, agentID, postID)
}

// Automations lists an agent's automation state ordered by post.
func (s *Store) Automations(ctx context.Context, agentID string) ([]Automation, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent id is required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, post_id, session_id, created_at, updated_at, last_turn_at,
		        turns, paused_at, pause_reason
		 FROM brd_automations WHERE agent_id = ? ORDER BY post_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("board: list automations: %w", err)
	}
	defer rows.Close()
	var automations []Automation
	for rows.Next() {
		var automation Automation
		var lastTurn, paused sql.NullTime
		if err := rows.Scan(&automation.AgentID, &automation.PostID, &automation.SessionID,
			&automation.CreatedAt, &automation.UpdatedAt, &lastTurn, &automation.Turns,
			&paused, &automation.PauseReason); err != nil {
			return nil, fmt.Errorf("board: list automations: %w", err)
		}
		if lastTurn.Valid {
			automation.LastTurnAt = lastTurn.Time.UTC()
		}
		if paused.Valid {
			automation.PausedAt = paused.Time.UTC()
		}
		automations = append(automations, automation)
	}
	return automations, rows.Err()
}

// Enqueue inserts one board inbox row per subscribed participant and returns them.
func (s *Store) Enqueue(ctx context.Context, entry Entry) ([]Delivery, error) {
	if strings.TrimSpace(entry.PostID) == "" || strings.TrimSpace(entry.ID) == "" {
		return nil, fmt.Errorf("%w: post and entry ids are required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id,
		        (SELECT s.aware FROM brd_subscriptions s WHERE s.agent_id = p.id AND s.post_id = ?),
		        (SELECT s.aware FROM brd_subscriptions s WHERE s.agent_id = p.id AND s.post_id = ''),
		        EXISTS (SELECT 1 FROM brd_automations a
		                WHERE a.agent_id = p.id AND a.post_id = ? AND a.paused_at IS NULL),
		        (SELECT a.session_id FROM brd_automations a
		         WHERE a.agent_id = p.id AND a.post_id = ? AND a.paused_at IS NULL)
		 FROM brd_participants p
		 WHERE p.post_id = ?
		   AND p.kind = 'agent'
		   AND (? <> 'agent' OR p.id <> ?)
		   AND NOT EXISTS (SELECT 1 FROM brd_mutes m WHERE m.agent_id = p.id AND m.post_id = ?)
		 ORDER BY p.id`,
		entry.PostID, entry.PostID, entry.PostID, entry.PostID, string(entry.Author.Kind), entry.Author.ID, entry.PostID)
	if err != nil {
		return nil, fmt.Errorf("board: resolve recipients: %w", err)
	}
	defer rows.Close()

	var deliveries []Delivery
	for rows.Next() {
		var agentID string
		var postAware, allAware sql.NullInt64
		var automated int
		var sessionID sql.NullString
		if err := rows.Scan(&agentID, &postAware, &allAware, &automated, &sessionID); err != nil {
			return nil, fmt.Errorf("board: resolve recipients: %w", err)
		}
		aware := int64(0)
		subscribed := false
		if postAware.Valid {
			aware, subscribed = postAware.Int64, true
		} else if allAware.Valid {
			aware, subscribed = allAware.Int64, true
		}
		if !subscribed {
			continue
		}
		mode := "manual"
		session := ""
		if automated != 0 {
			mode = "automated"
			aware = 0
			if sessionID.Valid {
				session = sessionID.String
			}
		}
		deliveries = append(deliveries, Delivery{
			AgentID:   agentID,
			Mode:      mode,
			Aware:     aware != 0,
			SessionID: session,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("board: begin enqueue: %w", err)
	}
	defer tx.Rollback()
	now := s.now()
	for _, delivery := range deliveries {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO brd_inbox
			 (agent_id, entry_id, post_id, created_at, mode, aware)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			delivery.AgentID, entry.ID, entry.PostID, now, delivery.Mode, boolInt(delivery.Aware),
		); err != nil {
			return nil, fmt.Errorf("board: enqueue inbox: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("board: commit enqueue: %w", err)
	}
	return deliveries, nil
}

// MarkInboxRead marks every ping for an agent+post as read.
func (s *Store) MarkInboxRead(ctx context.Context, agentID, postID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_inbox SET read_at = ?
		 WHERE agent_id = ? AND post_id = ? AND read_at IS NULL`,
		s.now(), strings.TrimSpace(agentID), strings.TrimSpace(postID),
	); err != nil {
		return fmt.Errorf("board: mark inbox read: %w", err)
	}
	return nil
}

// MarkInboxProcessed records a successful automated admission. Automated
// pings are also marked read so they stop showing as unread.
func (s *Store) MarkInboxProcessed(ctx context.Context, agentID, entryID, sessionID, messageID string) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_inbox
		 SET processed_at = ?, read_at = COALESCE(read_at, ?), session_id = ?, message_id = ?,
		     claimed_at = NULL, claim_token = '', error = ''
		 WHERE agent_id = ? AND entry_id = ?`,
		now, now, sessionID, messageID, agentID, entryID,
	); err != nil {
		return fmt.Errorf("board: mark inbox processed: %w", err)
	}
	return nil
}

// MarkInboxError records a failed automated admission attempt, increments the
// attempt counter, and releases the claim so a later retry can pick it up.
func (s *Store) MarkInboxError(ctx context.Context, agentID, entryID, message string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_inbox
		 SET attempts = attempts + 1, error = ?, claimed_at = NULL, claim_token = ''
		 WHERE agent_id = ? AND entry_id = ?`,
		message, agentID, entryID,
	); err != nil {
		return fmt.Errorf("board: mark inbox error: %w", err)
	}
	return nil
}

// ReleaseInboxClaim releases a claim without recording a failure. It is used
// when another turn already reserved the per-post budget.
func (s *Store) ReleaseInboxClaim(ctx context.Context, agentID, entryID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_inbox SET claimed_at = NULL, claim_token = ''
		 WHERE agent_id = ? AND entry_id = ? AND processed_at IS NULL`,
		agentID, entryID,
	); err != nil {
		return fmt.Errorf("board: release inbox claim: %w", err)
	}
	return nil
}

// PendingAutomated lists entry IDs waiting for automated processing.
func (s *Store) PendingAutomated(ctx context.Context, agentID, postID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.entry_id FROM brd_inbox i
		 WHERE i.agent_id = ? AND i.post_id = ? AND i.mode = 'automated' AND i.processed_at IS NULL
		   AND EXISTS (SELECT 1 FROM brd_participants p
		               WHERE p.post_id = i.post_id AND p.kind = 'agent' AND p.id = i.agent_id)
		 ORDER BY i.created_at, i.entry_id LIMIT ?`, agentID, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("board: pending automation: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("board: pending automation: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimPendingAutomated atomically claims one pending automated ping. It
// returns stale=true when a previous claim expired, so the caller can reconcile
// the admission before retrying.
func (s *Store) ClaimPendingAutomated(ctx context.Context, agentID, postID, claimToken string, staleAfter time.Duration) (entryID string, stale bool, err error) {
	claimToken = strings.TrimSpace(claimToken)
	if claimToken == "" {
		return "", false, fmt.Errorf("%w: claim token is required", ErrInvalid)
	}
	if staleAfter <= 0 {
		staleAfter = 10 * time.Minute
	}
	now := s.now()
	cutoff := now.Add(-staleAfter)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("board: begin claim: %w", err)
	}
	defer tx.Rollback()

	var claimedAt sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT i.entry_id, i.claimed_at FROM brd_inbox i
		 WHERE i.agent_id = ? AND i.post_id = ? AND i.mode = 'automated' AND i.processed_at IS NULL
		   AND (i.claimed_at IS NULL OR i.claimed_at < ?)
		   AND EXISTS (SELECT 1 FROM brd_participants p
		               WHERE p.post_id = i.post_id AND p.kind = 'agent' AND p.id = i.agent_id)
		 ORDER BY i.created_at, i.entry_id LIMIT 1`,
		agentID, postID, cutoff,
	).Scan(&entryID, &claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("board: select claim: %w", err)
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE brd_inbox
		 SET claimed_at = ?, claim_token = ?
		 WHERE agent_id = ? AND entry_id = ? AND processed_at IS NULL
		   AND (claimed_at IS NULL OR claimed_at < ?)
		   AND EXISTS (SELECT 1 FROM brd_participants p
		               WHERE p.post_id = brd_inbox.post_id AND p.kind = 'agent'
		                 AND p.id = brd_inbox.agent_id)`,
		now, claimToken, agentID, entryID, cutoff,
	)
	if err != nil {
		return "", false, fmt.Errorf("board: claim inbox: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("board: claim inbox: %w", err)
	}
	if rows == 0 {
		return "", false, nil
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("board: commit claim: %w", err)
	}
	return entryID, claimedAt.Valid, nil
}

// ReserveAutomationTurn atomically reserves the cooldown and one turn of the
// per-post budget before an admission. It returns false when the post is
// paused, cooling down, or at the cap.
func (s *Store) ReserveAutomationTurn(ctx context.Context, agentID, postID string, cooldown time.Duration, maxTurns int) (bool, error) {
	if maxTurns <= 0 {
		return false, fmt.Errorf("%w: max turns must be > 0", ErrInvalid)
	}
	if cooldown < 0 {
		cooldown = 0
	}
	now := s.now()
	cutoff := now.Add(-cooldown)
	result, err := s.db.ExecContext(ctx,
		`UPDATE brd_automations
		 SET last_turn_at = ?, turns = turns + 1, updated_at = ?
		 WHERE agent_id = ? AND post_id = ? AND paused_at IS NULL
		   AND turns < ? AND (last_turn_at IS NULL OR last_turn_at <= ?)`,
		now, now, agentID, postID, maxTurns, cutoff,
	)
	if err != nil {
		return false, fmt.Errorf("board: reserve automation turn: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("board: reserve automation turn: %w", err)
	}
	return rows > 0, nil
}

// RecordAutomationSession stores the dedicated session after an admission
// that already reserved its turn.
func (s *Store) RecordAutomationSession(ctx context.Context, agentID, postID, sessionID string) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_automations SET session_id = ?, updated_at = ?
		 WHERE agent_id = ? AND post_id = ?`,
		sessionID, now, agentID, postID,
	); err != nil {
		return fmt.Errorf("board: record automation session: %w", err)
	}
	return nil
}

// RecordAutomationAdmission reconciles a stale claim that was already
// admitted. It records the session and stamps the turn budget, since the
// original reservation may have been lost with a crash.
func (s *Store) RecordAutomationAdmission(ctx context.Context, agentID, postID, sessionID string) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_automations
		 SET session_id = ?, last_turn_at = ?, updated_at = ?
		 WHERE agent_id = ? AND post_id = ?`,
		sessionID, now, now, agentID, postID,
	); err != nil {
		return fmt.Errorf("board: reconcile automation admission: %w", err)
	}
	return nil
}

// PauseAutomation stops automation for an agent+post.
func (s *Store) PauseAutomation(ctx context.Context, agentID, postID, reason string) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_automations
		 SET paused_at = ?, pause_reason = ?, updated_at = ?
		 WHERE agent_id = ? AND post_id = ?`,
		now, strings.TrimSpace(reason), now, agentID, postID,
	); err != nil {
		return fmt.Errorf("board: pause automation: %w", err)
	}
	return nil
}

// InboxAttempts returns the admission attempt count for one ping.
func (s *Store) InboxAttempts(ctx context.Context, agentID, entryID string) (int, error) {
	var attempts int
	err := s.db.QueryRowContext(ctx,
		`SELECT attempts FROM brd_inbox WHERE agent_id = ? AND entry_id = ?`,
		agentID, entryID,
	).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: inbox ping %q", ErrNotFound, entryID)
	}
	if err != nil {
		return 0, fmt.Errorf("board: read inbox attempts: %w", err)
	}
	return attempts, nil
}

// MarkInboxSession records the main session used by an explicit promote.
func (s *Store) MarkInboxSession(ctx context.Context, agentID, postID, sessionID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brd_inbox SET session_id = ?
		 WHERE agent_id = ? AND post_id = ? AND read_at IS NULL`,
		sessionID, agentID, postID,
	); err != nil {
		return fmt.Errorf("board: mark inbox session: %w", err)
	}
	return nil
}

// CreateAlert records a user-facing automation alert.
func (s *Store) CreateAlert(ctx context.Context, agentID, postID, message string) (Alert, error) {
	alert := Alert{
		ID:        newID("alt_"),
		AgentID:   strings.TrimSpace(agentID),
		PostID:    strings.TrimSpace(postID),
		Message:   strings.TrimSpace(message),
		CreatedAt: s.now(),
	}
	if alert.AgentID == "" || alert.Message == "" {
		return Alert{}, fmt.Errorf("%w: agent and message are required", ErrInvalid)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO brd_alerts (id, agent_id, post_id, message, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		alert.ID, alert.AgentID, alert.PostID, alert.Message, alert.CreatedAt,
	); err != nil {
		return Alert{}, fmt.Errorf("board: create alert: %w", err)
	}
	return alert, nil
}

func upsertSubscription(ctx context.Context, q queryer, agentID, postID string, aware bool, now time.Time) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO brd_subscriptions (agent_id, post_id, aware, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(agent_id, post_id) DO UPDATE SET aware = excluded.aware`,
		agentID, postID, boolInt(aware), now,
	); err != nil {
		return fmt.Errorf("board: upsert subscription: %w", err)
	}
	return nil
}

type SessionState struct {
	SessionID string
	Turns     int
}
