package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iml885203/lodog/internal/approval"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/gateway"
)

// buildServeGateway reads the config, applies the command-line overrides and
// builds the gateway from the result: the config the gateway runs with is the
// overridden one.
func buildServeGateway(configPath string, over casecfg.Overrides, getenv func(string) string, labels gateway.Labels, log *slog.Logger) (*casecfg.Config, *gateway.Gateway, error) {
	cfg, err := casecfg.LoadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.Override(over); err != nil {
		return nil, nil, err
	}
	gw, err := gateway.New(cfg, getenv, labels, version, log)
	if err != nil {
		return nil, nil, err
	}
	return cfg, gw, nil
}

// overridesOf reads the override flags back from cmd, so a test can check
// which flag feeds which field.
func overridesOf(cmd *cobra.Command) casecfg.Overrides {
	get := func(name string) string { v, _ := cmd.Flags().GetString(name); return v }
	return casecfg.Overrides{
		Upstream: get("upstream"), Listen: get("listen"), ApprovalsListen: get("approvals-listen"),
		Actor: get("actor"), OnBehalfOf: get("on-behalf-of"),
	}
}

func newServeCmd() *cobra.Command {
	var configPath string
	var labels gateway.Labels
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP gateway at a given level (L0-L3)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			cfg, gw, err := buildServeGateway(configPath, overridesOf(cmd), os.Getenv, labels, log)
			if err != nil {
				return err
			}
			channel, err := approval.ChannelFor(cfg.Approvals, os.Getenv, log)
			if err != nil {
				return err
			}
			if channel != nil {
				gw.SetNotifier(channel)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return gw.Serve(ctx)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "gateway config file (docs/contracts.md §2)")
	cmd.Flags().StringVar(&labels.Run, "run", "", "default run label for clients that cannot set _meta (lodog/run)")
	cmd.Flags().StringVar(&labels.Task, "task", "", "default task label, e.g. T02 (lodog/task)")
	cmd.Flags().StringVar(&labels.Attempt, "attempt", "", "default attempt label (lodog/attempt)")
	cmd.Flags().String("upstream", "", "use this upstream instead of the config's (point the same level at another system)")
	cmd.Flags().String("listen", "", "use this MCP address instead of the config's")
	cmd.Flags().String("approvals-listen", "", "use this approvals address instead of the config's")
	cmd.Flags().String("actor", "", "use this identity.actor (who the approver sees acting) instead of the config's")
	cmd.Flags().String("on-behalf-of", "", "use this identity.on_behalf_of (for whom) instead of the config's")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
