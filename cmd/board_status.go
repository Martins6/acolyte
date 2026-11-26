package cmd

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var boardStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show board subscriptions, automation, and unread pings",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBoardStatus(cmd.Context(), cmd.OutOrStdout(), boardStatusOptions{
			Target: boardTargetFromFlags(),
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardStatusCmd)
}

type boardStatusOptions struct {
	Target boardTarget
	JSON   bool
}

type boardStatusInbox struct {
	Unread       int `json:"unread"`
	UnreadAware  int `json:"unread_aware"`
	UnreadSilent int `json:"unread_silent"`
	Processed    int `json:"processed"`
	Total        int `json:"total"`
}

type boardStatusResult struct {
	Agent         board.Actor             `json:"agent"`
	Subscriptions board.SubscriptionState `json:"subscriptions"`
	Automations   []board.Automation      `json:"automations,omitempty"`
	Inbox         boardStatusInbox        `json:"inbox"`
	Alerts        []board.Alert           `json:"alerts,omitempty"`
}

func runBoardStatus(ctx context.Context, w io.Writer, opts boardStatusOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	result := boardStatusResult{Agent: actor}
	if actor.IsAgent() {
		result.Subscriptions, err = st.SubscriptionState(ctx, actor.ID)
		if err != nil {
			return err
		}
		result.Automations, err = st.Automations(ctx, actor.ID)
		if err != nil {
			return err
		}
		items, err := st.Inbox(ctx, actor.ID, true)
		if err != nil {
			return err
		}
		for _, item := range items {
			result.Inbox.Total++
			if item.ProcessedAt != nil {
				result.Inbox.Processed++
			}
			if item.ReadAt == nil {
				result.Inbox.Unread++
				if item.Aware {
					result.Inbox.UnreadAware++
				} else {
					result.Inbox.UnreadSilent++
				}
			}
		}
		result.Alerts, err = st.Alerts(ctx, actor.ID)
		if err != nil {
			return err
		}
	}

	if opts.JSON {
		return printBoardJSON(w, result)
	}

	fmt.Fprintf(w, "Agent: %s\n", actor.ID)
	fmt.Fprintf(w, "Subscriptions: %s\n", boardSubscriptionSummary(result.Subscriptions))
	if len(result.Subscriptions.Mutes) > 0 {
		fmt.Fprintf(w, "Mutes: %s\n", joinComma(result.Subscriptions.Mutes))
	} else {
		fmt.Fprintln(w, "Mutes: —")
	}
	if len(result.Automations) == 0 {
		fmt.Fprintln(w, "Automation: —")
	} else {
		fmt.Fprintln(w, "Automation:")
		table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "  POST\tSTATE\tTURNS\tSESSION")
		for _, automation := range result.Automations {
			state := "active"
			if automation.Paused() {
				state = "paused"
				if automation.PauseReason != "" {
					state += " (" + automation.PauseReason + ")"
				}
			}
			fmt.Fprintf(table, "  %s\t%s\t%d\t%s\n",
				automation.PostID, state, automation.Turns, orDash(automation.SessionID))
		}
		table.Flush()
	}
	fmt.Fprintf(w, "Inbox: %d unread (%d aware, %d silent), %d processed (%d total)\n",
		result.Inbox.Unread, result.Inbox.UnreadAware, result.Inbox.UnreadSilent,
		result.Inbox.Processed, result.Inbox.Total)
	if len(result.Alerts) == 0 {
		fmt.Fprintln(w, "Alerts: —")
	} else {
		fmt.Fprintf(w, "Alerts: %d\n", len(result.Alerts))
		for _, alert := range result.Alerts {
			fmt.Fprintf(w, "  - %s (%s)\n", alert.Message, alert.PostID)
		}
	}
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
