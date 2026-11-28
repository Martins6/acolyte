package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var boardReadCmd = &cobra.Command{
	Use:   "read <post>",
	Short: "Show a post and mark it read",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardRead(cmd.Context(), cmd.OutOrStdout(), boardReadOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardReadCmd)
}

type boardReadOptions struct {
	Target boardTarget
	Post   string
	JSON   bool
}

type boardReadResult struct {
	Post       board.PostView `json:"post"`
	MarkedRead bool           `json:"marked_read"`
}

func runBoardRead(ctx context.Context, w io.Writer, opts boardReadOptions) error {
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
	lastSeq := 0
	if len(view.Entries) > 0 {
		lastSeq = view.Entries[len(view.Entries)-1].Seq
	}
	if err := st.MarkRead(ctx, actor, view.ID, lastSeq); err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, boardReadResult{Post: view, MarkedRead: true})
	}
	printBoardView(w, view, 0)
	fmt.Fprintf(w, "Marked read through #%d.\n", lastSeq)
	return nil
}
