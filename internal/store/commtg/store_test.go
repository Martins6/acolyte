package commtg

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comm.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	return db
}

func migrateTestDB(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return New(db)
}

func TestMigrateFromV1(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)`, module, 1); err != nil {
		t.Fatalf("record v1: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(v1) error: %v", err)
	}
	var versionValue int
	if err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, module).Scan(&versionValue); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if versionValue != version {
		t.Errorf("version = %d, want %d", versionValue, version)
	}
	for _, table := range []string{"comm_tg_chats", "comm_tg_inbox", "comm_tg_outbox", "comm_tg_media"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestMigrateFromV2AddsPrivate(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)`, module, 2); err != nil {
		t.Fatalf("record v2: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(v2) error: %v", err)
	}
	var versionValue int
	if err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, module).Scan(&versionValue); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if versionValue != version {
		t.Errorf("version = %d, want %d", versionValue, version)
	}
	rows, err := db.Query(`PRAGMA table_info(comm_tg_chats)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "private" {
			found = true
		}
	}
	if !found {
		t.Error("comm_tg_chats is missing the private column after v2 migration")
	}
}

func TestMigrateFromV2GroupRowFailsClosed(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)`, module, 2); err != nil {
		t.Fatalf("record v2: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO comm_tg_chats (chat_id, username, allowed, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`, int64(-100123), "group", 1, now, now); err != nil {
		t.Fatalf("insert v2 chat: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(v2) error: %v", err)
	}
	store := New(db)
	if _, ok, err := store.LatestAllowedChat(); err != nil || ok {
		t.Errorf("LatestAllowedChat() = ok:%v err:%v, want none for an unclassified v2 chat", ok, err)
	}
	chat, ok, err := store.GetChat(-100123)
	if err != nil || !ok || chat.Private {
		t.Errorf("chat = %+v, %v, %v; want non-private fail-closed row", chat, ok, err)
	}
}

func TestMigrateV3Resumable(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE comm_tg_chats ADD COLUMN private INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("simulate partial v3: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)`, module, 2); err != nil {
		t.Fatalf("record v2: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(partial v3) error: %v", err)
	}
	var versionValue int
	if err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, module).Scan(&versionValue); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if versionValue != version {
		t.Errorf("version = %d, want %d", versionValue, version)
	}
}

