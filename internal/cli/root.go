// Package cli wires the lodog subcommands. Each subcommand belongs to one
// track (see docs/contracts.md once written); this package only parses flags
// and hands off, so no behaviour lives here.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is stamped at release; "dev" otherwise.
var version = "dev"

// Execute runs the root command and returns the process exit code.
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "lodog:", err)
		return 1
	}
	return 0
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "lodog",
		Short:         "Make a legacy API agent-friendly, and prove it with a score",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newInitCmd(),
		newScoreCmd(),
		newEnrichCmd(),
		newServeCmd(),
		newApprovalsCmd(),
		newPolicyCmd(),
		newReviewCmd(),
		newWorkflowsCmd(),
		newBenchCmd(),
		newReportCmd(),
		newSkillCmd(),
	)
	return root
}
