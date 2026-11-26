package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/comm"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/storepool"
	"github.com/spf13/cobra"
)

var (
	boardInboxAll            bool
	boardInboxRetry          bool
	boardInboxPromoteSession string
)

var boardInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "View the agent's board inbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBoardInbox(cmd.Context(), cmd.OutOrStdout(), boardInboxOptions{
			Target: boardTargetFromFlags(),
			All:    boardInboxAll,
			Retry:  boardInboxRetry,
			JSON:   boardJSONOut,
		})
	},
}

var boardInboxShowCmd = &cobra.Command{
	Use:   "show <post>",
	Short: "Show the inbox pings for one post",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardInboxShow(cmd.Context(), cmd.OutOrStdout(), boardInboxShowOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			JSON:   boardJSONOut,
		})
	},
}

var boardInboxReadCmd = &cobra.Command{
	Use:   "read <post>",
	Short: "Mark every inbox ping for a post as read",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardInboxRead(cmd.Context(), cmd.OutOrStdout(), boardInboxReadOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			JSON:   boardJSONOut,
		})
	},
}

var boardInboxPromoteCmd = &cobra.Command{
	Use:   "promote <post>",
	Short: "Deliver unread pings to the agent's main session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardInboxPromote(cmd.Context(), cmd.OutOrStdout(), boardInboxPromoteOptions{
			Target:    boardTargetFromFlags(),
			Post:      args[0],
			SessionID: boardInboxPromoteSession,
			JSON:      boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardInboxCmd)
	boardInboxCmd.AddCommand(boardInboxShowCmd)
	boardInboxCmd.AddCommand(boardInboxReadCmd)
	boardInboxCmd.AddCommand(boardInboxPromoteCmd)
	boardInboxCmd.Flags().BoolVar(&boardInboxAll, "all", false, "include read and processed pings")
	boardInboxCmd.Flags().BoolVar(&boardInboxRetry, "retry", false, "retry pending automated admissions")
	boardInboxPromoteCmd.Flags().StringVar(&boardInboxPromoteSession, "session-id", "", "optional main session for the promoted turn")
}

type boardInboxOptions struct {
	Target boardTarget
	All    bool
	Retry  bool
	JSON   bool
}

type boardInboxShowOptions struct {
	Target boardTarget
	Post   string
	JSON   bool
}

type boardInboxReadOptions struct {
	Target boardTarget
	Post   string
	JSON   bool
}

type boardInboxPromoteOptions struct {
	Target    boardTarget
	Post      string
	SessionID string
	JSON      bool
}

type boardInboxResult struct {
	Items []board.InboxView `json:"items"`
}

func runBoardInbox(ctx context.Context, w io.Writer, opts boardInboxOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: the user has no board inbox; use acolyte board list")
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	if opts.Retry {
		errs := processBoardAutomationAgent(ctx, st, actor.ID)
		result := boardAutomationErrors(errs)
		if opts.JSON {
			return printBoardJSON(w, map[string]any{"retried": true, "errors": result})
		}
		if len(result) == 0 {
			fmt.Fprintln(w, "Automation retry complete.")
		} else {
			for _, message := range result {
				fmt.Fprintf(w, "Warning: %s\n", message)
			}
		}
		return nil
	}

	items, err := st.Inbox(ctx, actor.ID, opts.All)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, boardInboxResult{Items: items})
	}
	printBoardInbox(w, items)
	return nil
}

func runBoardInboxShow(ctx context.Context, w io.Writer, opts boardInboxShowOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: the user has no board inbox; use acolyte board list")
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	view, err := st.Show(ctx, actor, opts.Post)
	if err != nil {
		return err
	}
	items, err := st.Inbox(ctx, actor.ID, true)
	if err != nil {
		return err
	}
	filtered := make([]board.InboxView, 0, len(items))
	for _, item := range items {
		if item.PostID == view.ID {
			filtered = append(filtered, item)
		}
	}
	if opts.JSON {
		return printBoardJSON(w, boardInboxResult{Items: filtered})
	}
	printBoardInbox(w, filtered)
	return nil
}

