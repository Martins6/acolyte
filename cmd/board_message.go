package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardMessageReplyTo   string
	boardMessageFiles     []string
	boardMessageCopy      bool
	boardMessageSessionID string
)

var boardMessageCmd = &cobra.Command{
	Use:   `message <post> "<text>"`,
	Short: "Add a message to a board post",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := ""
		if len(args) == 2 {
			text = args[1]
		}
		return runBoardMessage(cmd.Context(), cmd.OutOrStdout(), boardMessageOptions{
			Target:    boardTargetFromFlags(),
			Post:      args[0],
			Text:      text,
			ReplyTo:   boardMessageReplyTo,
			Files:     boardMessageFiles,
			Copy:      boardMessageCopy,
			SessionID: boardMessageSessionID,
			JSON:      boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardMessageCmd)
	boardMessageCmd.Flags().StringVar(&boardMessageReplyTo, "reply-to", "", "entry to quote (id, prefix, or #seq)")
	boardMessageCmd.Flags().StringArrayVar(&boardMessageFiles, "file", nil, "file to attach (repeatable)")
	boardMessageCmd.Flags().BoolVar(&boardMessageCopy, "copy", false, "copy attached files into $ACOLYTE_HOME/board/files")
	boardMessageCmd.Flags().StringVar(&boardMessageSessionID, "session-id", "", "automation session binding (rejects other posts)")
}

type boardMessageOptions struct {
	Target    boardTarget
	Post      string
	Text      string
	ReplyTo   string
	Files     []string
	Copy      bool
	SessionID string
	JSON      bool
}

type boardMessageResult struct {
	Message          board.Entry      `json:"message"`
	Deliveries       []board.Delivery `json:"deliveries,omitempty"`
	InboxError       string           `json:"inbox_error,omitempty"`
	AutomationErrors []string         `json:"automation_errors,omitempty"`
}

func runBoardMessage(ctx context.Context, w io.Writer, opts boardMessageOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	files, err := boardFiles(opts.Files, opts.Copy)
	if err != nil {
		return err
	}
	post, err := st.ResolvePost(ctx, opts.Post)
	if err != nil {
		return err
	}
	if err := enforceBoardAutomationBinding(ctx, st, actor.ID, opts.SessionID, post.ID); err != nil {
		return err
	}
	entry, err := st.AppendMessage(ctx, actor, post.ID, board.AppendMessageInput{
		Text:      opts.Text,
		Files:     files,
		ReplyToID: opts.ReplyTo,
	})
	if err != nil {
		return err
	}

	deliveries, enqueueErr := st.Enqueue(ctx, entry)
	result := boardMessageResult{Message: entry, Deliveries: deliveries}
	if enqueueErr != nil {
		result.InboxError = enqueueErr.Error()
	}
	result.AutomationErrors = boardAutomationErrors(processBoardAutomation(ctx, st, entry, deliveries))
	if opts.JSON {
		return printBoardJSON(w, result)
	}

	fmt.Fprintf(w, "Posted message #%d %s to %s\n", entry.Seq, entry.ID, entry.PostID)
	if len(deliveries) > 0 {
		fmt.Fprintf(w, "Board inbox: %d queued.\n", len(deliveries))
	}
	if enqueueErr != nil {
		fmt.Fprintf(w, "Warning: message posted but inbox queue failed: %v\n", enqueueErr)
	}
	for _, automationErr := range result.AutomationErrors {
		fmt.Fprintf(w, "Warning: automation: %s\n", automationErr)
	}
	return nil
}
