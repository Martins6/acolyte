package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ResolvePost resolves a full post ID or a unique prefix.
func (s *Store) ResolvePost(ctx context.Context, ref string) (Post, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Post{}, fmt.Errorf("%w: empty post reference", ErrInvalid)
	}
	post, err := s.getPost(ctx, s.db, ref)
	if err == nil {
		return post, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Post{}, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM brd_posts WHERE id LIKE ? ESCAPE '\' ORDER BY id LIMIT 5`,
		escapeLike(ref)+"%")
	if err != nil {
		return Post{}, fmt.Errorf("board: resolve post %q: %w", ref, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Post{}, fmt.Errorf("board: resolve post %q: %w", ref, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return Post{}, fmt.Errorf("board: resolve post %q: %w", ref, err)
	}
	switch len(ids) {
	case 0:
		return Post{}, fmt.Errorf("%w: post %q", ErrNotFound, ref)
	case 1:
		return s.getPost(ctx, s.db, ids[0])
	default:
		return Post{}, fmt.Errorf("%w: post prefix %q matches %d posts", ErrAmbiguous, ref, len(ids))
	}
}

// ResolveEntry resolves a full entry ID, a unique prefix, or #seq inside postID.
func (s *Store) ResolveEntry(ctx context.Context, postID, ref string) (Entry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Entry{}, fmt.Errorf("%w: empty entry reference", ErrInvalid)
	}
	if strings.HasPrefix(ref, "#") {
		seq, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
		if err != nil || seq < 0 {
			return Entry{}, fmt.Errorf("%w: invalid entry reference %q", ErrInvalid, ref)
		}
		return s.getEntryBySeq(ctx, s.db, postID, seq)
	}

	entry, err := s.getEntry(ctx, s.db, postID, ref)
	if err == nil {
		return entry, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Entry{}, err
	}

	rows, err := s.db.QueryContext(ctx,
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
		return Entry{}, fmt.Errorf("board: resolve entry %q: %w", ref, err)
	}
	switch len(ids) {
	case 0:
		return Entry{}, fmt.Errorf("%w: entry %q", ErrNotFound, ref)
	case 1:
		return s.getEntry(ctx, s.db, postID, ids[0])
	default:
		return Entry{}, fmt.Errorf("%w: entry prefix %q matches %d entries", ErrAmbiguous, ref, len(ids))
	}
}

func (s *Store) getPost(ctx context.Context, q queryer, id string) (Post, error) {
	var post Post
	var kind string
	err := q.QueryRowContext(ctx,
		`SELECT id, title, created_by_kind, created_by_id, created_at, updated_at
		 FROM brd_posts WHERE id = ?`, id,
	).Scan(&post.ID, &post.Title, &kind, &post.CreatedBy.ID, &post.CreatedAt, &post.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, fmt.Errorf("%w: post %q", ErrNotFound, id)
	}
	if err != nil {
		return Post{}, fmt.Errorf("board: load post %q: %w", id, err)
	}
	post.CreatedBy.Kind = ActorKind(kind)
	if post.Tags, err = s.loadTags(ctx, q, id); err != nil {
		return Post{}, err
	}
	if post.Participants, err = s.loadParticipants(ctx, q, id); err != nil {
		return Post{}, err
	}
	return post, nil
}

func (s *Store) loadTags(ctx context.Context, q queryer, postID string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT tag FROM brd_tags WHERE post_id = ? ORDER BY tag`, postID)
	if err != nil {
		return nil, fmt.Errorf("board: load tags: %w", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("board: load tags: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (s *Store) loadParticipants(ctx context.Context, q queryer, postID string) ([]Actor, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT kind, id FROM brd_participants WHERE post_id = ? ORDER BY kind, id`, postID)
	if err != nil {
		return nil, fmt.Errorf("board: load participants: %w", err)
	}
	defer rows.Close()
	var actors []Actor
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("board: load participants: %w", err)
		}
		actors = append(actors, Actor{Kind: ActorKind(kind), ID: id})
	}
	return actors, rows.Err()
}

