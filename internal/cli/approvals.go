package cli

import (
	"github.com/iml885203/lodog/internal/approval"

	"github.com/spf13/cobra"
)

// Lists the calls an L3 gateway is holding, through its approval endpoint
// (approvals.listen in the gateway config). The endpoint cannot approve or
// deny: verdicts come from the approval mode's own channel (docs/approvals.md).
func newApprovalsCmd() *cobra.Command {
	var admin string
	root := &cobra.Command{
		Use:   "approvals",
		Short: "List the calls the L3 gateway is holding for approval",
	}
	root.PersistentFlags().StringVar(&admin, "admin", "http://127.0.0.1:7402", "the gateway's approvals endpoint")
	list := &cobra.Command{
		Use:   "list",
		Short: "Show calls waiting for a verdict",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return approval.PrintPending(cmd.Context(), cmd.OutOrStdout(), admin+"/approvals")
		},
	}
	root.AddCommand(list)
	return root
}
