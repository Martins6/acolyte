package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martins6/acolyte/internal/agent"
	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/paths"
)

func setupBoardCLI(t *testing.T) (valetWS, scribeWS string) {
	t.Helper()
	valetWS = setupCronCLI(t)
	scribeWS = filepath.Join(t.TempDir(), "scribe")
	cfg, err := agent.ParseConfig(`{}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{
		Name:      "scribe",
		Workspace: scribeWS,
		Config:    cfg,
	}); err != nil {
		t.Fatalf("Create(scribe) error: %v", err)
	}
	return valetWS, scribeWS
}

func runBoardPostCLI(t *testing.T, opts boardPostOptions) (boardPostResult, string) {
	t.Helper()
	var out bytes.Buffer
	if err := runBoardPost(context.Background(), &out, opts); err != nil {
		t.Fatalf("runBoardPost() error: %v", err)
	}
	var result boardPostResult
	if opts.JSON {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode post JSON: %v\n%s", err, out.String())
		}
	}
	return result, out.String()
}

func TestResolveBoardActor(t *testing.T) {
	valetWS, _ := setupBoardCLI(t)
	t.Setenv(paths.WorkspaceEnvVar, "")

	actor, err := resolveBoardActor("", "", "user")
	if err != nil {
		t.Fatalf("resolveBoardActor(user) error: %v", err)
	}
	if actor != board.UserActor() {
		t.Errorf("actor = %+v, want user", actor)
	}

	actor, err = resolveBoardActor("valet", "", "")
	if err != nil {
		t.Fatalf("resolveBoardActor(name) error: %v", err)
	}
	if actor != board.AgentActor("valet") {
		t.Errorf("actor = %+v, want agent:valet", actor)
	}

	actor, err = resolveBoardActor("", valetWS, "")
	if err != nil {
		t.Fatalf("resolveBoardActor(workspace) error: %v", err)
	}
	if actor != board.AgentActor("valet") {
		t.Errorf("actor = %+v, want agent:valet", actor)
	}

	t.Setenv(paths.WorkspaceEnvVar, valetWS)
	actor, err = resolveBoardActor("", "", "")
	if err != nil {
		t.Fatalf("resolveBoardActor(env) error: %v", err)
	}
	if actor != board.AgentActor("valet") {
		t.Errorf("actor = %+v, want agent:valet", actor)
	}
	t.Setenv(paths.WorkspaceEnvVar, "")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(filepath.Join(valetWS, ".acolyte")); err != nil {
		t.Fatalf("Chdir() error: %v", err)
	}
	actor, err = resolveBoardActor("", "", "")
	if err != nil {
		t.Fatalf("resolveBoardActor(walk-up) error: %v", err)
	}
	if actor != board.AgentActor("valet") {
		t.Errorf("actor = %+v, want agent:valet", actor)
	}

	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir() error: %v", err)
	}
	if _, err := resolveBoardActor("", "", ""); err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("resolveBoardActor(unknown) error = %v", err)
	}
	if _, err := resolveBoardActor("valet", valetWS, ""); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("resolveBoardActor(both) error = %v", err)
	}
	if _, err := resolveBoardActor("valet", "", "user"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("resolveBoardActor(as+name) error = %v", err)
	}
	if _, err := resolveBoardActor("ghost", "", ""); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("resolveBoardActor(unknown agent) error = %v", err)
	}
}

func TestBoardPostMessageShow(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, out := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "who here likes dogs",
		With:   []string{"scribe"},
		Tags:   []string{"pets"},
		JSON:   true,
	})
	if result.Post.ID == "" || result.Entry.Seq != 0 {
		t.Fatalf("post result = %+v", result)
	}
	if !strings.Contains(out, result.Post.ID) {
		t.Errorf("post output missing id: %s", out)
	}

	var listOut bytes.Buffer
	if err := runBoardList(context.Background(), &listOut, boardListOptions{
		Target: scribe,
		Tags:   []string{"pets"},
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardList() error: %v", err)
	}
	var list boardListResult
	if err := json.Unmarshal(listOut.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, listOut.String())
	}
	if len(list.Posts) != 1 || list.Posts[0].ID != result.Post.ID {
		t.Fatalf("list = %+v", list.Posts)
	}

	var showOut bytes.Buffer
	if err := runBoardShow(context.Background(), &showOut, boardShowOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardShow() error: %v", err)
	}
	if !strings.Contains(showOut.String(), "who here likes dogs") {
		t.Errorf("show output = %q", showOut.String())
	}

	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Text:   "I like dogs",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	showOut.Reset()
	if err := runBoardShow(context.Background(), &showOut, boardShowOptions{
		Target: valet,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardShow() error: %v", err)
	}
	if !strings.Contains(showOut.String(), "[#1]") || !strings.Contains(showOut.String(), "I like dogs") {
		t.Errorf("show output = %q", showOut.String())
	}

	if err := runBoardShow(context.Background(), &bytes.Buffer{}, boardShowOptions{
		Target: boardTarget{AgentName: "scribe"},
		Post:   "brd_nope",
	}); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("runBoardShow(missing) error = %v, want ErrNotFound", err)
	}
}

func TestSanitizeTerminal(t *testing.T) {
	input := "ok\x1b[31mred\x07\r\n\ttext\u202e"
	want := "ok[31mred\n\ttext"
	if got := sanitizeTerminal(input); got != want {
		t.Fatalf("sanitizeTerminal() = %q, want %q", got, want)
	}
}

func TestBoardShowStripsTerminalControls(t *testing.T) {
	view := board.PostView{
		Post: board.Post{
			ID:    "brd_x",
			Title: "\x1b[2Jtitle",
		},
		Entries: []board.Entry{{
			ID:     "ent_x",
			Kind:   board.EntryPost,
			Author: board.Actor{Kind: board.ActorAgent, ID: "valet"},
			Text:   "\x1b]0;pwned\x07hello",
			Files: []board.File{{
				Name: "\x1b[31mfile",
				Path: "/tmp/\x07x",
				Mode: board.FileReference,
			}},
		}},
	}
	var out bytes.Buffer
	printBoardView(&out, view, 0)
	if strings.ContainsRune(out.String(), '\x1b') || strings.ContainsRune(out.String(), '\x07') {
		t.Errorf("output contains terminal controls: %q", out.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("output lost entry text: %q", out.String())
	}
}

func TestBoardUserActor(t *testing.T) {
	setupBoardCLI(t)
	user := boardTarget{As: "user"}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: user,
		Text:   "from the user",
		JSON:   true,
	})
	if result.Post.CreatedBy != board.UserActor() {
		t.Errorf("created by = %+v, want user", result.Post.CreatedBy)
	}
	if len(result.Post.Participants) != 1 || result.Post.Participants[0] != board.UserActor() {
		t.Errorf("participants = %+v, want [user]", result.Post.Participants)
	}

	var out bytes.Buffer
	if err := runBoardList(context.Background(), &out, boardListOptions{
		Target: user,
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardList() error: %v", err)
	}
	var list boardListResult
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Posts) != 1 || list.Posts[0].ID != result.Post.ID {
		t.Errorf("list = %+v", list.Posts)
	}
}

func TestBoardCopyAttachment(t *testing.T) {
	setupBoardCLI(t)
	source := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(source, []byte("board notes"), 0600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: boardTarget{AgentName: "valet"},
		Text:   "with a file",
		Files:  []string{source},
		Copy:   true,
		JSON:   true,
	})
	if len(result.Entry.Files) != 1 {
		t.Fatalf("files = %+v", result.Entry.Files)
	}
	file := result.Entry.Files[0]
	if file.Mode != board.FileCopy || file.SHA256 == "" || file.SourcePath == "" {
		t.Errorf("file = %+v, want copy with hash", file)
	}
	home := os.Getenv(paths.HomeEnv)
	if !strings.HasPrefix(file.Path, filepath.Join(home, "board", "files")) {
		t.Errorf("path = %q, want under board/files", file.Path)
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(data) != "board notes" {
		t.Errorf("content = %q", data)
	}
}

func TestBoardNonParticipantForbidden(t *testing.T) {
	setupBoardCLI(t)
	cfg, err := agent.ParseConfig(`{}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{
		Name:      "auditor",
		Workspace: filepath.Join(t.TempDir(), "auditor"),
		Config:    cfg,
	}); err != nil {
		t.Fatalf("Create(auditor) error: %v", err)
	}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: boardTarget{AgentName: "valet"},
		Text:   "private",
		With:   []string{"scribe"},
		JSON:   true,
	})
	err = runBoardShow(context.Background(), &bytes.Buffer{}, boardShowOptions{
		Target: boardTarget{AgentName: "auditor"},
		Post:   result.Post.ID,
	})
	if !errors.Is(err, board.ErrForbidden) {
		t.Errorf("runBoardShow(non participant) error = %v, want ErrForbidden", err)
	}
}

