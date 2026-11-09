package cli

import (
	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/policydraft"
)

func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Produce the L3 policy that gives every operation a risk level",
	}
	cmd.AddCommand(newPolicyDraftCmd())
	return cmd
}

func newPolicyDraftCmd() *cobra.Command {
	var out, model, claudeBin, risksPath, caseDir string
	cmd := &cobra.Command{
		Use:   "draft <openapi.json>",
		Short: "Draft a policy from an OpenAPI document, for an ops or risk owner to review",
		Long: `Gives every operation of the document its own rule and risk level. Clear
signals in the document decide first; a model, asked through headless Claude
Code with no tools, decides the rest and marks what it is unsure of as
"needs review". Reads only the OpenAPI document and the risk set: lodog's
default, or the system's own --risks file. Writes the policy to --out (or
stdout) and a summary to stderr. See docs/policy.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return policydraft.RunToFile(cmd.Context(), policydraft.FileOptions{
				OpenAPI: args[0], CaseDir: caseDir, RisksPath: risksPath, Out: out, Model: model, ClaudeBin: claudeBin,
			}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "write the policy here instead of stdout, e.g. cases/backoffice/spec/policy.auto.yaml")
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory whose case.yaml adds the system's own signal words, e.g. cases/backoffice")
	cmd.Flags().StringVar(&model, "model", "claude-opus-5-5", "model for the operations heuristics cannot settle")
	cmd.Flags().StringVar(&claudeBin, "claude", "claude", "Claude Code binary")
	cmd.Flags().StringVar(&risksPath, "risks", "", "the system's own risk set instead of lodog's default (format: internal/policydraft/risks.yaml)")
	return cmd
}
