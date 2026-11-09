package cli

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/docfetch"
	"github.com/iml885203/lodog/internal/readiness"
)

var errScoreCase = errors.New("--case applies to an OpenAPI document; --config already names its case")

var errScoreInput = errors.New("give exactly one of: an OpenAPI document, or --config")

func newScoreCmd() *cobra.Command {
	var configPath string
	var asJSON, insecure bool
	var origin, caseDir string
	cmd := &cobra.Command{
		Use:   "score [openapi.json | URL]",
		Short: "Score how agent-ready an API is from its documents alone (static 30 points)",
		Long: `Scores the raw OpenAPI document (a file or an http(s) URL), as an agent sees it served one to one,
or with --config what one gateway level gives the agent (policy and identity
count at L3). Prints a summary; --json prints the report as JSON.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (configPath != "") {
				return errScoreInput
			}
			if configPath != "" && caseDir != "" {
				return errScoreCase
			}
			var client *http.Client // nil: the default client
			if insecure {
				client = docfetch.InsecureClient()
			}
			var v readiness.View
			var notes []string
			var err error
			if configPath != "" {
				v, notes, err = readiness.FromGateway(configPath)
			} else {
				var sig readiness.Signals
				if sig, err = readiness.SignalsOf(caseDir); err == nil {
					v, err = readiness.FromSource(cmd.Context(), client, args[0], sig)
				}
			}
			if err != nil {
				return err
			}
			report := readiness.Score(v)
			report.Notes = notes
			if origin != "" {
				if client == nil {
					client = http.DefaultClient
				}
				if err := readiness.CheckStandards(cmd.Context(), client, origin, &report); err != nil {
					return err
				}
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return readiness.Render(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "gateway config of a level, e.g. cases/backoffice/gateway/L3.yaml")
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory whose case.yaml adds the system's own signal words, e.g. cases/backoffice")
	cmd.Flags().StringVar(&origin, "origin", "", "probe this system's origin (e.g. https://api.example.com) for the public agent standards of the L4 section; with a URL as the document this and the download are the only network access of score")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "skip TLS verification when downloading the document or probing --origin (local self-signed certificate only)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}