func TestBoardInboxSubscribeAndRead(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}

	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "inbox",
		With:   []string{"scribe"},
		JSON:   true,
	})
	if err := runBoardSubscribe(context.Background(), &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Aware:  true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe() error: %v", err)
	}
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}

	var inboxOut bytes.Buffer
	if err := runBoardInbox(context.Background(), &inboxOut, boardInboxOptions{
		Target: scribe,
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardInbox() error: %v", err)
	}
	var inbox boardInboxResult
	if err := json.Unmarshal(inboxOut.Bytes(), &inbox); err != nil {
		t.Fatalf("decode inbox: %v\n%s", err, inboxOut.String())
	}
	if len(inbox.Items) != 1 || !inbox.Items[0].Aware {
		t.Fatalf("inbox = %+v", inbox.Items)
	}

	if err := runBoardInboxRead(context.Background(), &bytes.Buffer{}, boardInboxReadOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardInboxRead() error: %v", err)
	}
	inboxOut.Reset()
	if err := runBoardInbox(context.Background(), &inboxOut, boardInboxOptions{
		Target: scribe,
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardInbox() error: %v", err)
	}
	if err := json.Unmarshal(inboxOut.Bytes(), &inbox); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(inbox.Items) != 0 {
		t.Errorf("inbox after read = %+v", inbox.Items)
	}

	if err := runBoardSubscribe(context.Background(), &bytes.Buffer{}, boardSubscribeOptions{
		Target: scribe,
		Post:   result.Post.ID,
		Silent: true,
	}); err != nil {
		t.Fatalf("runBoardSubscribe(silent) error: %v", err)
	}
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "silent ping",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	inboxOut.Reset()
	if err := runBoardInbox(context.Background(), &inboxOut, boardInboxOptions{
		Target: scribe,
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardInbox() error: %v", err)
	}
	if err := json.Unmarshal(inboxOut.Bytes(), &inbox); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Aware {
		t.Errorf("silent inbox = %+v", inbox.Items)
	}
}

func TestBoardReadAndTag(t *testing.T) {
	setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	scribe := boardTarget{AgentName: "scribe"}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "read me",
		With:   []string{"scribe"},
		Tags:   []string{"one"},
		JSON:   true,
	})

	if err := runBoardTag(context.Background(), &bytes.Buffer{}, boardTagOptions{
		Target: valet,
		Post:   result.Post.ID,
		Add:    []string{"two"},
		Remove: []string{"one"},
	}); err != nil {
		t.Fatalf("runBoardTag() error: %v", err)
	}
	if err := runBoardMessage(context.Background(), &bytes.Buffer{}, boardMessageOptions{
		Target: valet,
		Post:   result.Post.ID,
		Text:   "new",
	}); err != nil {
		t.Fatalf("runBoardMessage() error: %v", err)
	}
	var out bytes.Buffer
	if err := runBoardRead(context.Background(), &out, boardReadOptions{
		Target: scribe,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardRead() error: %v", err)
	}
	if !strings.Contains(out.String(), "Marked read through #1") {
		t.Errorf("read output = %q", out.String())
	}
}

func TestBoardInviteLeaveAndStatus(t *testing.T) {
	valetWS, _ := setupBoardCLI(t)
	valet := boardTarget{AgentName: "valet"}
	auditor := boardTarget{AgentName: "auditor"}
	if _, err := agent.Create(context.Background(), agent.CreateOptions{
		Name:      "auditor",
		Workspace: filepath.Join(t.TempDir(), "auditor"),
		Config:    mustBoardConfig(t),
	}); err != nil {
		// Create may have been done by another test in the same process; the
		// registry is per-test, so this should succeed.
		t.Fatalf("Create(auditor) error: %v", err)
	}
	result, _ := runBoardPostCLI(t, boardPostOptions{
		Target: valet,
		Text:   "invite",
		JSON:   true,
	})
	if err := runBoardInvite(context.Background(), &bytes.Buffer{}, boardInviteOptions{
		Target: valet,
		Post:   result.Post.ID,
		Agents: []string{"auditor"},
	}); err != nil {
		t.Fatalf("runBoardInvite() error: %v", err)
	}
	if err := runBoardShow(context.Background(), &bytes.Buffer{}, boardShowOptions{
		Target: auditor,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardShow(auditor) error: %v", err)
	}
	if err := runBoardLeave(context.Background(), &bytes.Buffer{}, boardLeaveOptions{
		Target: auditor,
		Post:   result.Post.ID,
	}); err != nil {
		t.Fatalf("runBoardLeave() error: %v", err)
	}
	if err := runBoardShow(context.Background(), &bytes.Buffer{}, boardShowOptions{
		Target: auditor,
		Post:   result.Post.ID,
	}); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("runBoardShow(after leave) error = %v, want ErrForbidden", err)
	}

	var statusOut bytes.Buffer
	if err := runBoardStatus(context.Background(), &statusOut, boardStatusOptions{
		Target: boardTarget{AgentName: "valet"},
		JSON:   true,
	}); err != nil {
		t.Fatalf("runBoardStatus() error: %v", err)
	}
	var status boardStatusResult
	if err := json.Unmarshal(statusOut.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v\n%s", err, statusOut.String())
	}
	if status.Agent.ID != "valet" || len(status.Subscriptions.Posts) != 1 {
		t.Errorf("status = %+v", status)
	}

	actor, err := resolveBoardActor("", valetWS, "")
	if err != nil || actor.ID != "valet" {
		t.Errorf("resolveBoardActor() = %+v, %v", actor, err)
	}
}

func mustBoardConfig(t *testing.T) agent.Config {
	t.Helper()
	cfg, err := agent.ParseConfig(`{}`)
	if err != nil {
		t.Fatalf("ParseConfig() error: %v", err)
	}
	return cfg
}
