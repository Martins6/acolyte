package board

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "board.sqlite.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time {
		base = base.Add(time.Minute)
		return base
	}
	return st
}

func mustCreatePost(t *testing.T, st *Store, actor Actor, in CreatePostInput) (Post, Entry) {
	t.Helper()
	post, entry, err := st.CreatePost(context.Background(), actor, in)
	if err != nil {
		t.Fatalf("CreatePost() error: %v", err)
	}
	return post, entry
}

func actorSet(actors []Actor) map[string]bool {
	set := make(map[string]bool, len(actors))
	for _, actor := range actors {
		set[actor.String()] = true
	}
	return set
}

func TestCreatePost(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")

	post, entry, err := st.CreatePost(context.Background(), valet, CreatePostInput{
		Text: "who here likes dogs\nsecond line",
		With: []string{"scribe", "scribe", "user"},
		Tags: []string{"Pets", "release", "pets"},
		Files: []File{{
			Name: "dogs.png",
			Path: "/tmp/dogs.png",
			Mode: FileReference,
			Size: 12,
		}},
	})
	if err != nil {
		t.Fatalf("CreatePost() error: %v", err)
	}
	if post.Title != "who here likes dogs" {
		t.Errorf("title = %q", post.Title)
	}
	if post.CreatedBy.String() != "agent:valet" {
		t.Errorf("created by = %s", post.CreatedBy.String())
	}
	if len(post.Tags) != 2 || post.Tags[0] != "pets" || post.Tags[1] != "release" {
		t.Errorf("tags = %v, want [pets release]", post.Tags)
	}
	participants := actorSet(post.Participants)
	for _, want := range []string{"agent:valet", "agent:scribe", "user:user"} {
		if !participants[want] {
			t.Errorf("participants = %v, missing %s", post.Participants, want)
		}
	}
	if len(post.Participants) != 3 {
		t.Errorf("participants = %v, want 3", post.Participants)
	}

	if entry.Seq != 0 || entry.Kind != EntryPost {
		t.Errorf("entry = %+v, want post seq 0", entry)
	}
	if entry.EnvelopeID == "" || !strings.HasPrefix(entry.EnvelopeID, "env_") {
		t.Errorf("envelope id = %q", entry.EnvelopeID)
	}
	if len(entry.Files) != 1 || entry.Files[0].Name != "dogs.png" {
		t.Errorf("files = %+v", entry.Files)
	}

	view, err := st.Show(context.Background(), AgentActor("scribe"), post.ID)
	if err != nil {
		t.Fatalf("Show() error: %v", err)
	}
	if len(view.Entries) != 1 || view.Entries[0].ID != entry.ID {
		t.Fatalf("entries = %+v", view.Entries)
	}
	if view.Subscribed {
		t.Error("scribe unexpectedly subscribed")
	}
	if view.Unread != 1 {
		t.Errorf("unread = %d, want 1", view.Unread)
	}

	state, err := st.SubscriptionState(context.Background(), "valet")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Posts) != 1 || state.Posts[0].PostID != post.ID || !state.Posts[0].Aware {
		t.Errorf("creator subscription = %+v", state)
	}
}

func TestCreatePostNoInbox(t *testing.T) {
	st := openTestStore(t)
	post, _, err := st.CreatePost(context.Background(), AgentActor("valet"), CreatePostInput{
		Text:    "quiet opener",
		NoInbox: true,
	})
	if err != nil {
		t.Fatalf("CreatePost() error: %v", err)
	}
	state, err := st.SubscriptionState(context.Background(), "valet")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Posts) != 0 {
		t.Errorf("subscriptions = %+v, want none", state.Posts)
	}
	if len(state.Mutes) != 1 || state.Mutes[0] != post.ID {
		t.Errorf("mutes = %v, want [%s]", state.Mutes, post.ID)
	}
}

