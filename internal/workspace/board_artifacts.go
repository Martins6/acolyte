package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	BoardSkillPath = ".agents/skills/board/SKILL.md"

	BoardReadToolPath  = ".opencode/tools/board_read.ts"
	BoardReplyToolPath = ".opencode/tools/board_reply.ts"

	boardMarkerStart = "<!-- acolyte:board -->"
	boardMarkerEnd   = "<!-- /acolyte:board -->"

	boardSkillMarker = "<!-- acolyte:managed:board-skill -->"
)

var boardAgentsBlock = strings.Join([]string{
	boardMarkerStart,
	"- **Internal board** — a post plus ordered messages. Use `acolyte board` (see `.agents/skills/board/SKILL.md`).",
	boardMarkerEnd,
}, "\n")

var boardConfigBlock = strings.Join([]string{
	boardMarkerStart,
	"- **Internal board** — `acolyte board`: post + ordered messages; pings land in `acolyte board inbox`.",
	boardMarkerEnd,
}, "\n")

var boardHeartbeatBlock = strings.Join([]string{
	boardMarkerStart,
	"## Board inbox",
	"- Check `acolyte board inbox --json` for unread pings.",
	"- Check `acolyte board status --json` for automation alerts.",
	"- If there are unread pings or alerts, tell the user with `acolyte notify-user \"Board: ...\"`.",
	"- Do not reply to board pings from the heartbeat; the board inbox is the place to respond.",
	boardMarkerEnd,
}, "\n")

// BoardSkillContent is the opt-in agent skill copied by
// `communication.board: {"mode":"skill"}`.
var BoardSkillContent = strings.Join([]string{
	"---",
	"name: board",
	"description: Coordinate with other agents and the user through the internal board.",
	"metadata:",
	"  opencode/autoinvoke: true",
	"---",
	"",
	boardSkillMarker,
	"",
	"# Internal board",
	"",
	"A board post is one conversation: an opening post followed by an ordered list of",
	"messages. Posts are participants-only; the user is always implicit.",
	"",
	"## Commands",
	"",
	"```bash",
	"acolyte board list --unread --json",
	"acolyte board show <post> --json",
	"acolyte board post \"topic\" --with <agent> --tag <tag> --json",
	"acolyte board message <post> \"reply\" --json",
	"acolyte board read <post>",
	"acolyte board inbox --json",
	"acolyte board inbox read <post>",
	"acolyte board subscribe <post> --aware   # or --silent",
	"acolyte board subscribe --all --aware",
	"acolyte board automate <post>",
	"acolyte board status --json",
	"```",
	"",
	"## Rules",
	"",
	"- The board inbox is personal. Never assume another agent was pinged.",
	"- `notify-user` is for the human; it does not put anything in a board post.",
	"- Reply in the board with `acolyte board message`; the final text of an automated",
	"  board turn is not delivered to the user.",
	"- `--aware` includes pings in the heartbeat summary; `--silent` keeps them in the",
	"  queue only.",
	"- `--automate` enables a dedicated background session for that agent+post.",
	"  Stay silent when you cannot contribute, and be succinct.",
	"- Attach files with `--file`; add `--copy` when the file must survive its source",
	"  workspace. Regex/photo attachments are read by absolute path.",
	"- Use `--json` for machine-readable output and `--as user` to act as the human.",
	"",
}, "\n")

