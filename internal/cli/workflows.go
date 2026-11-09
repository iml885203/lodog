package cli

import (
	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/workflowdraft"
)

func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Produce the L2 task tools (an Arazzo document of workflows)",
	}
	cmd.AddCommand(newWorkflowsDraftCmd())
	return cmd
}

func newWorkflowsDraftCmd() *cobra.Command {
	var caseDir, out, source, model, claudeBin string
	var force bool
	cmd := &cobra.Command{
		Use:   "draft",
		Short: "Draft about twenty task tools from the L1 overlay, for a person to review",
		Long: `Reads the case's OpenAPI document with its L1 overlay (spec/overlay.yaml) and asks a
model, through headless Claude Code with no tools, to group the operations into about twenty
tasks an operator does. lodog builds the Arazzo document itself and checks every workflow
against the document; one that does not load is sent back to the model once, then dropped
and reported. Workflows the model was unsure about are marked x-agent.needsReview. Writes
spec/workflows.arazzo.yaml, which L2 and L3 gateways serve once a person has reviewed it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return workflowdraft.RunToFile(cmd.Context(), workflowdraft.FileOptions{
				CaseDir: caseDir, Out: out, Source: source, Model: model, ClaudeBin: claudeBin, Force: force,
			}, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&caseDir, "case", "", "case directory: reads spec/openapi.json and spec/overlay.yaml")
	f.StringVarP(&out, "out", "o", "", "file to write (default <case>/"+casecfg.DefaultWorkflows+")")
	f.StringVar(&source, "source", "api", "the sourceDescriptions name the workflows call into")
	f.StringVar(&model, "model", "claude-opus-5-5", "Claude model")
	f.StringVar(&claudeBin, "claude", "claude", "Claude Code binary")
	f.BoolVar(&force, "force", false, "overwrite an existing workflows file")
	return cmd
}