func TestCreatePostValidation(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")

	tests := []struct {
		name string
		in   CreatePostInput
	}{
		{name: "empty", in: CreatePostInput{}},
		{name: "blank text", in: CreatePostInput{Text: "   "}},
		{name: "relative file", in: CreatePostInput{Text: "hi", Files: []File{{
			Name: "a.png", Path: "a.png", Mode: FileReference,
		}}}},
		{name: "invalid mode", in: CreatePostInput{Text: "hi", Files: []File{{
			Name: "a.png", Path: "/tmp/a.png", Mode: "link",
		}}}},
		{name: "invalid tag", in: CreatePostInput{Text: "hi", Tags: []string{"not a tag"}}},
		{name: "too many tags", in: CreatePostInput{Text: "hi", Tags: []string{
			"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k",
		}}},
		{name: "too many files", in: CreatePostInput{Text: "hi", Files: manyFiles(21)}},
		{name: "long title", in: CreatePostInput{Text: "hi", Title: strings.Repeat("x", 201)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := st.CreatePost(context.Background(), valet, tt.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CreatePost() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func manyFiles(count int) []File {
	files := make([]File, 0, count)
	for i := 0; i < count; i++ {
		files = append(files, File{
			Name: "file.png",
			Path: "/tmp/file.png",
			Mode: FileReference,
		})
	}
	return files
}

func TestDerivedTitleTruncates(t *testing.T) {
	st := openTestStore(t)
	post, _, err := st.CreatePost(context.Background(), AgentActor("valet"), CreatePostInput{
		Text: strings.Repeat("x", 100) + "\nsecond line",
	})
	if err != nil {
		t.Fatalf("CreatePost() error: %v", err)
	}
	if got := []rune(post.Title); len(got) != MaxTitleRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("title = %q (%d runes)", post.Title, len(got))
	}
}

func TestAppendMessage(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, postEntry := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "who here likes dogs",
		With: []string{"scribe"},
	})

	first, err := st.AppendMessage(context.Background(), scribe, post.ID[:8], AppendMessageInput{
		Text:      "I like dogs",
		ReplyToID: "#0",
	})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if first.Seq != 1 || first.ReplyToID != postEntry.ID {
		t.Errorf("first = %+v, want seq 1 reply to %s", first, postEntry.ID)
	}

	second, err := st.AppendMessage(context.Background(), valet, post.ID, AppendMessageInput{
		Text: "Same here",
	})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if second.Seq != 2 {
		t.Errorf("second seq = %d, want 2", second.Seq)
	}

	view, err := st.Show(context.Background(), scribe, post.ID)
	if err != nil {
		t.Fatalf("Show() error: %v", err)
	}
	if len(view.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(view.Entries))
	}
	for i, entry := range view.Entries {
		if entry.Seq != i {
			t.Errorf("entry %d seq = %d", i, entry.Seq)
		}
	}
}

func TestAppendMessageForbidden(t *testing.T) {
	st := openTestStore(t)
	post, postEntry := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{
		Text: "private",
		With: []string{"scribe"},
	})
	if _, err := st.AppendMessage(context.Background(), AgentActor("auditor"), post.ID, AppendMessageInput{
		Text: "hello",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AppendMessage() error = %v, want ErrForbidden", err)
	}

	other, _ := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{Text: "other"})
	if _, err := st.AppendMessage(context.Background(), AgentActor("scribe"), post.ID, AppendMessageInput{
		Text:      "wrong post",
		ReplyToID: "ent_" + other.ID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendMessage(cross-post reply) error = %v, want ErrNotFound", err)
	}
	if _, err := st.AppendMessage(context.Background(), AgentActor("scribe"), post.ID, AppendMessageInput{
		ReplyToID: postEntry.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AppendMessage(empty) error = %v, want ErrInvalid", err)
	}
}

func TestUnreadAndMarkRead(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "who here likes dogs",
		With: []string{"scribe"},
	})

	if err := st.MarkRead(context.Background(), scribe, post.ID, 0); err != nil {
		t.Fatalf("MarkRead() error: %v", err)
	}
	if view, err := st.Show(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Unread != 0 {
		t.Errorf("scribe unread = %d, want 0", view.Unread)
	}
	if view, err := st.Show(context.Background(), valet, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Unread != 0 {
		t.Errorf("valet unread = %d, want 0", view.Unread)
	}

	if _, err := st.AppendMessage(context.Background(), scribe, post.ID, AppendMessageInput{Text: "I like dogs"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if view, err := st.Show(context.Background(), valet, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Unread != 1 {
		t.Errorf("valet unread = %d, want 1", view.Unread)
	}
	if view, err := st.Show(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Unread != 0 {
		t.Errorf("scribe unread = %d, want 0 (own message)", view.Unread)
	}

	if err := st.MarkRead(context.Background(), valet, post.ID, 99); err != nil {
		t.Fatalf("MarkRead() error: %v", err)
	}
	if view, err := st.Show(context.Background(), valet, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Unread != 0 {
		t.Errorf("valet unread after clamp = %d, want 0", view.Unread)
	}
}

func TestListFilters(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	auditor := AgentActor("auditor")

	dogs, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "Dogs", With: []string{"scribe"}, Tags: []string{"pets"},
	})
	launch, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "Launch", With: []string{"scribe"}, Tags: []string{"release"},
	})
	cats, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "Cats", With: []string{"scribe", "auditor"}, Tags: []string{"pets", "release"},
	})
	if _, err := st.AppendMessage(context.Background(), scribe, dogs.ID, AppendMessageInput{Text: "woof"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.AppendMessage(context.Background(), scribe, launch.ID, AppendMessageInput{Text: "ready"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}

	tests := []struct {
		name   string
		actor  Actor
		filter ListFilter
		want   []string
	}{
		{name: "all participant", actor: scribe, filter: ListFilter{}, want: []string{launch.ID, dogs.ID, cats.ID}},
		{name: "non participant", actor: AgentActor("ghost"), filter: ListFilter{}, want: nil},
		{name: "tag pets", actor: scribe, filter: ListFilter{Tags: []string{"pets"}}, want: []string{dogs.ID, cats.ID}},
		{name: "tags all", actor: scribe, filter: ListFilter{Tags: []string{"pets", "release"}}, want: []string{cats.ID}},
		{name: "search body", actor: scribe, filter: ListFilter{Search: "woof"}, want: []string{dogs.ID}},
		{name: "search title", actor: scribe, filter: ListFilter{Search: "launch"}, want: []string{launch.ID}},
		{name: "limit", actor: scribe, filter: ListFilter{Limit: 1}, want: []string{launch.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.List(context.Background(), tt.actor, tt.filter)
			if err != nil {
				t.Fatalf("List() error: %v", err)
			}
			var ids []string
			for _, summary := range got {
				ids = append(ids, summary.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Errorf("ids = %v, want %v", ids, tt.want)
			}
		})
	}

	if _, err := st.List(context.Background(), scribe, ListFilter{Search: "%"}); err != nil {
		t.Errorf("List(percent search) error: %v", err)
	}
	if _, err := st.List(context.Background(), scribe, ListFilter{Tags: []string{"bad tag"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("List(invalid tag) error = %v, want ErrInvalid", err)
	}

	unread, err := st.List(context.Background(), auditor, ListFilter{Unread: true})
	if err != nil {
		t.Fatalf("List(unread) error: %v", err)
	}
	if len(unread) != 1 || unread[0].ID != cats.ID {
		t.Errorf("auditor unread = %+v, want cats", unread)
	}
}

func TestListTimeFilters(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	first, _ := mustCreatePost(t, st, valet, CreatePostInput{Text: "first"})
	second, _ := mustCreatePost(t, st, valet, CreatePostInput{Text: "second"})

	since := second.CreatedAt
	got, err := st.List(context.Background(), valet, ListFilter{Since: &since})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(got) != 1 || got[0].ID != second.ID {
		t.Errorf("since = %+v, want second", got)
	}

	until := second.CreatedAt
	got, err = st.List(context.Background(), valet, ListFilter{Until: &until})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(got) != 1 || got[0].ID != first.ID {
		t.Errorf("until = %+v, want first", got)
	}
}

func TestInviteLeave(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	auditor := AgentActor("auditor")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "invite me",
		With: []string{"scribe"},
	})

	added, err := st.Invite(context.Background(), scribe, post.ID, []string{"auditor", "auditor", "user", ""})
	if err != nil {
		t.Fatalf("Invite() error: %v", err)
	}
	if len(added) != 1 || added[0] != "auditor" {
		t.Errorf("added = %v, want [auditor]", added)
	}
	if _, err := st.Show(context.Background(), auditor, post.ID); err != nil {
		t.Fatalf("Show() as invited error: %v", err)
	}

	if err := st.Leave(context.Background(), auditor, post.ID); err != nil {
		t.Fatalf("Leave() error: %v", err)
	}
	if _, err := st.Show(context.Background(), auditor, post.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Show() after leave error = %v, want ErrForbidden", err)
	}
	if err := st.Leave(context.Background(), UserActor(), post.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Leave(user) error = %v, want ErrInvalid", err)
	}
}

func TestTags(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	initial := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "tags",
		With: []string{"scribe"},
		Tags: initial,
	})

	tags, err := st.AddTags(context.Background(), scribe, post.ID, []string{"J", "j"})
	if err != nil {
		t.Fatalf("AddTags() error: %v", err)
	}
	if len(tags) != 10 {
		t.Errorf("tags = %v, want 10", tags)
	}
	if _, err := st.AddTags(context.Background(), scribe, post.ID, []string{"k"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("AddTags(11) error = %v, want ErrInvalid", err)
	}
	tags, err = st.RemoveTags(context.Background(), scribe, post.ID, []string{"a", "b"})
	if err != nil {
		t.Fatalf("RemoveTags() error: %v", err)
	}
	if len(tags) != 8 {
		t.Errorf("tags = %v, want 8", tags)
	}
	if _, err := st.AddTags(context.Background(), AgentActor("ghost"), post.ID, []string{"x"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("AddTags(non participant) error = %v, want ErrForbidden", err)
	}
}

func TestSubscriptions(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "subscribe",
		With: []string{"scribe"},
	})
	second, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "second",
		With: []string{"scribe"},
	})

	state, err := st.SubscriptionState(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Posts) != 0 || state.All != nil {
		t.Fatalf("initial subscriptions = %+v, want none", state)
	}

	if err := st.Subscribe(context.Background(), scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	if view, err := st.Show(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if !view.Subscribed || !view.Aware {
		t.Errorf("subscribed=%v aware=%v, want true/true", view.Subscribed, view.Aware)
	}

	if err := st.Unsubscribe(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("Unsubscribe() error: %v", err)
	}
	if view, err := st.Show(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if view.Subscribed {
		t.Error("still subscribed after unsubscribe")
	}
	state, err = st.SubscriptionState(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Mutes) != 1 || state.Mutes[0] != post.ID {
		t.Errorf("mutes = %v", state.Mutes)
	}

	if err := st.Subscribe(context.Background(), scribe, post.ID, false); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	state, err = st.SubscriptionState(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Mutes) != 0 {
		t.Errorf("mutes after subscribe = %v, want none", state.Mutes)
	}

	if err := st.SubscribeAll(context.Background(), scribe, false); err != nil {
		t.Fatalf("SubscribeAll() error: %v", err)
	}
	state, err = st.SubscriptionState(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if state.All == nil || state.All.Aware {
		t.Errorf("all subscription = %+v, want silent", state.All)
	}
	if view, err := st.Show(context.Background(), scribe, second.ID); err != nil {
		t.Fatalf("Show() error: %v", err)
	} else if !view.Subscribed || view.Aware {
		t.Errorf("second subscribed=%v aware=%v, want true/false", view.Subscribed, view.Aware)
	}

	if err := st.UnsubscribeAll(context.Background(), scribe); err != nil {
		t.Fatalf("UnsubscribeAll() error: %v", err)
	}
	state, err = st.SubscriptionState(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if state.All != nil || len(state.Posts) != 0 {
		t.Errorf("subscriptions after unsubscribe all = %+v", state)
	}
}

func TestAutomation(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "automate",
		With: []string{"scribe"},
	})

	if err := st.SetAutomation(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	if err := st.SetAutomation(context.Background(), AgentActor("ghost"), post.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetAutomation(non participant) error = %v, want ErrForbidden", err)
	}
	if err := st.ClearAutomation(context.Background(), AgentActor("ghost"), post.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("ClearAutomation(non participant) error = %v, want ErrForbidden", err)
	}
	if err := st.ResetAutomation(context.Background(), AgentActor("ghost"), post.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("ResetAutomation(non participant) error = %v, want ErrForbidden", err)
	}
	automation, err := st.Automation(context.Background(), "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation == nil || automation.Paused() || automation.Turns != 0 {
		t.Fatalf("automation = %+v", automation)
	}
	if _, err := st.DB().Exec(`UPDATE brd_automations SET session_id = 'ses_1', turns = 5 WHERE agent_id = 'scribe' AND post_id = ?`, post.ID); err != nil {
		t.Fatalf("seed automation: %v", err)
	}
	if err := st.SetAutomation(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	automation, err = st.Automation(context.Background(), "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.SessionID != "ses_1" || automation.Turns != 5 {
		t.Errorf("automation after re-enable = %+v", automation)
	}

	if _, err := st.DB().Exec(`UPDATE brd_automations SET paused_at = ?, pause_reason = 'cap' WHERE agent_id = 'scribe' AND post_id = ?`, time.Now().UTC(), post.ID); err != nil {
		t.Fatalf("pause automation: %v", err)
	}
	if err := st.SetAutomation(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	automation, err = st.Automation(context.Background(), "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.Paused() || automation.Turns != 0 {
		t.Errorf("automation after resume = %+v, want unpaused turns 0", automation)
	}

	if err := st.ResetAutomation(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("ResetAutomation() error: %v", err)
	}
	automation, err = st.Automation(context.Background(), "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.SessionID != "" || automation.Turns != 0 {
		t.Errorf("automation after reset = %+v", automation)
	}

	if err := st.ClearAutomation(context.Background(), scribe, post.ID); err != nil {
		t.Fatalf("ClearAutomation() error: %v", err)
	}
	automation, err = st.Automation(context.Background(), "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation != nil {
		t.Errorf("automation after clear = %+v, want nil", automation)
	}
	if err := st.ResetAutomation(context.Background(), scribe, post.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ResetAutomation(absent) error = %v, want ErrNotFound", err)
	}
}

func TestEnqueue(t *testing.T) {
	st := openTestStore(t)
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	auditor := AgentActor("auditor")
	post, postEntry := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "deliver",
		With: []string{"scribe", "auditor"},
	})
	if err := st.Subscribe(context.Background(), scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe(scribe) error: %v", err)
	}
	if err := st.Subscribe(context.Background(), auditor, post.ID, false); err != nil {
		t.Fatalf("Subscribe(auditor) error: %v", err)
	}

	deliveries, err := st.Enqueue(context.Background(), postEntry)
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	byAgent := map[string]Delivery{}
	for _, delivery := range deliveries {
		byAgent[delivery.AgentID] = delivery
	}
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want 2", deliveries)
	}
	if got := byAgent["scribe"]; got.Mode != "manual" || !got.Aware {
		t.Errorf("scribe delivery = %+v", got)
	}
	if got := byAgent["auditor"]; got.Mode != "manual" || got.Aware {
		t.Errorf("auditor delivery = %+v", got)
	}
	if _, ok := byAgent["valet"]; ok {
		t.Error("author valet was enqueued")
	}

	message, err := st.AppendMessage(context.Background(), scribe, post.ID, AppendMessageInput{Text: "reply"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if err := st.SetAutomation(context.Background(), auditor, post.ID); err != nil {
		t.Fatalf("SetAutomation(auditor) error: %v", err)
	}
	deliveries, err = st.Enqueue(context.Background(), message)
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	byAgent = map[string]Delivery{}
	for _, delivery := range deliveries {
		byAgent[delivery.AgentID] = delivery
	}
	if got := byAgent["auditor"]; got.Mode != "automated" || got.Aware {
		t.Errorf("auditor automated delivery = %+v", got)
	}
	if _, ok := byAgent["scribe"]; ok {
		t.Error("author scribe was enqueued for their own message")
	}

	if err := st.Mute(context.Background(), auditor, post.ID); err != nil {
		t.Fatalf("Mute() error: %v", err)
	}
	next, err := st.AppendMessage(context.Background(), valet, post.ID, AppendMessageInput{Text: "next"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	deliveries, err = st.Enqueue(context.Background(), next)
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	for _, delivery := range deliveries {
		if delivery.AgentID == "auditor" {
			t.Errorf("muted auditor was enqueued: %+v", delivery)
		}
	}

	inbox, err := st.Inbox(context.Background(), "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 2 {
		t.Errorf("scribe inbox = %d, want 2", len(inbox))
	}
	if err := st.MarkInboxRead(context.Background(), "scribe", post.ID); err != nil {
		t.Fatalf("MarkInboxRead() error: %v", err)
	}
	inbox, err = st.Inbox(context.Background(), "scribe", false)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("unread scribe inbox = %d, want 0", len(inbox))
	}

	if err := st.MarkInboxProcessed(context.Background(), "auditor", postEntry.ID, "ses_1", "msg_1"); err != nil {
		t.Fatalf("MarkInboxProcessed() error: %v", err)
	}
	if err := st.MarkInboxProcessed(context.Background(), "auditor", message.ID, "ses_1", "msg_2"); err != nil {
		t.Fatalf("MarkInboxProcessed() error: %v", err)
	}
	pending, err := st.PendingAutomated(context.Background(), "auditor", post.ID, 10)
	if err != nil {
		t.Fatalf("PendingAutomated() error: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %v, want none", pending)
	}
	if err := st.MarkInboxError(context.Background(), "auditor", message.ID, "boom"); err != nil {
		t.Fatalf("MarkInboxError() error: %v", err)
	}

	pings, posts, alerts, err := st.UnreadCount(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("UnreadCount() error: %v", err)
	}
	if pings != 0 || posts != 0 || alerts != 0 {
		t.Errorf("unread count = %d/%d/%d, want 0/0/0", pings, posts, alerts)
	}
	if _, err := st.CreateAlert(context.Background(), "scribe", post.ID, "automation paused"); err != nil {
		t.Fatalf("CreateAlert() error: %v", err)
	}
	pings, posts, alerts, err = st.UnreadCount(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("UnreadCount() error: %v", err)
	}
	if alerts != 1 {
		t.Errorf("alerts = %d, want 1", alerts)
	}
	alertsList, err := st.Alerts(context.Background(), "scribe")
	if err != nil {
		t.Fatalf("Alerts() error: %v", err)
	}
	if len(alertsList) != 1 {
		t.Fatalf("alerts list = %+v", alertsList)
	}
	if err := st.MarkAlertsNotified(context.Background(), []string{alertsList[0].ID}); err != nil {
		t.Fatalf("MarkAlertsNotified() error: %v", err)
	}
}

func TestLeaveRevokesState(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "leave",
		With: []string{"scribe"},
	})
	if err := st.Subscribe(ctx, scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	if err := st.SetAutomation(ctx, scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	entry, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "ping"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.Enqueue(ctx, entry); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, err := st.CreateAlert(ctx, "scribe", post.ID, "alert"); err != nil {
		t.Fatalf("CreateAlert() error: %v", err)
	}

	if err := st.Leave(ctx, scribe, post.ID); err != nil {
		t.Fatalf("Leave() error: %v", err)
	}
	if inbox, err := st.Inbox(ctx, "scribe", true); err != nil {
		t.Fatalf("Inbox() error: %v", err)
	} else if len(inbox) != 0 {
		t.Errorf("inbox after leave = %+v", inbox)
	}
	state, err := st.SubscriptionState(ctx, "scribe")
	if err != nil {
		t.Fatalf("SubscriptionState() error: %v", err)
	}
	if len(state.Posts) != 0 || len(state.Mutes) != 0 {
		t.Errorf("subscriptions after leave = %+v", state)
	}
	automation, err := st.Automation(ctx, "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation != nil {
		t.Errorf("automation after leave = %+v", automation)
	}
	if alerts, err := st.Alerts(ctx, "scribe"); err != nil {
		t.Fatalf("Alerts() error: %v", err)
	} else if len(alerts) != 0 {
		t.Errorf("alerts after leave = %+v", alerts)
	}

	var count int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM brd_inbox WHERE agent_id = 'scribe' AND post_id = ?`, post.ID,
	).Scan(&count); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if count != 0 {
		t.Errorf("inbox rows after leave = %d", count)
	}
}

func TestInboxRequiresParticipation(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "participation",
		With: []string{"scribe"},
	})
	if err := st.SetAutomation(ctx, scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	entry, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "ping"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.Enqueue(ctx, entry); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM brd_participants WHERE post_id = ? AND kind = 'agent' AND id = 'scribe'`, post.ID,
	); err != nil {
		t.Fatalf("delete participant: %v", err)
	}
	if inbox, err := st.Inbox(ctx, "scribe", true); err != nil {
		t.Fatalf("Inbox() error: %v", err)
	} else if len(inbox) != 0 {
		t.Errorf("inbox for non-participant = %+v", inbox)
	}
	if entryID, _, err := st.ClaimPendingAutomated(ctx, "scribe", post.ID, "claim", time.Minute); err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	} else if entryID != "" {
		t.Errorf("claimed for non-participant: %q", entryID)
	}
	if pings, _, _, err := st.UnreadCount(ctx, "scribe"); err != nil {
		t.Fatalf("UnreadCount() error: %v", err)
	} else if pings != 0 {
		t.Errorf("unread count for non-participant = %d", pings)
	}
}

func TestRecordAutomationAdmissionDoesNotDoubleCount(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	post, _ := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{
		Text: "reconcile",
		With: []string{"scribe"},
	})
	if err := st.SetAutomation(ctx, AgentActor("scribe"), post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	ok, err := st.ReserveAutomationTurn(ctx, "scribe", post.ID, 2*time.Minute, 20)
	if err != nil || !ok {
		t.Fatalf("ReserveAutomationTurn() = %v, %v", ok, err)
	}
	if err := st.RecordAutomationAdmission(ctx, "scribe", post.ID, "ses_x"); err != nil {
		t.Fatalf("RecordAutomationAdmission() error: %v", err)
	}
	automation, err := st.Automation(ctx, "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.Turns != 1 {
		t.Errorf("turns = %d, want 1 (reservation already counted)", automation.Turns)
	}
	if automation.SessionID != "ses_x" {
		t.Errorf("session = %q, want ses_x", automation.SessionID)
	}
}

func TestMarkInboxPromotedOnlyTargets(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "promote target",
		With: []string{"scribe"},
	})
	if err := st.Subscribe(ctx, scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	first, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "first"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	second, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "second"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	for _, entry := range []Entry{first, second} {
		if _, err := st.Enqueue(ctx, entry); err != nil {
			t.Fatalf("Enqueue() error: %v", err)
		}
	}
	token := "promote-token"
	if _, err := st.ClaimPromotion(ctx, "scribe", post.ID, token, boardPromotionMarkerForTest(token)); err != nil {
		t.Fatalf("ClaimPromotion() error: %v", err)
	}
	if err := st.MarkInboxPromoted(ctx, "scribe", []string{first.ID}, "ses_x"); err != nil {
		t.Fatalf("MarkInboxPromoted() error: %v", err)
	}
	unread, err := st.Inbox(ctx, "scribe", false)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(unread) != 1 || unread[0].EntryID != second.ID {
		t.Fatalf("unread = %+v, want only the second entry", unread)
	}
	all, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox(all) error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all inbox = %+v", all)
	}
	for _, item := range all {
		if item.EntryID == first.ID {
			if item.ReadAt == nil || item.PromotionSessionID != "ses_x" {
				t.Errorf("promoted row = %+v", item)
			}
		}
	}
}

func boardPromotionMarkerForTest(token string) string {
	return "[[acolyte-board-promote:" + token + "]]"
}

func TestReconcileInbox(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "reconcile",
		With: []string{"scribe"},
	})
	if err := st.Subscribe(ctx, scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	entry, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "lost enqueue"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}

	count, err := st.ReconcileInbox(ctx, "scribe")
	if err != nil {
		t.Fatalf("ReconcileInbox() error: %v", err)
	}
	if count != 1 {
		t.Fatalf("reconciled = %d, want 1", count)
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 1 || inbox[0].EntryID != entry.ID || inbox[0].Mode != "manual" || !inbox[0].Aware {
		t.Fatalf("inbox = %+v", inbox)
	}
	count, err = st.ReconcileInbox(ctx, "scribe")
	if err != nil || count != 0 {
		t.Fatalf("second reconcile = %d, %v, want 0", count, err)
	}
}

func TestReconcileInboxRespectsSubscriptionTimeAndMute(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "cutoff",
		With: []string{"scribe"},
	})
	if _, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "before subscribe"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if err := st.Subscribe(ctx, scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	if count, err := st.ReconcileInbox(ctx, "scribe"); err != nil || count != 0 {
		t.Fatalf("pre-subscription reconcile = %d, %v, want 0", count, err)
	}
	if _, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "after subscribe"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if err := st.Mute(ctx, scribe, post.ID); err != nil {
		t.Fatalf("Mute() error: %v", err)
	}
	if _, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "while muted"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if count, err := st.ReconcileInbox(ctx, "scribe"); err != nil || count != 0 {
		t.Fatalf("muted reconcile = %d, %v, want 0", count, err)
	}
	if err := st.Subscribe(ctx, scribe, post.ID, true); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}
	afterUnmute, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "after unmute"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if count, err := st.ReconcileInbox(ctx, "scribe"); err != nil || count != 1 {
		t.Fatalf("unmuted reconcile = %d, %v, want 1", count, err)
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 1 || inbox[0].EntryID != afterUnmute.ID {
		t.Errorf("inbox = %+v, want the after-unmute entry", inbox)
	}
}

func TestReconcileInboxAutomated(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "automated reconcile",
		With: []string{"scribe"},
	})
	if err := st.SetAutomation(ctx, scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	if _, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "lost automated"}); err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	count, err := st.ReconcileInbox(ctx, "scribe")
	if err != nil || count != 1 {
		t.Fatalf("reconcile = %d, %v, want 1", count, err)
	}
	inbox, err := st.Inbox(ctx, "scribe", true)
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(inbox) != 1 || inbox[0].Mode != "automated" || inbox[0].Aware {
		t.Fatalf("inbox = %+v", inbox)
	}
}