func runBoardInboxRead(ctx context.Context, w io.Writer, opts boardInboxReadOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: the user has no board inbox; use acolyte board list")
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	view, err := st.Show(ctx, actor, opts.Post)
	if err != nil {
		return err
	}
	if err := st.MarkInboxRead(ctx, actor.ID, view.ID); err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, map[string]any{"post": view.ID, "read": true})
	}
	fmt.Fprintf(w, "Marked inbox for %s read.\n", view.ID)
	return nil
}

func printBoardInbox(w io.Writer, items []board.InboxView) {
	if len(items) == 0 {
		fmt.Fprintln(w, "Board inbox is empty.")
		return
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "POST\tENTRY\tAUTHOR\tMODE\tAWARE\tCREATED\tPREVIEW")
	for _, item := range items {
		aware := "silent"
		if item.Aware {
			aware = "aware"
		}
		preview := boardPreview(item.EntryText, 48)
		if preview == "" && item.EntryKind == board.EntryPost {
			preview = boardPreview(item.PostTitle, 48)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			item.PostID, boardEntryLabel(board.Entry{Kind: item.EntryKind, Seq: item.EntrySeq}),
			item.EntryAuthor.ID, item.Mode, aware, boardTime(item.CreatedAt), strings.TrimSpace(preview))
	}
	table.Flush()
}

func runBoardInboxPromote(ctx context.Context, w io.Writer, opts boardInboxPromoteOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: the user has no board inbox; use acolyte board list")
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	view, err := st.Show(ctx, actor, opts.Post)
	if err != nil {
		return err
	}
	items, err := st.Inbox(ctx, actor.ID, false)
	if err != nil {
		return err
	}
	filtered := make([]board.InboxView, 0, len(items))
	for _, item := range items {
		if item.PostID == view.ID {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		if opts.JSON {
			return printBoardJSON(w, map[string]any{"post": view.ID, "promoted": 0})
		}
		fmt.Fprintln(w, "No unread pings to promote.")
		return nil
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		return err
	}
	defer reg.Close()
	agent, err := reg.GetByName(actor.ID)
	if err != nil {
		return fmt.Errorf("board: agent %q is not registered", actor.ID)
	}
	client, err := openCodeEnsure(ctx)
	if err != nil {
		return fmt.Errorf("opencode service unavailable: %w", err)
	}
	pool := storepool.New()
	defer pool.CloseAll()
	sink := comm.OutboxSink{Stores: pool, Turn: client}
	reply, sessionID, err := sink.Inbox(ctx, agent, opts.SessionID, boardPromoteText(view, filtered), map[string]any{
		"channel":  "agent-board",
		"post_id":  view.ID,
		"promoted": len(filtered),
	})
	if err != nil {
		return err
	}
	if err := st.MarkInboxRead(ctx, actor.ID, view.ID); err != nil {
		return err
	}
	if sessionID != "" {
		_ = st.MarkInboxSession(ctx, actor.ID, view.ID, sessionID)
	}
	result := map[string]any{
		"post":       view.ID,
		"session_id": sessionID,
		"promoted":   len(filtered),
		"reply":      reply,
	}
	if opts.JSON {
		return printBoardJSON(w, result)
	}
	fmt.Fprintf(w, "Promoted %d pings to session %s.\n", len(filtered), orDash(sessionID))
	if strings.TrimSpace(reply) != "" {
		fmt.Fprintf(w, "Reply:\n%s\n", reply)
	}
	return nil
}

func boardPromoteText(view board.PostView, items []board.InboxView) string {
	var b strings.Builder
	b.WriteString("ACOLYTE BOARD PROMOTION\n")
	fmt.Fprintf(&b, "Post: %q (%s)\n", view.Title, view.ID)
	fmt.Fprintf(&b, "Unread entries: %d\n", len(items))
	b.WriteString("<untrusted_board_entries>\n")
	for _, item := range items {
		text := boardPreview(item.EntryText, 300)
		if text == "" {
			text = boardPreview(item.PostTitle, 300)
		}
		fmt.Fprintf(&b, "#%d %s: %s\n", item.EntrySeq, item.EntryAuthor.ID, text)
	}
	b.WriteString("</untrusted_board_entries>\n")
	fmt.Fprintf(&b, "Read: acolyte board show %s\n", view.ID)
	fmt.Fprintf(&b, "Reply: acolyte board message %s \"...\"\n", view.ID)
	return b.String()
}
