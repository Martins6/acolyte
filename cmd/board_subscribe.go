package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardSubscribeAll    bool
	boardSubscribeOff    bool
	boardSubscribeAware  bool
	boardSubscribeSilent bool
)

var boardSubscribeCmd = &cobra.Command{
	Use:   "subscribe [post]",
	Short: "Subscribe to a post's board inbox",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		post := ""
		if len(args) == 1 {
			post = args[0]
		}
		return runBoardSubscribe(cmd.Context(), cmd.OutOrStdout(), boardSubscribeOptions{
			Target: boardTargetFromFlags(),
			Post:   post,
			All:    boardSubscribeAll,
			Off:    boardSubscribeOff,
			Aware:  boardSubscribeAware,
			Silent: boardSubscribeSilent,
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardSubscribeCmd)
	boardSubscribeCmd.Flags().BoolVar(&boardSubscribeAll, "all", false, "apply to every post the agent is invited to")
	boardSubscribeCmd.Flags().BoolVar(&boardSubscribeOff, "off", false, "unsubscribe/mute instead of subscribe")
	boardSubscribeCmd.Flags().BoolVar(&boardSubscribeAware, "aware", false, "include pings in the heartbeat summary")
	boardSubscribeCmd.Flags().BoolVar(&boardSubscribeSilent, "silent", false, "queue pings without the heartbeat summary")
}

type boardSubscribeOptions struct {
	Target boardTarget
	Post   string
	All    bool
	Off    bool
	Aware  bool
	Silent bool
	JSON   bool
}

func runBoardSubscribe(ctx context.Context, w io.Writer, opts boardSubscribeOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	if !actor.IsAgent() {
		return fmt.Errorf("board: only agents subscribe")
	}
	if opts.All && opts.Post != "" {
		return fmt.Errorf("board: use --all or a post, not both")
	}
	if !opts.All && opts.Post == "" {
		return fmt.Errorf("board: a post or --all is required")
	}
	if opts.Aware && opts.Silent {
		return fmt.Errorf("board: use --aware or --silent, not both")
	}
	if opts.Off && (opts.Aware || opts.Silent) {
		return fmt.Errorf("board: --off cannot be combined with --aware/--silent")
	}

	aware := true
	if opts.Silent {
		aware = false
	}

	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	if opts.Off {
		if opts.All {
			err = st.UnsubscribeAll(ctx, actor)
		} else {
			err = st.Unsubscribe(ctx, actor, opts.Post)
		}
	} else {
		if opts.All {
			err = st.SubscribeAll(ctx, actor, aware)
		} else {
			err = st.Subscribe(ctx, actor, opts.Post, aware)
		}
	}
	if err != nil {
		return err
	}

	state, err := st.SubscriptionState(ctx, actor.ID)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, state)
	}
	if opts.Off {
		fmt.Fprintln(w, "Unsubscribed.")
	} else if opts.All {
		fmt.Fprintf(w, "Subscribed to all posts (%s).\n", boardAwareLabel(aware))
	} else {
		fmt.Fprintf(w, "Subscribed to %s (%s).\n", opts.Post, boardAwareLabel(aware))
	}
	return nil
}

func boardAwareLabel(aware bool) string {
	if aware {
		return "aware"
	}
	return "silent"
}

// boardSubscriptionSummary is used by status output.
func boardSubscriptionSummary(state board.SubscriptionState) string {
	var parts []string
	if state.All != nil {
		parts = append(parts, "all:"+boardAwareLabel(state.All.Aware))
	}
	for _, sub := range state.Posts {
		parts = append(parts, sub.PostID+":"+boardAwareLabel(sub.Aware))
	}
	if len(parts) == 0 {
		return "none"
	}
	return joinComma(parts)
}