func TestReserveAutomationTurn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	post, _ := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{
		Text: "reserve",
		With: []string{"scribe"},
	})
	if err := st.SetAutomation(ctx, AgentActor("scribe"), post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}

	ok, err := st.ReserveAutomationTurn(ctx, "scribe", post.ID, 2*time.Minute, 20)
	if err != nil || !ok {
		t.Fatalf("first reserve = %v, %v", ok, err)
	}
	ok, err = st.ReserveAutomationTurn(ctx, "scribe", post.ID, 2*time.Minute, 20)
	if err != nil || ok {
		t.Fatalf("cooldown reserve = %v, %v, want false", ok, err)
	}
	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET last_turn_at = ? WHERE agent_id = 'scribe' AND post_id = ?`,
		st.now().Add(-time.Hour).UTC(), post.ID,
	); err != nil {
		t.Fatalf("age reserve: %v", err)
	}
	ok, err = st.ReserveAutomationTurn(ctx, "scribe", post.ID, 2*time.Minute, 20)
	if err != nil || !ok {
		t.Fatalf("second reserve = %v, %v", ok, err)
	}
	automation, err := st.Automation(ctx, "scribe", post.ID)
	if err != nil {
		t.Fatalf("Automation() error: %v", err)
	}
	if automation.Turns != 2 {
		t.Errorf("turns = %d, want 2", automation.Turns)
	}

	if _, err := st.DB().Exec(
		`UPDATE brd_automations SET turns = 20, last_turn_at = ? WHERE agent_id = 'scribe' AND post_id = ?`,
		st.now().Add(-time.Hour).UTC(), post.ID,
	); err != nil {
		t.Fatalf("seed cap: %v", err)
	}
	ok, err = st.ReserveAutomationTurn(ctx, "scribe", post.ID, 30*time.Second, 20)
	if err != nil || ok {
		t.Fatalf("cap reserve = %v, %v, want false", ok, err)
	}
}

func TestClaimPendingAutomated(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	valet := AgentActor("valet")
	scribe := AgentActor("scribe")
	post, _ := mustCreatePost(t, st, valet, CreatePostInput{
		Text: "claim",
		With: []string{"scribe"},
	})
	if err := st.SetAutomation(ctx, scribe, post.ID); err != nil {
		t.Fatalf("SetAutomation() error: %v", err)
	}
	entry, err := st.AppendMessage(ctx, valet, post.ID, AppendMessageInput{Text: "ping"})
	if err != nil {
		t.Fatalf("AppendMessage() error: %v", err)
	}
	if _, err := st.Enqueue(ctx, entry); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}

	claimed, stale, err := st.ClaimPendingAutomated(ctx, "scribe", post.ID, "claim-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if claimed != entry.ID || stale {
		t.Fatalf("first claim = %q stale=%v", claimed, stale)
	}
	claimed, stale, err = st.ClaimPendingAutomated(ctx, "scribe", post.ID, "claim-2", time.Hour)
	if err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if claimed != "" || stale {
		t.Fatalf("second claim = %q stale=%v, want none", claimed, stale)
	}

	if _, err := st.DB().Exec(
		`UPDATE brd_inbox SET claimed_at = ? WHERE agent_id = 'scribe' AND entry_id = ?`,
		st.now().Add(-2*time.Hour).UTC(), entry.ID,
	); err != nil {
		t.Fatalf("age claim: %v", err)
	}
	claimed, stale, err = st.ClaimPendingAutomated(ctx, "scribe", post.ID, "claim-3", time.Hour)
	if err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if claimed != entry.ID || !stale {
		t.Fatalf("stale claim = %q stale=%v", claimed, stale)
	}
	if err := st.MarkInboxProcessed(ctx, "scribe", entry.ID, "ses_x", "msg_x"); err != nil {
		t.Fatalf("MarkInboxProcessed() error: %v", err)
	}
	claimed, stale, err = st.ClaimPendingAutomated(ctx, "scribe", post.ID, "claim-4", time.Hour)
	if err != nil {
		t.Fatalf("ClaimPendingAutomated() error: %v", err)
	}
	if claimed != "" || stale {
		t.Fatalf("post-process claim = %q stale=%v, want none", claimed, stale)
	}
}

func TestReplyToStrict(t *testing.T) {
	st := openTestStore(t)
	post, _ := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{
		Text: "strict",
		With: []string{"scribe"},
	})
	if _, err := st.AppendMessage(context.Background(), AgentActor("scribe"), post.ID, AppendMessageInput{
		Text:      "bad reference",
		ReplyToID: "#1junk",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AppendMessage() error = %v, want ErrInvalid", err)
	}
}

func TestResolvePrefixes(t *testing.T) {
	st := openTestStore(t)
	now := st.now()
	for _, id := range []string{"brd_abc111", "brd_abc222"} {
		if _, err := st.DB().Exec(
			`INSERT INTO brd_posts (id, title, created_by_kind, created_by_id, created_at, updated_at)
			 VALUES (?, 't', 'agent', 'valet', ?, ?)`, id, now, now,
		); err != nil {
			t.Fatalf("insert post %s: %v", id, err)
		}
	}
	if _, err := st.ResolvePost(context.Background(), "brd_abc"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ResolvePost(ambiguous) error = %v, want ErrAmbiguous", err)
	}
	if post, err := st.ResolvePost(context.Background(), "brd_abc111"); err != nil || post.ID != "brd_abc111" {
		t.Fatalf("ResolvePost(exact) = %+v, %v", post, err)
	}
	if _, err := st.ResolvePost(context.Background(), "brd_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolvePost(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := st.ResolvePost(context.Background(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ResolvePost(empty) error = %v, want ErrInvalid", err)
	}

	post, entry := mustCreatePost(t, st, AgentActor("valet"), CreatePostInput{Text: "entries"})
	if got, err := st.ResolveEntry(context.Background(), post.ID, "#0"); err != nil || got.ID != entry.ID {
		t.Fatalf("ResolveEntry(#0) = %+v, %v", got, err)
	}
	if _, err := st.ResolveEntry(context.Background(), post.ID, "#9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveEntry(#9) error = %v, want ErrNotFound", err)
	}
}
