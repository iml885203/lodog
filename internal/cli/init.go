package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/initcase"
)

func newInitCmd() *cobra.Command {
	opts := initcase.Options{}
	cmd := &cobra.Command{
		Use:   "init <folder>",
		Short: "Create the files to put your own system behind lodog",
		Long: `Create <folder> with case.yaml, spec/openapi.json, the gateway configs
gateway/L0.yaml, L1.yaml, L2.yaml and L3.yaml, and a README.md for your system. The
auth type and fields are read from the document's securitySchemes (--auth
overrides the type), and the upstream from its servers entry (--upstream
overrides it; without either init fails and writes nothing). L1 needs
spec/overlay.yaml (lodog enrich) and L3 needs spec/policy.yaml (lodog policy
draft) before they start. The folder must not exist yet; init never overwrites
your files. Run from the folder that will contain it.`,
		Example: `  lodog init my-system --swagger https://my-system/swagger/v1/swagger.json
  lodog init my-system --swagger ./swagger.json --with-bench
  lodog init my-system --swagger https://localhost:7144/swagger/v1/swagger.json --insecure`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dir = args[0]
			opts.Out = cmd.OutOrStdout()
			if err := initcase.Generate(cmd.Context(), opts); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "created %s\nnext: read %s/README.md, fill in the TODOs in %s/gateway/L0.yaml, then\n  lodog serve -c %s/gateway/L0.yaml\n",
				opts.Dir, opts.Dir, opts.Dir, opts.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Swagger, "swagger", "", "the system's OpenAPI document: a URL or a file path (required)")
	cmd.Flags().StringVar(&opts.Upstream, "upstream", "", "the system's base URL with its base path; default: the servers entry of the document, else the --swagger host; required when neither gives one")
	cmd.Flags().StringVar(&opts.Auth, "auth", "", "how the system signs in: cookie-session, bearer, api-key or oauth2-client-credentials; default: read from the securitySchemes of the document")
	cmd.Flags().BoolVar(&opts.WithBench, "with-bench", false, "also create a starter task bank (tasks.md, expected/answers.yaml, rubric.yaml)")
	cmd.Flags().BoolVar(&opts.Insecure, "insecure", false, "accept a self-signed certificate: skip TLS verification when fetching --swagger and write upstream_insecure_tls: true into the gateway configs (local system only)")
	_ = cmd.MarkFlagRequired("swagger")
	return cmd
}
