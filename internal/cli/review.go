package cli

import (
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/review"
)

func newReviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Turn what lodog drafted into a few questions for a person, and write the answers back",
	}
	cmd.AddCommand(newReviewAskCmd(), newReviewApplyCmd())
	return cmd
}

func newReviewAskCmd() *cobra.Command {
	var caseDir, out, html string
	var files review.Files
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "Group the drafts' pending items into yes-or-no questions",
		Long: `Reads a drafted policy, an overlay and a workflows file, and groups every item
nobody has reviewed into one question per group: policy rules by risk (the
model's unsure ones first), overlay operations still marked needsReview by
their risk, and task tools still marked needsReview. Each question has a
one-line reason and lists its items with why each was drafted that way.
Writes the checklist as YAML to --out (or stdout) for an agent to ask question
by question, and with --html a page a person reviews from. See docs/review.md.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return review.AskToFiles(review.AskOptions{CaseDir: caseDir, Files: files, Out: out, HTML: html}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&caseDir, "case", "", "case directory: reads "+review.DraftPolicy+" (or "+casecfg.DefaultPolicy+"), spec/overlay.yaml and "+casecfg.DefaultWorkflows+" where they exist")
	f.StringVar(&files.Policy, "policy", "", "drafted policy to review (default <case>/"+review.DraftPolicy+", else <case>/"+casecfg.DefaultPolicy+")")
	f.StringVar(&files.Overlay, "overlay", "", "overlay to review (default <case>/spec/overlay.yaml)")
	f.StringVar(&files.Workflows, "workflows", "", "workflows file to review (default <case>/"+casecfg.DefaultWorkflows+")")
	f.StringVarP(&out, "out", "o", "", "write the checklist here instead of stdout")
	f.StringVar(&html, "html", "", "also write a one-page HTML summary here")
	return cmd
}

func newReviewApplyCmd() *cobra.Command {
	var by, policyOut, logPath string
	cmd := &cobra.Command{
		Use:   "apply <checklist.yaml>",
		Short: "Write a person's answers back to the drafts and log the review",
		Long: `Reads a checklist from 'lodog review ask' whose questions carry answers:

  answer: {confirmed: true}                                  every item as proposed
  answer: {confirmed: true, change: {"POST /api/x": write}}  yes, except these
  answer: {confirmed: false, change: {...every item...}}     no; each item's right value

Policy rules take the answered risk and a why naming the reviewer and date,
written to --policy-out (default: the drafted policy itself). Overlay
operations take the answered risk and lose needsReview. Task tools answered
keep lose needsReview; those answered drop are removed. Every answered question
is appended to the review log: who, when, what was asked and what changed.
Nothing is written when an answer is invalid; an item that is no longer
pending as asked is refused, so ask again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return review.ApplyFile(args[0], review.ApplyOptions{By: by, PolicyOut: policyOut, LogPath: logPath, Now: time.Now()}, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&by, "by", "", "the person who answered (required)")
	f.StringVar(&policyOut, "policy-out", "", "write the reviewed policy here, e.g. cases/backoffice/spec/policy.reviewed.yaml (default: the drafted policy)")
	f.StringVar(&logPath, "log", "", "review log to append to (default review-log.yaml next to the checklist)")
	_ = cmd.MarkFlagRequired("by")
	return cmd
}
