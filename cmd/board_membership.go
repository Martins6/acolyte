package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/spf13/cobra"
)

var (
	boardInviteAgents []string
	boardTagAdd       []string
	boardTagRemove    []string
)

var boardInviteCmd = &cobra.Command{
	Use:   "invite <post> --agent <name>",
	Short: "Invite registered agents to a board post",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardInvite(cmd.Context(), cmd.OutOrStdout(), boardInviteOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			Agents: boardInviteAgents,
			JSON:   boardJSONOut,
		})
	},
}

var boardLeaveCmd = &cobra.Command{
	Use:   "leave <post>",
	Short: "Leave a board post",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardLeave(cmd.Context(), cmd.OutOrStdout(), boardLeaveOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			JSON:   boardJSONOut,
		})
	},
}

var boardTagCmd = &cobra.Command{
	Use:   "tag <post>",
	Short: "Add or remove tags on a board post",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardTag(cmd.Context(), cmd.OutOrStdout(), boardTagOptions{
			Target: boardTargetFromFlags(),
			Post:   args[0],
			Add:    boardTagAdd,
			Remove: boardTagRemove,
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardInviteCmd)
	boardCmd.AddCommand(boardLeaveCmd)
	boardCmd.AddCommand(boardTagCmd)
	boardInviteCmd.Flags().StringArrayVar(&boardInviteAgents, "agent", nil, "agent name to invite (repeatable)")
	boardTagCmd.Flags().StringArrayVar(&boardTagAdd, "add", nil, "tag to add (repeatable)")
	boardTagCmd.Flags().StringArrayVar(&boardTagRemove, "rm", nil, "tag to remove (repeatable)")
}

type boardInviteOptions struct {
	Target boardTarget
	Post   string
	Agents []string
	JSON   bool
}

type boardLeaveOptions struct {
	Target boardTarget
	Post   string
	JSON   bool
}

type boardTagOptions struct {
	Target boardTarget
	Post   string
	Add    []string
	Remove []string
	JSON   bool
}

type boardInviteResult struct {
	Added            []string         `json:"added"`
	Deliveries       []board.Delivery `json:"deliveries,omitempty"`
	InboxError       string           `json:"inbox_error,omitempty"`
	AutomationErrors []string         `json:"automation_errors,omitempty"`
}

func runBoardInvite(ctx context.Context, w io.Writer, opts boardInviteOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	reg, err := registry.OpenDefault()
	if err != nil {
		return err
	}
	defer reg.Close()
	for _, raw := range opts.Agents {
		name := strings.TrimSpace(raw)
		if name == "" || name == board.UserActorID {
			continue
		}
		if _, err := reg.GetByName(name); err != nil {
			return fmt.Errorf("agent %q is not registered", name)
		}
	}

	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	added, err := st.Invite(ctx, actor, opts.Post, opts.Agents)
	if err != nil {
		return err
	}
	entry, deliveries, enqueueErr := enqueueBoardPost(ctx, st, actor, opts.Post)
	result := boardInviteResult{Added: added, Deliveries: deliveries}
	if enqueueErr != nil {
		result.InboxError = enqueueErr.Error()
	}
	if enqueueErr == nil {
		result.AutomationErrors = boardAutomationErrors(processBoardAutomation(ctx, st, entry, deliveries))
	}
	if opts.JSON {
		return printBoardJSON(w, result)
	}
	if len(added) == 0 {
		fmt.Fprintln(w, "No new participants.")
	} else {
		fmt.Fprintf(w, "Invited: %s.\n", strings.Join(added, ", "))
	}
	if len(deliveries) > 0 {
		fmt.Fprintf(w, "Board inbox: %d queued.\n", len(deliveries))
	}
	if enqueueErr != nil {
		fmt.Fprintf(w, "Warning: participants added but inbox queue failed: %v\n", enqueueErr)
	}
	for _, automationErr := range result.AutomationErrors {
		fmt.Fprintf(w, "Warning: automation: %s\n", automationErr)
	}
	return nil
}

func runBoardLeave(ctx context.Context, w io.Writer, opts boardLeaveOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Leave(ctx, actor, opts.Post); err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, map[string]any{"left": opts.Post})
	}
	fmt.Fprintf(w, "Left post %s.\n", opts.Post)
	return nil
}

func runBoardTag(ctx context.Context, w io.Writer, opts boardTagOptions) error {
	if len(opts.Add) == 0 && len(opts.Remove) == 0 {
		return fmt.Errorf("board: use --add and/or --rm")
	}
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	var tags []string
	if len(opts.Add) > 0 {
		tags, err = st.AddTags(ctx, actor, opts.Post, opts.Add)
		if err != nil {
			return err
		}
	}
	if len(opts.Remove) > 0 {
		tags, err = st.RemoveTags(ctx, actor, opts.Post, opts.Remove)
		if err != nil {
			return err
		}
	}
	if opts.JSON {
		return printBoardJSON(w, map[string]any{"tags": tags})
	}
	fmt.Fprintf(w, "Tags: %s\n", boardTags(tags))
	return nil
}

func enqueueBoardPost(ctx context.Context, st *board.Store, actor board.Actor, postRef string) (board.Entry, []board.Delivery, error) {
	view, err := st.Show(ctx, actor, postRef)
	if err != nil {
		return board.Entry{}, nil, err
	}
	for _, entry := range view.Entries {
		if entry.Kind == board.EntryPost {
			deliveries, err := st.Enqueue(ctx, entry)
			return entry, deliveries, err
		}
	}
	return board.Entry{}, nil, fmt.Errorf("board: post %q has no opening entry", postRef)
}
