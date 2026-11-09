package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/report"
)

func newReportCmd() *cobra.Command {
	var opts report.Options
	var out string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Write one HTML page with every level of a case side by side",
		Long: `Scores every gateway config under <case>/gateway/ (lodog score) and rescores
the latest run of each under --runs (lodog bench score, without asking the
judge), then writes one self-contained HTML page: the levels side by side, the
safety gate, results by task type, the L4 list, and what is not measured yet.
With no runs it shows the static scores alone and says so.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Now = time.Now()
			r, err := report.Build(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if out == "" {
				return report.RenderHTML(cmd.OutOrStdout(), r)
			}
			f, err := os.Create(out) //nolint:gosec // G304: the operator names the output file
			if err != nil {
				return fmt.Errorf("create %s: %w", out, err)
			}
			if err := report.RenderHTML(f, r); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.CaseDir, "case", "", "case directory, e.g. cases/backoffice")
	cmd.Flags().StringVar(&opts.RunsDir, "runs", "runs", "directory holding runs/<run_id>/")
	cmd.Flags().StringVar(&opts.Origin, "origin", "", "probe this system's origin for the public agent standards of the L4 section; the only network access of report")
	cmd.Flags().StringVar(&opts.Mode, "mode", "full", "which runs to score: full, read-only or replay (modes are never mixed on one page)")
	cmd.Flags().StringVarP(&out, "output", "o", "", "write the page to this file (default stdout)")
	_ = cmd.MarkFlagRequired("case")
	return cmd
}