func TestMigrateFromV3AddsReplayColumns(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_meta (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE comm_tg_chats ADD COLUMN private INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("create v3 chat column: %v", err)
	}
	// Simulate a partial v4 migration: one replay column already exists.
	if _, err := db.Exec(`ALTER TABLE comm_tg_inbox ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("create partial v4 column: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta (module, version) VALUES (?, ?)`, module, 3); err != nil {
		t.Fatalf("record v3: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO comm_tg_inbox
		(id, tg_update_id, chat_id, envelope_id, media_path, opencode_session_id, opencode_message_id, received_at)
		VALUES (?, ?, ?, ?, '', '', '', ?)`, "old", 1, 10, "env_old", now); err != nil {
		t.Fatalf("insert v3 inbox: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate(v3) error: %v", err)
	}
	var versionValue int
	if err := db.QueryRow(`SELECT version FROM schema_meta WHERE module = ?`, module).Scan(&versionValue); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if versionValue != version {
		t.Errorf("version = %d, want %d", versionValue, version)
	}
	store := New(db)
	item, ok, err := store.GetInboxByEnvelope("env_old")
	if err != nil || !ok {
		t.Fatalf("GetInboxByEnvelope() = %+v, %v, %v", item, ok, err)
	}
	if item.ProcessedAt == nil {
		t.Error("pre-v4 inbox row was not marked processed")
	}
	if pending, err := store.PendingInbound(time.Now().UTC(), 10); err != nil || len(pending) != 0 {
		t.Errorf("PendingInbound() = %+v, %v; want none", pending, err)
	}
}

func TestClaimInboundOnce(t *testing.T) {
	store := migrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := store.RecordInbound(InboxItem{
		TGUpdateID: 300, ChatID: 10, EnvelopeID: "env_claim", PendingEnvelope: "{}", ReceivedAt: now,
	}); err != nil {
		t.Fatalf("RecordInbound() error: %v", err)
	}
	lease := now.Add(10 * time.Minute)
	claimed, err := store.ClaimInbound("env_claim", "tok1", now, lease)
	if err != nil || !claimed {
		t.Fatalf("ClaimInbound() = %v, %v", claimed, err)
	}
	if claimed, err := store.ClaimInbound("env_claim", "tok2", now, lease); err != nil || claimed {
		t.Fatalf("ClaimInbound(second) = %v, %v; want false", claimed, err)
	}
	if renewed, err := store.RenewInboundClaim("env_claim", "tok2", lease.Add(time.Minute)); err != nil || renewed {
		t.Fatalf("RenewInboundClaim(wrong token) = %v, %v; want false", renewed, err)
	}
	if renewed, err := store.RenewInboundClaim("env_claim", "tok1", lease.Add(time.Minute)); err != nil || !renewed {
		t.Fatalf("RenewInboundClaim() = %v, %v; want true", renewed, err)
	}
	if pending, err := store.PendingInbound(now, 10); err != nil || len(pending) != 0 {
		t.Errorf("PendingInbound(leased) = %+v, %v; want none", pending, err)
	}
	if pending, err := store.PendingInbound(lease.Add(time.Minute+time.Second), 10); err != nil || len(pending) != 1 {
		t.Errorf("PendingInbound(expired) = %+v, %v; want 1", pending, err)
	}
}

func TestSettings(t *testing.T) {
	store := migrateTestDB(t)
	if err := store.SetSetting("telegram.allowed_users", `["@alice"]`); err != nil {
		t.Fatalf("SetSetting() error: %v", err)
	}
	value, ok, err := store.GetSetting("telegram.allowed_users")
	if err != nil || !ok || value != `["@alice"]` {
		t.Fatalf("GetSetting() = %q, %v, %v", value, ok, err)
	}
	if err := store.SetSetting("telegram.allowed_users", `["@bob"]`); err != nil {
		t.Fatalf("SetSetting(update) error: %v", err)
	}
	value, _, _ = store.GetSetting("telegram.allowed_users")
	if value != `["@bob"]` {
		t.Errorf("setting = %q, want updated value", value)
	}
}

func TestChats(t *testing.T) {
	store := migrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertChat(10, "alice", true, true, now); err != nil {
		t.Fatalf("UpsertChat() error: %v", err)
	}
	if err := store.UpsertChat(20, "bob", false, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("UpsertChat(denied) error: %v", err)
	}
	chat, ok, err := store.GetChat(10)
	if err != nil || !ok || chat.Username != "alice" || !chat.Allowed {
		t.Fatalf("GetChat() = %+v, %v, %v", chat, ok, err)
	}
	latest, ok, err := store.LatestAllowedChat()
	if err != nil || !ok || latest.ChatID != 10 {
		t.Fatalf("LatestAllowedChat() = %+v, %v, %v", latest, ok, err)
	}
	if err := store.UpsertChat(10, "alice2", true, true, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertChat(update) error: %v", err)
	}
	if err := store.UpsertChat(30, "group", true, false, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("UpsertChat(group) error: %v", err)
	}
	latest, _, _ = store.LatestAllowedChat()
	if latest.ChatID != 10 || latest.Username != "alice2" {
		t.Errorf("latest = %+v, want private chat 10 even though the group is newer", latest)
	}
	group, ok, err := store.GetChat(30)
	if err != nil || !ok || group.Private {
		t.Errorf("group = %+v, %v, %v; want non-private chat", group, ok, err)
	}
	if _, ok, err := store.GetChat(999); err != nil || ok {
		t.Errorf("GetChat(missing) = ok:%v err:%v", ok, err)
	}
}

func TestInboundDedupAndLink(t *testing.T) {
	store := migrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	item := InboxItem{TGUpdateID: 100, ChatID: 10, EnvelopeID: "env_1", ReceivedAt: now}
	inserted, err := store.RecordInbound(item)
	if err != nil || !inserted {
		t.Fatalf("RecordInbound() = %v, %v", inserted, err)
	}
	inserted, err = store.RecordInbound(item)
	if err != nil {
		t.Fatalf("RecordInbound(duplicate) error: %v", err)
	}
	if inserted {
		t.Error("RecordInbound(duplicate) = true, want false")
	}
	if _, err := store.RecordInbound(InboxItem{TGUpdateID: 101, ChatID: 10, EnvelopeID: "env_1"}); err == nil {
		t.Error("RecordInbound(duplicate envelope) error = nil, want error")
	}
	if err := store.LinkInbound("env_1", "ses_1", "msg_1"); err != nil {
		t.Fatalf("LinkInbound() error: %v", err)
	}
	if err := store.LinkInbound("env_missing", "ses_1", "msg_1"); !errors.Is(err, ErrInboundNotFound) {
		t.Fatalf("LinkInbound(missing) error = %v, want ErrInboundNotFound", err)
	}
	got, ok, err := store.GetInboxByEnvelope("env_1")
	if err != nil || !ok || got.OpenCodeSessionID != "ses_1" || got.OpenCodeMessageID != "msg_1" {
		t.Fatalf("GetInboxByEnvelope() = %+v, %v, %v", got, ok, err)
	}
	list, err := store.ListInbox(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListInbox() = %+v, %v", list, err)
	}
}

func TestOutboxLifecycle(t *testing.T) {
	store := migrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	item, err := store.EnqueueOutbound(OutboxItem{
		EnvelopeID: "env_1", ChatID: 10, Text: "hello", OpenCodeSessionID: "ses_1",
		SourceMessageID: "msg_1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("EnqueueOutbound() error: %v", err)
	}
	if item.ID == "" || item.Delivery != "pending" || item.NextAttemptAt == nil {
		t.Fatalf("item = %+v", item)
	}
	due, err := store.DueOutbound(now, 10)
	if err != nil || len(due) != 1 || due[0].ID != item.ID {
		t.Fatalf("DueOutbound() = %+v, %v", due, err)
	}
	if err := store.MarkOutboundProgress(item.ID, 1, 555); err != nil {
		t.Fatalf("MarkOutboundProgress() error: %v", err)
	}
	due, _ = store.DueOutbound(now, 10)
	if len(due) != 1 || due[0].ChunksSent != 1 || due[0].TGMessageID == nil || *due[0].TGMessageID != 555 {
		t.Fatalf("due after progress = %+v", due)
	}
	next := now.Add(time.Minute)
	if err := store.MarkOutboundRetry(item.ID, 1, next, "temporary"); err != nil {
		t.Fatalf("MarkOutboundRetry() error: %v", err)
	}
	due, _ = store.DueOutbound(now, 10)
	if len(due) != 0 {
		t.Errorf("due before retry = %+v, want none", due)
	}
	due, _ = store.DueOutbound(next, 10)
	if len(due) != 1 || due[0].Attempts != 1 || due[0].LastError != "temporary" {
		t.Fatalf("due at retry = %+v", due)
	}
	if err := store.MarkOutboundSent(item.ID, 2, 556, next); err != nil {
		t.Fatalf("MarkOutboundSent() error: %v", err)
	}
	list, err := store.ListOutbox(10)
	if err != nil || len(list) != 1 || list[0].Delivery != "sent" || list[0].SentAt == nil {
		t.Fatalf("ListOutbox() = %+v, %v", list, err)
	}
	if list[0].Text != "" {
		t.Errorf("sent outbox text = %q, want cleared after delivery", list[0].Text)
	}
	if err := store.MarkOutboundRetry(item.ID, 2, next, "late"); !errors.Is(err, ErrOutboxNotFound) {
		t.Errorf("MarkOutboundRetry(sent) error = %v, want ErrOutboxNotFound", err)
	}
	if err := store.MarkOutboundProgress(item.ID, 3, 557); !errors.Is(err, ErrOutboxNotFound) {
		t.Errorf("MarkOutboundProgress(sent) error = %v, want ErrOutboxNotFound", err)
	}
	if err := store.MarkOutboundFailed(item.ID, "late"); !errors.Is(err, ErrOutboxNotFound) {
		t.Errorf("MarkOutboundFailed(sent) error = %v, want ErrOutboxNotFound", err)
	}
	if err := store.MarkOutboundFailed("missing", "boom"); !errors.Is(err, ErrOutboxNotFound) {
		t.Fatalf("MarkOutboundFailed(missing) error = %v, want ErrOutboxNotFound", err)
	}
	failed, err := store.EnqueueOutbound(OutboxItem{EnvelopeID: "env_2", ChatID: 10, Text: "secret", CreatedAt: now})
	if err != nil {
		t.Fatalf("EnqueueOutbound(failed) error: %v", err)
	}
	if err := store.MarkOutboundFailed(failed.ID, "permanent"); err != nil {
		t.Fatalf("MarkOutboundFailed() error: %v", err)
	}
	if err := store.MarkOutboundRetry(failed.ID, 1, next, "late"); !errors.Is(err, ErrOutboxNotFound) {
		t.Errorf("MarkOutboundRetry(failed) error = %v, want ErrOutboxNotFound", err)
	}
	list, _ = store.ListOutbox(10)
	for _, item := range list {
		if item.ID == failed.ID && item.Text != "" {
			t.Errorf("failed outbox text = %q, want cleared after terminal state", item.Text)
		}
	}
}

func TestMedia(t *testing.T) {
	store := migrateTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordMedia(MediaItem{TGFileID: "file_1", FilePath: "/tmp/a.jpg", Type: "photo", ChatID: 10, SavedAt: now}); err != nil {
		t.Fatalf("RecordMedia() error: %v", err)
	}
	item, ok, err := store.GetMedia("file_1")
	if err != nil || !ok || item.FilePath != "/tmp/a.jpg" || item.Type != "photo" {
		t.Fatalf("GetMedia() = %+v, %v, %v", item, ok, err)
	}
	if _, ok, err := store.GetMedia("missing"); err != nil || ok {
		t.Errorf("GetMedia(missing) = ok:%v err:%v", ok, err)
	}
}
