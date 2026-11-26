package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardPostTitle   string
	boardPostWith    []string
	boardPostTags    []string
	boardPostFiles   []string
	boardPostNoInbox bool
	boardPostCopy    bool
)

var boardPostCmd = &cobra.Command{
	Use:   `post "<text>"`,
	Short: "Create a board post and its conversation",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := ""
		if len(args) == 1 {
			text = args[0]
		}
		return runBoardPost(cmd.Context(), cmd.OutOrStdout(), boardPostOptions{
			Target:  boardTargetFromFlags(),
			Text:    text,
			Title:   boardPostTitle,
			With:    boardPostWith,
			Tags:    boardPostTags,
			Files:   boardPostFiles,
			NoInbox: boardPostNoInbox,
			Copy:    boardPostCopy,
			JSON:    boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardPostCmd)
	boardPostCmd.Flags().StringVar(&boardPostTitle, "title", "", "optional post title (defaults to the first line)")
	boardPostCmd.Flags().StringArrayVar(&boardPostWith, "with", nil, "agent to invite (repeatable)")
	boardPostCmd.Flags().StringArrayVar(&boardPostTags, "tag", nil, "tag to add (repeatable)")
	boardPostCmd.Flags().StringArrayVar(&boardPostFiles, "file", nil, "file to attach (repeatable)")
	boardPostCmd.Flags().BoolVar(&boardPostCopy, "copy", false, "copy attached files into $ACOLYTE_HOME/board/files")
	boardPostCmd.Flags().BoolVar(&boardPostNoInbox, "no-inbox", false, "do not subscribe the creator to this post")
}

type boardPostOptions struct {
	Target  boardTarget
	Text    string
	Title   string
	With    []string
	Tags    []string
	Files   []string
	NoInbox bool
	Copy    bool
	JSON    bool
}

type boardPostResult struct {
	Post             board.Post       `json:"post"`
	Entry            board.Entry      `json:"entry"`
	Deliveries       []board.Delivery `json:"deliveries,omitempty"`
	InboxError       string           `json:"inbox_error,omitempty"`
	AutomationErrors []string         `json:"automation_errors,omitempty"`
}

func runBoardPost(ctx context.Context, w io.Writer, opts boardPostOptions) error {
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
	post, entry, err := st.CreatePost(ctx, actor, board.CreatePostInput{
		Title:   opts.Title,
		Text:    opts.Text,
		Files:   files,
		With:    opts.With,
		Tags:    opts.Tags,
		NoInbox: opts.NoInbox,
	})
	if err != nil {
		return err
	}

	deliveries, enqueueErr := st.Enqueue(ctx, entry)
	result := boardPostResult{Post: post, Entry: entry, Deliveries: deliveries}
	if enqueueErr != nil {
		result.InboxError = enqueueErr.Error()
	}
	result.AutomationErrors = boardAutomationErrors(processBoardAutomation(ctx, st, entry, deliveries))
	if opts.JSON {
		return printBoardJSON(w, result)
	}

	fmt.Fprintf(w, "Created post %s %q\n", post.ID, post.Title)
	if len(deliveries) > 0 {
		fmt.Fprintf(w, "Board inbox: %d queued.\n", len(deliveries))
	}
	if enqueueErr != nil {
		fmt.Fprintf(w, "Warning: post created but inbox queue failed: %v\n", enqueueErr)
	}
	for _, automationErr := range result.AutomationErrors {
		fmt.Fprintf(w, "Warning: automation: %s\n", automationErr)
	}
	return nil
}
