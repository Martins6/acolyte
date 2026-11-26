package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardAutomateOff   bool
	boardAutomateReset bool
)

var boardAutomateCmd = &cobra.Command{
	Use:   "automate <post>",
	Short: "Enable, disable, or reset background automation for a post",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardAutomate(cmd.Context(), cmd.OutOrStdout(), boardAutomateOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			Off:    boardAutomateOff,
			Reset:  boardAutomateReset,
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardAutomateCmd)
	boardAutomateCmd.Flags().BoolVar(&boardAutomateOff, "off", false, "stop automation for this post")
	boardAutomateCmd.Flags().BoolVar(&boardAutomateReset, "reset", false, "clear the automation session and turn counter")
}

type boardAutomateOptions struct {
	Target boardTarget
	Post   string
	Off    bool
	Reset  bool
	JSON   bool
}

type boardAutomateResult struct {
	Post             string            `json:"post"`
	Action           string            `json:"action"`
	Automation       *board.Automation `json:"automation,omitempty"`
	AutomationErrors []string          `json:"automation_errors,omitempty"`
}

func runBoardAutomate(ctx context.Context, w io.Writer, opts boardAutomateOptions) error {
	if opts.Off && opts.Reset {
		return fmt.Errorf("board: use --off or --reset, not both")
	}
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: only agents automate")
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	result := boardAutomateResult{Post: opts.Post}
	switch {
	case opts.Off:
		result.Action = "off"
		err = st.ClearAutomation(ctx, actor, opts.Post)
	case opts.Reset:
		result.Action = "reset"
		err = st.ResetAutomation(ctx, actor, opts.Post)
	default:
		result.Action = "active"
		err = st.SetAutomation(ctx, actor, opts.Post)
	}
	if err != nil {
		return err
	}
	if result.Action == "active" {
		post, err := st.ResolvePost(ctx, opts.Post)
		if err != nil {
			return err
		}
		pending, err := st.PendingAutomated(ctx, actor.ID, post.ID, 1)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			result.AutomationErrors = boardAutomationErrors(processBoardAutomationPost(ctx, st, actor.ID, post.ID))
		}
	}
	automation, err := st.Automation(ctx, actor.ID, opts.Post)
	if err != nil {
		return err
	}
	result.Automation = automation
	if opts.JSON {
		return printBoardJSON(w, result)
	}
	switch result.Action {
	case "off":
		fmt.Fprintf(w, "Automation off for %s.\n", opts.Post)
	case "reset":
		fmt.Fprintf(w, "Automation reset for %s.\n", opts.Post)
	default:
		fmt.Fprintf(w, "Automation active for %s.\n", opts.Post)
	}
	for _, automationErr := range result.AutomationErrors {
		fmt.Fprintf(w, "Warning: automation: %s\n", automationErr)
	}
	return nil
}
