package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/martins6/acolyte/internal/board"
	"github.com/spf13/cobra"
)

var (
	boardListTags   []string
	boardListSearch string
	boardListSince  string
	boardListUntil  string
	boardListUnread bool
	boardListLimit  int
)

var boardListCmd = &cobra.Command{
	Use:   "list",
	Short: "List board posts visible to the actor",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBoardList(cmd.Context(), cmd.OutOrStdout(), boardListOptions{
			Target: boardTargetFromFlags(),
			Tags:   boardListTags,
			Search: boardListSearch,
			Since:  boardListSince,
			Until:  boardListUntil,
			Unread: boardListUnread,
			Limit:  boardListLimit,
			JSON:   boardJSONOut,
		})
	},
}

func init() {
	boardCmd.AddCommand(boardListCmd)
	boardListCmd.Flags().StringArrayVar(&boardListTags, "tag", nil, "filter by tag (repeatable; all must match)")
	boardListCmd.Flags().StringVar(&boardListSearch, "search", "", "search titles and message text")
	boardListCmd.Flags().StringVar(&boardListSince, "since", "", "only posts updated at/after this time (RFC3339 or YYYY-MM-DD)")
	boardListCmd.Flags().StringVar(&boardListUntil, "until", "", "only posts updated before this time (RFC3339 or YYYY-MM-DD)")
	boardListCmd.Flags().BoolVar(&boardListUnread, "unread", false, "only posts with unread entries")
	boardListCmd.Flags().IntVar(&boardListLimit, "limit", 50, "maximum posts to return")
}

type boardListOptions struct {
	Target boardTarget
	Tags   []string
	Search string
	Since  string
	Until  string
	Unread bool
	Limit  int
	JSON   bool
}

type boardListResult struct {
	Posts []board.PostSummary `json:"posts"`
}

func runBoardList(ctx context.Context, w io.Writer, opts boardListOptions) error {
	actor, err := opts.Target.resolve()
	if err != nil {
		return err
	}
	since, err := parseBoardTime(opts.Since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	until, err := parseBoardTime(opts.Until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}

	st, err := openBoardStore()
	if err != nil {
		return err
	}
	defer st.Close()

	posts, err := st.List(ctx, actor, board.ListFilter{
		Tags:   opts.Tags,
		Search: opts.Search,
		Since:  since,
		Until:  until,
		Unread: opts.Unread,
		Limit:  opts.Limit,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printBoardJSON(w, boardListResult{Posts: posts})
	}
	if len(posts) == 0 {
		fmt.Fprintln(w, "No board posts.")
		return nil
	}

	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tTITLE\tTAGS\tPARTICIPANTS\tMSGS\tUNREAD\tUPDATED")
	for _, post := range posts {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			post.ID, boardPreview(post.Title, 40), boardTags(post.Tags),
			boardParticipants(post.Participants), post.MessageCount, post.Unread,
			boardTime(post.UpdatedAt))
	}
	return table.Flush()
}

func parseBoardTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := parsed.UTC()
		return &utc, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("expected RFC3339 or YYYY-MM-DD, got %q", raw)
	}
	utc := parsed.UTC()
	return &utc, nil
}
