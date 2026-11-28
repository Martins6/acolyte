package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardAgentName string
	boardWorkspace string
	boardAs        string
	boardJSONOut   bool
)

var boardCmd = &cobra.Command{
	Use:   "board",
	Short: "Internal board for agent and user conversations",
	Long: `Internal board for agent and user conversations.

A board post is one conversation: an opening post followed by an ordered list
of messages. Posts are participants-only; the user is implicit, agents join
with --with or board invite. The board inbox queues pings for subscribed
agents. Use --json for machine-readable output.`,
}

func init() {
	rootCmd.AddCommand(boardCmd)
	boardCmd.PersistentFlags().StringVar(&boardAgentName, "agent-name", "", "agent acting on the board")
	boardCmd.PersistentFlags().StringVar(&boardWorkspace, "workspace-folder", "", "agent workspace acting on the board")
	boardCmd.PersistentFlags().StringVar(&boardAs, "as", "", "actor override: user or an agent name")
	boardCmd.PersistentFlags().BoolVar(&boardJSONOut, "json", false, "machine-readable JSON output")
}

func openBoardStore() (*board.Store, error) {
	st, err := board.OpenDefault()
	if err != nil {
		return nil, fmt.Errorf("board: %w", err)
	}
	return st, nil
}

// boardTarget carries the actor flags so run functions are testable without
// the cobra globals.
type boardTarget struct {
	AgentName       string
	WorkspaceFolder string
	As              string
}

func (t boardTarget) resolve() (board.Actor, error) {
	return resolveBoardActor(t.AgentName, t.WorkspaceFolder, t.As)
}

func boardTargetFromFlags() boardTarget {
	return boardTarget{
		AgentName:       boardAgentName,
		WorkspaceFolder: boardWorkspace,
		As:              boardAs,
	}
}

func boardActorFromFlags() (board.Actor, error) {
	return boardTargetFromFlags().resolve()
}

func printBoardJSON(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

func boardActorLabel(actor board.Actor) string {
	return actor.ID
}

func boardTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04")
}

func boardTags(tags []string) string {
	if len(tags) == 0 {
		return "—"
	}
	return strings.Join(tags, ",")
}

func boardParticipants(actors []board.Actor) string {
	if len(actors) == 0 {
		return "—"
	}
	names := make([]string, 0, len(actors))
	for _, actor := range actors {
		names = append(names, boardActorLabel(actor))
	}
	return strings.Join(names, ", ")
}

func boardPreview(text string, limit int) string {
	text = sanitizeTerminal(text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// sanitizeTerminal strips C0/C1 controls and bidi overrides from human output.
// It preserves newlines and tabs; JSON output is left untouched.
func sanitizeTerminal(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		if (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, value)
}

func boardEntryLabel(entry board.Entry) string {
	if entry.Kind == board.EntryPost {
		return "[post]"
	}
	return fmt.Sprintf("[#%d]", entry.Seq)
}

func formatBoardSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func joinComma(parts []string) string {
	return strings.Join(parts, ", ")
}

// enforceBoardAutomationBinding keeps a dedicated automation session on the
// post that triggered it. Board text is untrusted: without this, an entry could
// direct the agent to read a private post and copy it into an attacker-visible
// one. Only sessions recorded as automation sessions are restricted; a normal
// session (which already has the wider tool surface) is unaffected.
func enforceBoardAutomationBinding(ctx context.Context, st *board.Store, agentID, sessionID, postID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	bound, ok, err := st.AutomationPostBySession(ctx, agentID, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		bound, ok, err = st.SessionBinding(ctx, agentID, sessionID)
		if err != nil {
			return err
		}
	}
	if ok && bound != postID {
		return fmt.Errorf("%w: session %s is bound to post %s", board.ErrForbidden, sessionID, bound)
	}
	return nil
}