func (s *Store) getEntry(ctx context.Context, q queryer, postID, id string) (Entry, error) {
	entry, err := scanEntry(q.QueryRowContext(ctx,
		`SELECT id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at
		 FROM brd_entries WHERE post_id = ? AND id = ?`, postID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: entry %q", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("board: load entry %q: %w", id, err)
	}
	if entry.Files, err = s.loadFiles(ctx, q, entry.ID); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Store) getEntryBySeq(ctx context.Context, q queryer, postID string, seq int) (Entry, error) {
	entry, err := scanEntry(q.QueryRowContext(ctx,
		`SELECT id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at
		 FROM brd_entries WHERE post_id = ? AND seq = ?`, postID, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: entry #%d", ErrNotFound, seq)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("board: load entry #%d: %w", seq, err)
	}
	if entry.Files, err = s.loadFiles(ctx, q, entry.ID); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func scanEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var entry Entry
	var kind, authorKind string
	if err := row.Scan(
		&entry.ID, &entry.EnvelopeID, &entry.PostID, &entry.Seq, &kind, &authorKind, &entry.Author.ID,
		&entry.Text, &entry.ReplyToID, &entry.CreatedAt,
	); err != nil {
		return Entry{}, err
	}
	entry.Kind = EntryKind(kind)
	entry.Author.Kind = ActorKind(authorKind)
	return entry, nil
}

func (s *Store) loadFiles(ctx context.Context, q queryer, entryID string) ([]File, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name, path, mode, source_path, size, sha256
		 FROM brd_files WHERE entry_id = ? ORDER BY seq`, entryID)
	if err != nil {
		return nil, fmt.Errorf("board: load files: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var file File
		var mode string
		if err := rows.Scan(&file.Name, &file.Path, &mode, &file.SourcePath, &file.Size, &file.SHA256); err != nil {
			return nil, fmt.Errorf("board: load files: %w", err)
		}
		file.Mode = FileMode(mode)
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Store) loadEntries(ctx context.Context, q queryer, postID string) ([]Entry, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, envelope_id, post_id, seq, kind, author_kind, author_id, text, reply_to_id, created_at
		 FROM brd_entries WHERE post_id = ? ORDER BY seq`, postID)
	if err != nil {
		return nil, fmt.Errorf("board: load entries: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("board: load entries: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range entries {
		files, err := s.loadFiles(ctx, q, entries[i].ID)
		if err != nil {
			return nil, err
		}
		entries[i].Files = files
	}
	return entries, nil
}

// List returns participant-visible posts ordered by last activity.
func (s *Store) List(ctx context.Context, actor Actor, filter ListFilter) ([]PostSummary, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var where []string
	var args []any
	if !actor.IsUser() {
		where = append(where, `EXISTS (
			SELECT 1 FROM brd_participants x
			WHERE x.post_id = p.id AND x.kind = ? AND x.id = ?)`)
		args = append(args, string(actor.Kind), actor.ID)
	}
	for _, tag := range filter.Tags {
		normalized, err := normalizeTag(tag)
		if err != nil {
			return nil, err
		}
		where = append(where, `EXISTS (
			SELECT 1 FROM brd_tags t WHERE t.post_id = p.id AND t.tag = ?)`)
		args = append(args, normalized)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + escapeLike(search) + "%"
		where = append(where, `(p.title LIKE ? ESCAPE '\' OR EXISTS (
			SELECT 1 FROM brd_entries e
			WHERE e.post_id = p.id AND e.text LIKE ? ESCAPE '\'))`)
		args = append(args, pattern, pattern)
	}
	if filter.Since != nil {
		where = append(where, `p.updated_at >= ?`)
		args = append(args, filter.Since.UTC())
	}
	if filter.Until != nil {
		where = append(where, `p.updated_at < ?`)
		args = append(args, filter.Until.UTC())
	}
	if filter.Unread {
		where = append(where, `EXISTS (
			SELECT 1 FROM brd_entries e
			WHERE e.post_id = p.id
			  AND e.seq > COALESCE((
				SELECT r.last_read_seq FROM brd_reads r
				WHERE r.post_id = p.id AND r.kind = ? AND r.id = ?), -1)
			  AND NOT (e.author_kind = ? AND e.author_id = ?))`)
		args = append(args, string(actor.Kind), actor.ID, string(actor.Kind), actor.ID)
	}

	query := `SELECT p.id FROM brd_posts p`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY p.updated_at DESC, p.id LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("board: list posts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("board: list posts: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	summaries := make([]PostSummary, 0, len(ids))
	for _, id := range ids {
		post, err := s.getPost(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		messages, unread, err := s.postCounts(ctx, post.ID, actor)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, PostSummary{Post: post, MessageCount: messages, Unread: unread})
	}
	return summaries, nil
}

func (s *Store) postCounts(ctx context.Context, postID string, actor Actor) (messages, unread int, err error) {
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_entries WHERE post_id = ? AND kind = 'message'`, postID,
	).Scan(&messages); err != nil {
		return 0, 0, fmt.Errorf("board: count messages: %w", err)
	}
	unread, err = s.unreadCount(ctx, s.db, postID, actor)
	if err != nil {
		return 0, 0, err
	}
	return messages, unread, nil
}

func (s *Store) unreadCount(ctx context.Context, q queryer, postID string, actor Actor) (int, error) {
	var count int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_entries e
		 WHERE e.post_id = ?
		   AND e.seq > COALESCE((
			SELECT r.last_read_seq FROM brd_reads r
			WHERE r.post_id = e.post_id AND r.kind = ? AND r.id = ?), -1)
		   AND NOT (e.author_kind = ? AND e.author_id = ?)`,
		postID, string(actor.Kind), actor.ID, string(actor.Kind), actor.ID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("board: count unread: %w", err)
	}
	return count, nil
}

func (s *Store) subscriptionFor(ctx context.Context, q queryer, agentID, postID string) (Subscription, bool, error) {
	for _, scope := range []string{postID, ""} {
		var sub Subscription
		var aware int
		err := q.QueryRowContext(ctx,
			`SELECT agent_id, post_id, aware, created_at
			 FROM brd_subscriptions WHERE agent_id = ? AND post_id = ?`, agentID, scope,
		).Scan(&sub.AgentID, &sub.PostID, &aware, &sub.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Subscription{}, false, fmt.Errorf("board: load subscription: %w", err)
		}
		sub.Aware = aware != 0
		return sub, true, nil
	}
	return Subscription{}, false, nil
}

func (s *Store) muted(ctx context.Context, q queryer, agentID, postID string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_mutes WHERE agent_id = ? AND post_id = ?`, agentID, postID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("board: load mute: %w", err)
	}
	return count > 0, nil
}

func (s *Store) getAutomation(ctx context.Context, q queryer, agentID, postID string) (*Automation, error) {
	var automation Automation
	var lastTurn, paused sql.NullTime
	err := q.QueryRowContext(ctx,
		`SELECT agent_id, post_id, session_id, created_at, updated_at, last_turn_at,
		        turns, paused_at, pause_reason
		 FROM brd_automations WHERE agent_id = ? AND post_id = ?`, agentID, postID,
	).Scan(&automation.AgentID, &automation.PostID, &automation.SessionID,
		&automation.CreatedAt, &automation.UpdatedAt, &lastTurn, &automation.Turns,
		&paused, &automation.PauseReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("board: load automation: %w", err)
	}
	if lastTurn.Valid {
		automation.LastTurnAt = lastTurn.Time.UTC()
	}
	if paused.Valid {
		automation.PausedAt = paused.Time.UTC()
	}
	return &automation, nil
}

// Inbox returns an agent's queued board pings.
func (s *Store) Inbox(ctx context.Context, agentID string, all bool) ([]InboxView, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent id is required", ErrInvalid)
	}
	query := `SELECT i.agent_id, i.entry_id, i.post_id, i.created_at, i.read_at, i.processed_at,
	                i.mode, i.aware, i.session_id, i.message_id, i.attempts, i.error,
	                p.title, e.seq, e.kind, e.text, e.author_kind, e.author_id
	         FROM brd_inbox i
	         JOIN brd_posts p ON p.id = i.post_id
	         JOIN brd_entries e ON e.id = i.entry_id
	         WHERE i.agent_id = ?`
	if !all {
		query += ` AND i.read_at IS NULL`
	}
	query += ` ORDER BY i.created_at DESC, e.seq DESC`

	rows, err := s.db.QueryContext(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("board: list inbox: %w", err)
	}
	defer rows.Close()

	var items []InboxView
	for rows.Next() {
		var item InboxView
		var readAt, processedAt sql.NullTime
		var aware int
		var entryKind, authorKind string
		if err := rows.Scan(
			&item.AgentID, &item.EntryID, &item.PostID, &item.CreatedAt, &readAt, &processedAt,
			&item.Mode, &aware, &item.SessionID, &item.MessageID, &item.Attempts, &item.Error,
			&item.PostTitle, &item.EntrySeq, &entryKind, &item.EntryText, &authorKind, &item.EntryAuthor.ID,
		); err != nil {
			return nil, fmt.Errorf("board: list inbox: %w", err)
		}
		item.Aware = aware != 0
		if readAt.Valid {
			value := readAt.Time.UTC()
			item.ReadAt = &value
		}
		if processedAt.Valid {
			value := processedAt.Time.UTC()
			item.ProcessedAt = &value
		}
		item.EntryKind = EntryKind(entryKind)
		item.EntryAuthor.Kind = ActorKind(authorKind)
		items = append(items, item)
	}
	return items, rows.Err()
}

// UnreadCount returns unread aware manual pings, their post count, and unread alerts.
func (s *Store) UnreadCount(ctx context.Context, agentID string) (pings, posts, alerts int, err error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, 0, 0, fmt.Errorf("%w: agent id is required", ErrInvalid)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT post_id)
		 FROM brd_inbox
		 WHERE agent_id = ? AND read_at IS NULL AND aware = 1 AND mode = 'manual'`, agentID,
	).Scan(&pings, &posts); err != nil {
		return 0, 0, 0, fmt.Errorf("board: count inbox: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_alerts WHERE agent_id = ? AND notified_at IS NULL`, agentID,
	).Scan(&alerts); err != nil {
		return 0, 0, 0, fmt.Errorf("board: count alerts: %w", err)
	}
	return pings, posts, alerts, nil
}

// Alerts returns unread automation alerts for an agent.
func (s *Store) Alerts(ctx context.Context, agentID string) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, post_id, message, created_at, notified_at
		 FROM brd_alerts WHERE agent_id = ? AND notified_at IS NULL
		 ORDER BY created_at`, strings.TrimSpace(agentID))
	if err != nil {
		return nil, fmt.Errorf("board: list alerts: %w", err)
	}
	defer rows.Close()
	var alerts []Alert
	for rows.Next() {
		var alert Alert
		var notified sql.NullTime
		if err := rows.Scan(&alert.ID, &alert.AgentID, &alert.PostID, &alert.Message, &alert.CreatedAt, &notified); err != nil {
			return nil, fmt.Errorf("board: list alerts: %w", err)
		}
		if notified.Valid {
			alert.NotifiedAt = notified.Time.UTC()
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

// MarkAlertsNotified stamps alerts as delivered to the user.
func (s *Store) MarkAlertsNotified(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("board: mark alerts: %w", err)
	}
	defer tx.Rollback()
	now := s.now()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE brd_alerts SET notified_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("board: mark alert %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("board: mark alerts: %w", err)
	}
	return nil
}

// SubscriptionState returns an agent's subscriptions and mutes.
func (s *Store) SubscriptionState(ctx context.Context, agentID string) (SubscriptionState, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return SubscriptionState{}, fmt.Errorf("%w: agent id is required", ErrInvalid)
	}
	state := SubscriptionState{AgentID: agentID}
	rows, err := s.db.QueryContext(ctx,
		`SELECT post_id, aware, created_at FROM brd_subscriptions
		 WHERE agent_id = ? ORDER BY post_id`, agentID)
	if err != nil {
		return state, fmt.Errorf("board: list subscriptions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sub Subscription
		var aware int
		if err := rows.Scan(&sub.PostID, &aware, &sub.CreatedAt); err != nil {
			return state, fmt.Errorf("board: list subscriptions: %w", err)
		}
		sub.AgentID = agentID
		sub.Aware = aware != 0
		if sub.PostID == "" {
			copy := sub
			state.All = &copy
			continue
		}
		state.Posts = append(state.Posts, sub)
	}
	if err := rows.Err(); err != nil {
		return state, err
	}

	muteRows, err := s.db.QueryContext(ctx,
		`SELECT post_id FROM brd_mutes WHERE agent_id = ? ORDER BY post_id`, agentID)
	if err != nil {
		return state, fmt.Errorf("board: list mutes: %w", err)
	}
	defer muteRows.Close()
	for muteRows.Next() {
		var postID string
		if err := muteRows.Scan(&postID); err != nil {
			return state, fmt.Errorf("board: list mutes: %w", err)
		}
		state.Mutes = append(state.Mutes, postID)
	}
	return state, muteRows.Err()
}

func (s *Store) requireParticipant(ctx context.Context, q queryer, postID string, actor Actor) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if actor.IsUser() {
		return nil
	}
	var count int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_participants
		 WHERE post_id = ? AND kind = ? AND id = ?`,
		postID, string(actor.Kind), actor.ID,
	).Scan(&count); err != nil {
		return fmt.Errorf("board: check participant: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %s is not a participant of post %q", ErrForbidden, actor.String(), postID)
	}
	return nil
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	u := value.UTC()
	return &u
}
