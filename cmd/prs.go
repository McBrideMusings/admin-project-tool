package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/gh"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

var prsCmd = &cobra.Command{
	Use:   "prs",
	Short: "List every open PR in this repo with the dashboard's own bucketing",
	Long: `List every open PR in this repo, each carrying the same bucket the PRs tab
puts it in — mine / needs-me / waiting / other — plus its conflict, check and
review state.

The point of the JSON form is that "needs me" is a real classification, not a
filter over GitHub's review-request list: GitHub clears a review request the
moment you review, so anything reading that list alone loses the PRs you have
already spoken on and would have to guess when they come back. Tooling that
wants the same answer the dashboard shows should ask for it here rather than
reimplement it and drift.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")
		bucket, _ := cmd.Flags().GetString("bucket")
		return runPRs(cmd.Context(), asJSON, bucket)
	},
}

func init() {
	prsCmd.Flags().Bool("json", false, "print JSON instead of a text listing")
	prsCmd.Flags().String("bucket", "", "only PRs in this bucket: mine, needs-me, waiting, other")
}

// prJSON is the wire shape, deliberately separate from gh.OpenPR. The internal
// type's enums are ints and it is what the on-disk cache stores, so giving them
// MarshalJSON methods would silently rewrite the cache format. Names live here
// instead, where nothing else reads them.
type prJSON struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Author     string    `json:"author"`
	HeadBranch string    `json:"headBranch"`
	URL        string    `json:"url"`
	IsDraft    bool      `json:"isDraft"`
	Conflicts  bool      `json:"conflicts"`
	Mergeable  string    `json:"mergeable"`
	Checks     string    `json:"checks"`
	Review     string    `json:"review"`
	Bucket     string    `json:"bucket"`
	MyReview   string    `json:"myReview,omitempty"`
	Updated    time.Time `json:"updated"`
}

func bucketName(b gh.PRBucket) string {
	switch b {
	case gh.BucketMine:
		return "mine"
	case gh.BucketAssigned:
		return "needs-me"
	case gh.BucketReviewed:
		return "waiting"
	default:
		return "other"
	}
}

func checksName(c gh.CheckState) string {
	switch c {
	case gh.ChecksPending:
		return "pending"
	case gh.ChecksPassing:
		return "passing"
	case gh.ChecksFailing:
		return "failing"
	default:
		return "none"
	}
}

func reviewName(r gh.ReviewClass) string {
	switch r {
	case gh.ReviewNeedsResponse:
		return "needs-response"
	case gh.ReviewChangesRequested:
		return "changes-requested"
	case gh.ReviewApproved:
		return "approved"
	default:
		return "in-review"
	}
}

func myReviewName(s gh.ReviewState) string {
	switch s {
	case gh.UpdatedSinceReview:
		return "updated-since-review"
	case gh.RepliedSinceReview:
		return "replied-since-review"
	case gh.ReviewedApproved:
		return "approved"
	case gh.ReviewedChangesRequested:
		return "changes-requested"
	case gh.ReviewedCommented:
		return "commented"
	default:
		return "not-reviewed"
	}
}

func toJSON(nwo string, pr gh.OpenPR) prJSON {
	out := prJSON{
		Number:     pr.Number,
		Title:      pr.Title,
		Author:     pr.Author,
		HeadBranch: pr.HeadBranch,
		URL:        gh.PRURL(nwo, pr.Number),
		IsDraft:    pr.IsDraft,
		// Surfaced as its own boolean because it is the field callers act on, and
		// "CONFLICTING" vs "UNKNOWN" vs "MERGEABLE" is three-valued: UNKNOWN means
		// GitHub has not finished computing the merge, not that it is clean.
		Conflicts: pr.Mergeable == "CONFLICTING",
		Mergeable: pr.Mergeable,
		Checks:    checksName(pr.Checks),
		Review:    reviewName(pr.Review),
		Bucket:    bucketName(pr.Bucket),
		Updated:   pr.Updated,
	}
	if pr.Bucket != gh.BucketMine {
		out.MyReview = myReviewName(pr.MyReview)
	}
	return out
}

func runPRs(ctx context.Context, asJSON bool, bucket string) error {
	nwo, err := git.OriginNWO(ctx)
	if err != nil {
		return err
	}

	prs, err := gh.OpenPRs(ctx, nwo)
	if err != nil {
		return err
	}

	rows := make([]prJSON, 0, len(prs))
	for _, pr := range prs {
		row := toJSON(nwo, pr)
		if bucket != "" && row.Bucket != bucket {
			continue
		}
		rows = append(rows, row)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	for _, r := range rows {
		flags := []string{}
		if r.IsDraft {
			flags = append(flags, "draft")
		}
		if r.Conflicts {
			flags = append(flags, "conflicts")
		}
		if r.Checks == "failing" {
			flags = append(flags, "tests failing")
		}
		suffix := ""
		if len(flags) > 0 {
			suffix = "  [" + strings.Join(flags, ", ") + "]"
		}
		fmt.Printf("#%-5d %-10s %-18s %s%s\n", r.Number, r.Bucket, r.Review, r.Title, suffix)
	}
	return nil
}