// ArtifactChange is one planned file write or delete under an agent workspace.
type ArtifactChange struct {
	Path   string `json:"path"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Delete bool   `json:"delete,omitempty"`
	Action string `json:"action"` // create | update | delete
}

// PlanBoardArtifacts plans the opt-in board files. It returns only real
// changes; a missing marker or skill file is skipped.
func PlanBoardArtifacts(workspace string, enabled, heartbeat bool) ([]ArtifactChange, error) {
	var changes []ArtifactChange

	skill, err := planSkill(workspace, enabled)
	if err != nil {
		return nil, err
	}
	if skill.Path != "" {
		changes = append(changes, skill)
	}

	for _, item := range []struct {
		path  string
		block string
		on    bool
	}{
		{"AGENTS.md", boardAgentsBlock, enabled},
		{".acolyte/CONFIG.md", boardConfigBlock, enabled},
		{".acolyte/HEARTBEAT.md", boardHeartbeatBlock, enabled && heartbeat},
	} {
		change, err := planMarker(workspace, item.path, item.block, item.on)
		if err != nil {
			return nil, err
		}
		if change.Path != "" {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func planSkill(workspace string, enabled bool) (ArtifactChange, error) {
	before, exists, err := readArtifact(workspace, BoardSkillPath)
	if err != nil {
		return ArtifactChange{}, err
	}
	if enabled {
		if exists && !strings.Contains(before, boardSkillMarker) {
			return ArtifactChange{}, fmt.Errorf("workspace: %s exists and is not Acolyte-managed; refusing to overwrite", BoardSkillPath)
		}
		if exists && before == BoardSkillContent {
			return ArtifactChange{}, nil
		}
		action := "create"
		if exists {
			action = "update"
		}
		return ArtifactChange{Path: BoardSkillPath, Before: before, After: BoardSkillContent, Action: action}, nil
	}
	if !exists {
		return ArtifactChange{}, nil
	}
	if !strings.Contains(before, boardSkillMarker) {
		return ArtifactChange{}, fmt.Errorf("workspace: %s is not Acolyte-managed; refusing to delete", BoardSkillPath)
	}
	return ArtifactChange{Path: BoardSkillPath, Before: before, Delete: true, Action: "delete"}, nil
}

// EnsureBoardTools creates the board tool files when they are missing. It
// never overwrites an existing file.
func EnsureBoardTools(workspace string) error {
	files := map[string]string{
		BoardReadToolPath:  agentBoardReadTool,
		BoardReplyToolPath: agentBoardReplyTool,
	}
	for path, content := range files {
		before, exists, err := readArtifact(workspace, path)
		if err != nil {
			return err
		}
		if exists {
			if before != content {
				return fmt.Errorf("workspace: %s is not the Acolyte board tool; refusing automation", path)
			}
			continue
		}
		if err := ApplyArtifactChanges(workspace, []ArtifactChange{{
			Path: path, After: content, Action: "create",
		}}); err != nil {
			return err
		}
	}
	return nil
}

func planMarker(workspace, relative, block string, enabled bool) (ArtifactChange, error) {
	before, exists, err := readArtifact(workspace, relative)
	if err != nil {
		return ArtifactChange{}, err
	}
	after := before
	if enabled {
		after, err = insertMarkerBlock(before, block)
	} else if exists {
		after, err = removeMarkerBlock(before)
	}
	if err != nil {
		return ArtifactChange{}, fmt.Errorf("workspace: %s: %w", relative, err)
	}
	if after == before {
		return ArtifactChange{}, nil
	}
	action := "update"
	if !exists {
		action = "create"
	}
	return ArtifactChange{Path: relative, Before: before, After: after, Action: action}, nil
}

func readArtifact(workspace, relative string) (string, bool, error) {
	path, err := artifactPath(workspace, relative)
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("workspace: inspect %s: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("workspace: %s is a symlink", relative)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("workspace: %s is not a regular file", relative)
	}
	if err := checkArtifactParent(workspace, path, relative); err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("workspace: read %s: %w", relative, err)
	}
	return string(raw), true, nil
}

func insertMarkerBlock(content, block string) (string, error) {
	start := strings.Index(content, boardMarkerStart)
	end := strings.Index(content, boardMarkerEnd)
	if start >= 0 || end >= 0 {
		if start < 0 || end < 0 || end < start {
			return "", fmt.Errorf("malformed acolyte board markers")
		}
		after := content[end+len(boardMarkerEnd):]
		return content[:start] + block + after, nil
	}
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return block + "\n", nil
	}
	return content + "\n\n" + block + "\n", nil
}

func removeMarkerBlock(content string) (string, error) {
	start := strings.Index(content, boardMarkerStart)
	end := strings.Index(content, boardMarkerEnd)
	if start < 0 && end < 0 {
		return content, nil
	}
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("malformed acolyte board markers")
	}
	before := strings.TrimRight(content[:start], "\n")
	after := strings.TrimLeft(content[end+len(boardMarkerEnd):], "\n")
	switch {
	case before == "" && after == "":
		return "", nil
	case before == "":
		return after, nil
	case after == "":
		return before + "\n", nil
	default:
		return before + "\n\n" + after, nil
	}
}

func artifactPath(workspace, relative string) (string, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace: invalid artifact path %q", relative)
	}
	return filepath.Join(workspace, clean), nil
}

func checkArtifactParent(workspace, path, relative string) error {
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("workspace: resolve %s: %w", workspace, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("workspace: resolve directory for %s: %w", relative, err)
	}
	if parent != canonicalWorkspace &&
		!strings.HasPrefix(parent, canonicalWorkspace+string(filepath.Separator)) {
		return fmt.Errorf("workspace: %s escapes the workspace", relative)
	}
	return nil
}

// ApplyArtifactChanges writes or deletes the planned files atomically. Parent
// directories are created with openat/mkdirat and O_NOFOLLOW so a symlinked
// artifact directory cannot redirect writes outside the workspace.
func ApplyArtifactChanges(workspacePath string, changes []ArtifactChange) error {
	for _, change := range changes {
		if change.Delete {
			if err := deleteArtifactNoFollow(workspacePath, change.Path); err != nil {
				return err
			}
			continue
		}
		if err := writeArtifactNoFollow(workspacePath, change.Path, change.After); err != nil {
			return err
		}
	}
	return nil
}

func writeArtifactNoFollow(workspace, relative, content string) error {
	rel := filepath.Clean(relative)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace: invalid artifact path %q", relative)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	dirfd, err := unix.Open(workspace, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("workspace: open %s: %w", workspace, err)
	}
	defer unix.Close(dirfd)
	for _, part := range parts[:len(parts)-1] {
		if err := unix.Mkdirat(dirfd, part, 0755); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("workspace: create directory %s: %w", part, err)
		}
		next, err := unix.Openat(dirfd, part, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("workspace: open directory %s: %w", part, err)
		}
		unix.Close(dirfd)
		dirfd = next
	}
	base := parts[len(parts)-1]
	tmp := fmt.Sprintf(".acolyte-tmp-%d-%d", os.Getpid(), time.Now().UnixNano())
	fd, err := unix.Openat(dirfd, tmp,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
	if err != nil {
		return fmt.Errorf("workspace: create %s: %w", relative, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlinkat(dirfd, tmp, 0)
		}
	}()

	data := []byte(content)
	for len(data) > 0 {
		written, err := unix.Write(fd, data)
		if err != nil {
			unix.Close(fd)
			return fmt.Errorf("workspace: write %s: %w", relative, err)
		}
		data = data[written:]
	}
	if err := unix.Fsync(fd); err != nil {
		unix.Close(fd)
		return fmt.Errorf("workspace: sync %s: %w", relative, err)
	}
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("workspace: close %s: %w", relative, err)
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, base); err != nil {
		return fmt.Errorf("workspace: replace %s: %w", relative, err)
	}
	cleanup = false
	return nil
}

func deleteArtifactNoFollow(workspace, relative string) error {
	rel := filepath.Clean(relative)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace: invalid artifact path %q", relative)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	dirfd, err := unix.Open(workspace, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("workspace: open %s: %w", workspace, err)
	}
	defer unix.Close(dirfd)
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(dirfd, part, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: open directory %s: %w", part, err)
		}
		unix.Close(dirfd)
		dirfd = next
	}
	base := parts[len(parts)-1]
	if err := unix.Unlinkat(dirfd, base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("workspace: delete %s: %w", relative, err)
	}
	return nil
}
