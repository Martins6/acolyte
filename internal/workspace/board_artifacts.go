package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		_, exists, err := readArtifact(workspace, path)
		if err != nil {
			return err
		}
		if exists {
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

// ApplyArtifactChanges writes or deletes the planned files atomically.
func ApplyArtifactChanges(workspacePath string, changes []ArtifactChange) error {
	for _, change := range changes {
		path, err := artifactPath(workspacePath, change.Path)
		if err != nil {
			return err
		}
		if change.Delete {
			if err := checkArtifactParent(workspacePath, path, change.Path); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("workspace: delete %s: %w", change.Path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("workspace: create directory for %s: %w", change.Path, err)
		}
		if err := checkArtifactParent(workspacePath, path, change.Path); err != nil {
			return err
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace: %s is a symlink", change.Path)
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".acolyte-*")
		if err != nil {
			return fmt.Errorf("workspace: create %s: %w", change.Path, err)
		}
		tmpName := tmp.Name()
		cleanup := func() {
			tmp.Close()
			os.Remove(tmpName)
		}
		if err := tmp.Chmod(0644); err != nil {
			cleanup()
			return fmt.Errorf("workspace: chmod %s: %w", change.Path, err)
		}
		if _, err := tmp.WriteString(change.After); err != nil {
			cleanup()
			return fmt.Errorf("workspace: write %s: %w", change.Path, err)
		}
		if err := tmp.Sync(); err != nil {
			cleanup()
			return fmt.Errorf("workspace: sync %s: %w", change.Path, err)
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("workspace: close %s: %w", change.Path, err)
		}
		if err := os.Rename(tmpName, path); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("workspace: replace %s: %w", change.Path, err)
		}
	}
	return nil
}
