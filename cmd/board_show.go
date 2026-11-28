package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var boardShowLimit int

var boardShowCmd = &cobra.Command{
	Use:   "show <post>",
	Short: "Show a board post and its messages",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardShow(cmd.Context(), cmd.OutOrStdout(), boardShowOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			Limit:  boardShowLimit,
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardShowCmd)
	boardShowCmd.Flags().IntVar(&boardShowLimit, "limit", 0, "show only the last N messages (0 = all)")
}

type boardShowOptions struct {
	Target boardTarget
	Post   string
	Limit  int
	JSON   bool
}

func runBoardShow(ctx context.Context, w io.Writer, opts boardShowOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
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
	if opts.JSON {
		view.Entries, _ = limitBoardEntries(view.Entries, opts.Limit)
		return printBoardJSON(w, view)
	}
	printBoardView(w, view, opts.Limit)
	return nil
}

// limitBoardEntries keeps the opening post and the last N messages. It returns
// the limited entries and how many earlier messages were omitted.
func limitBoardEntries(entries []board.Entry, limit int) ([]board.Entry, int) {
	if limit <= 0 || len(entries) <= 1 {
		return entries, 0
	}
	keep := limit
	if keep > len(entries)-1 {
		keep = len(entries) - 1
	}
	if keep >= len(entries)-1 {
		return entries, 0
	}
	omitted := len(entries) - 1 - keep
	tail := entries[len(entries)-keep:]
	return append([]board.Entry{entries[0]}, tail...), omitted
}

func printBoardView(w io.Writer, view board.PostView, limit int) {
	fmt.Fprintf(w, "Board post %s %q", view.ID, view.Title)
	if len(view.Tags) > 0 {
		fmt.Fprintf(w, " [%s]", strings.Join(view.Tags, ", "))
	}
	fmt.Fprintf(w, " · %s · updated %s\n\n", boardParticipants(view.Participants), boardTime(view.UpdatedAt))

	entries := view.Entries
	entries, omitted := limitBoardEntries(entries, limit)
	seqByID := make(map[string]int, len(view.Entries))
	for _, entry := range view.Entries {
		seqByID[entry.ID] = entry.Seq
	}
	for _, entry := range entries {
		fmt.Fprintf(w, "%s %s · %s", boardEntryLabel(entry), entry.Author.ID, boardTime(entry.CreatedAt))
		if entry.ReplyToID != "" {
			if seq, ok := seqByID[entry.ReplyToID]; ok {
				if seq == 0 {
					fmt.Fprint(w, " ↳ post")
				} else {
					fmt.Fprintf(w, " ↳ #%d", seq)
				}
			} else {
				fmt.Fprintf(w, " ↳ %s", sanitizeTerminal(entry.ReplyToID))
			}
		}
		fmt.Fprintln(w)
		if entry.Text != "" {
			fmt.Fprintln(w, sanitizeTerminal(entry.Text))
		}
		for _, file := range entry.Files {
			missing := ""
			if _, err := os.Stat(file.Path); err != nil {
				missing = " [missing]"
			}
			fmt.Fprintf(w, "  - %s  %s  %s  (%s)%s\n",
				sanitizeTerminal(file.Name), sanitizeTerminal(file.Path),
				formatBoardSize(file.Size), file.Mode, missing)
		}
		fmt.Fprintln(w)
	}
	if omitted > 0 {
		fmt.Fprintf(w, "… %d earlier messages omitted (use --limit 0 for all)\n\n", omitted)
	}

	if view.Subscribed {
		if view.Aware {
			fmt.Fprint(w, "inbox: subscribed (aware)")
		} else {
			fmt.Fprint(w, "inbox: subscribed (silent)")
		}
	} else if view.Automation != nil {
		fmt.Fprint(w, "inbox: automation only")
	} else {
		fmt.Fprint(w, "inbox: not subscribed")
	}
	if view.Automation != nil {
		if view.Automation.Paused() {
			fmt.Fprintf(w, " · automation: paused (%s)", view.Automation.PauseReason)
		} else {
			fmt.Fprintf(w, " · automation: active (%d turns)", view.Automation.Turns)
		}
	}
	fmt.Fprintf(w, " · unread: %d\n", view.Unread)
}
