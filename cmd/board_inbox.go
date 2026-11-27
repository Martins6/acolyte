package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

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
		reconciled, reconcileErr := st.ReconcileInbox(ctx, actor.ID)
		errs := processBoardAutomationAgent(ctx, st, actor.ID)
		result := boardAutomationErrors(errs)
		if reconcileErr != nil {
			result = append([]string{reconcileErr.Error()}, result...)
		}
		if opts.JSON {
			return printBoardJSON(w, map[string]any{
				"retried":    true,
				"reconciled": reconciled,
				"errors":     result,
			})
		}
		if reconciled > 0 {
			fmt.Fprintf(w, "Recovered %d missing pings.\n", reconciled)
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
	hasPing := false
	for _, item := range items {
		if item.PostID == view.ID {
			hasPing = true
			break
		}
	}
	if !hasPing {
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
	pool := storepool.New()
	defer pool.CloseAll()
	if opts.SessionID != "" {
		perAgent, release, err := pool.Acquire(agent.Workspace)
		if err != nil {
			return fmt.Errorf("board: open agent store: %w", err)
		}
		verifyErr := verifyPromotionSession(perAgent, opts.SessionID)
		release()
		if verifyErr != nil {
			return verifyErr
		}
	}
	client, err := openCodeEnsure(ctx)
	if err != nil {
		return fmt.Errorf("opencode service unavailable: %w", err)
	}

	// Reconcile a stale promotion before claiming it again.
	stale, staleSession, staleMarker, err := st.StalePromotion(ctx, actor.ID, view.ID, 10*time.Minute)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		if staleSession != "" && staleMarker != "" {
			promoted, truncated, scanErr := boardPromotionAdmitted(ctx, client, staleSession, staleMarker)
			if scanErr != nil {
				return scanErr
			}
			if promoted {
				if err := st.MarkInboxPromoted(ctx, actor.ID, stale, staleSession); err != nil {
					return err
				}
			} else if truncated {
				return fmt.Errorf("board: promotion session %s history is truncated; cannot reconcile", staleSession)
			} else if err := st.ReclaimPromotion(ctx, actor.ID, stale); err != nil {
				return err
			}
		} else if err := st.ReclaimPromotion(ctx, actor.ID, stale); err != nil {
			return err
		}
	}

	token := board.NewClaimToken()
	marker := boardPromotionMarker(token)
	entryIDs, err := st.ClaimPromotion(ctx, actor.ID, view.ID, token, marker)
	if err != nil {
		return err
	}
	if len(entryIDs) == 0 {
		if opts.JSON {
			return printBoardJSON(w, map[string]any{"post": view.ID, "promoted": 0})
		}
		fmt.Fprintln(w, "No unread pings to promote.")
		return nil
	}
	claimed := make(map[string]struct{}, len(entryIDs))
	for _, id := range entryIDs {
		claimed[id] = struct{}{}
	}
	items, err = st.Inbox(ctx, actor.ID, false)
	if err != nil {
		return err
	}
	filtered := make([]board.InboxView, 0, len(entryIDs))
	for _, item := range items {
		if item.PostID != view.ID {
			continue
		}
		if _, ok := claimed[item.EntryID]; ok {
			filtered = append(filtered, item)
		}
	}

	sink := comm.OutboxSink{
		Stores: pool,
		Turn:   client,
		OnSession: func(sessionID string) error {
			if err := st.MarkPromotionSession(ctx, actor.ID, token, sessionID); err != nil {
				return err
			}
			perAgent, release, err := pool.Acquire(agent.Workspace)
			if err != nil {
				return err
			}
			defer release()
			_, err = perAgent.Orch().UpsertSession(
				"promote:"+agent.Name, sessionID, time.Now().UTC(),
			)
			return err
		},
	}
	reply, sessionID, err := sink.Inbox(ctx, agent, opts.SessionID, boardPromoteText(view, filtered, marker), map[string]any{
		"channel":  "agent-board",
		"post_id":  view.ID,
		"promoted": len(entryIDs),
	})
	if err != nil {
		return err
	}
	if err := st.MarkInboxPromoted(ctx, actor.ID, entryIDs, sessionID); err != nil {
		return err
	}
	result := map[string]any{
		"post":       view.ID,
		"session_id": sessionID,
		"promoted":   len(entryIDs),
		"reply":      reply,
	}
	if opts.JSON {
		return printBoardJSON(w, result)
	}
	fmt.Fprintf(w, "Promoted %d pings to session %s.\n", len(entryIDs), orDash(sessionID))
	if strings.TrimSpace(reply) != "" {
		fmt.Fprintf(w, "Reply:\n%s\n", reply)
	}
	return nil
}

func boardPromotionAdmitted(ctx context.Context, client turnClient, sessionID, marker string) (bool, bool, error) {
	messages, truncated, err := client.SessionMessages(ctx, sessionID, 100)
	if err != nil {
		return false, false, err
	}
	for _, message := range messages {
		if strings.Contains(message.Text, marker) {
			return true, truncated, nil
		}
	}
	return false, truncated, nil
}

func boardPromotionMarker(token string) string {
	return "[[acolyte-board-promote:" + token + "]]"
}

func boardPromoteText(view board.PostView, items []board.InboxView, marker string) string {
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
	fmt.Fprintf(&b, "Promotion marker: %s\n", marker)
	return b.String()
}
