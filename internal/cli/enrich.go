package cli

import (
	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/enrich"
	"github.com/iml885203/lodog/internal/llm"
)

// enrichExec runs the model; tests replace it.
var enrichExec llm.Exec = llm.Command

func newEnrichCmd() *cobra.Command {
	var caseDir, specPath, out, source, model, claude string
	var batch, concurrency int
	var force bool
	var policyPath string
	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Draft an OpenAPI Overlay that describes every operation (L1)",
		Long: `Asks Claude to describe every operation of the legacy OpenAPI document — what it is
for, when to use it, what each parameter means, a first guess at its risk — and writes an
OpenAPI Overlay. --source lets it read the legacy code to confirm meanings; without it, it
works from the document alone. Operations it was unsure about are marked needsReview: a
person reviews the overlay before the gateway serves it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return enrich.RunToFile(cmd.Context(), enrich.FileOptions{
				CaseDir: caseDir, SpecPath: specPath, Out: out, PolicyPath: policyPath,
				Source: source, Model: model, ClaudeBin: claude,
				Batch: batch, Concurrency: concurrency, Force: force, Exec: enrichExec,
			}, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&caseDir, "case", "", "case directory: reads spec/openapi.json, writes spec/overlay.yaml")
	f.StringVar(&specPath, "spec", "", "OpenAPI document (overrides --case)")
	f.StringVar(&out, "out", "", "overlay to write (overrides --case)")
	f.StringVar(&source, "source", "", "legacy source directory the model may read (optional)")
	f.StringVar(&model, "model", "sonnet", "Claude model")
	f.StringVar(&claude, "claude", "claude", "Claude Code binary")
	f.IntVar(&batch, "batch", 12, "operations per prompt")
	f.IntVar(&concurrency, "concurrency", 4, "prompts in flight")
	f.StringVar(&policyPath, "policy", "", "policy whose risk levels the model may propose (default: the case's spec/policy.yaml, else the overlay-declarable risks of lodog's default risk set, internal/policydraft/risks.yaml)")
	f.BoolVar(&force, "force", false, "overwrite an existing overlay")
	return cmd
}
