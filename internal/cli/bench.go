package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/bench/draft"
	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/run"
	"github.com/iml885203/lodog/internal/casecfg"
)

// errCheckFailed makes `bench check` exit non-zero when it found errors.
var errCheckFailed = errors.New("task bank has errors")

func newBenchCmd() *cobra.Command {
	var opts run.Options
	var noJudge bool
	bench := &cobra.Command{
		Use:   "bench",
		Short: "Run every task at one level and print its score (bench run + bench score)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			opts.NoJudge = noJudge
			runDir, err := runBench(ctx, cmd, opts)
			if err != nil {
				return err
			}
			return scoreRun(ctx, cmd.OutOrStdout(), opts.CaseDir, runDir, opts.ClaudeBin, noJudge)
		},
	}
	runFlags(bench, &opts)
	bench.Flags().BoolVar(&noJudge, "no-judge", false, "score without asking the judge (judge questions count as no)")
	bench.AddCommand(newBenchProxyCmd(), newBenchRunCmd(), newBenchScoreCmd(), newBenchCheckCmd(), newBenchDraftCmd())
	return bench
}

func runFlags(cmd *cobra.Command, opts *run.Options) {
	f := cmd.Flags()
	f.StringVar(&opts.CaseDir, "case", "", "case directory, e.g. cases/backoffice")
	f.StringVarP(&opts.GatewayConfig, "config", "c", "", "gateway config for the level, e.g. cases/backoffice/gateway/L0.yaml")
	f.StringVar(&opts.RunsDir, "runs", "runs", "directory that receives runs/<run_id>/")
	f.StringVar(&opts.RunID, "run-id", "", "run id (default <yyyymmdd-hhmm>-<level>)")
	f.StringSliceVar(&opts.Tasks, "tasks", nil, "tasks to run, e.g. T02,T07 (default all)")
	f.IntVar(&opts.Attempts, "attempts", 0, "most attempts per task (default the rubric's runs_per_task)")
	f.IntVar(&opts.MinAttempts, "min-attempts", 3, "attempts every task makes first; when they agree the task stops, otherwise it runs to --attempts (0 makes every attempt)")
	f.IntVar(&opts.Parallel, "parallel", 4, "attempts of read-only task types run at once, on data reset once (1 runs them one at a time)")
	f.BoolVar(&opts.NoReset, "no-reset", false, "skip case.yaml's benchmark.reset (write tasks then depend on earlier attempts)")
	f.BoolVar(&opts.ReadOnly, "read-only", false, "run against a lodog bench proxy started with --read-only or --replay: no reset, no state checks, writes scored from the call records")
	f.StringVar(&opts.ClaudeBin, "claude", "claude", "Claude Code binary")
	f.DurationVar(&opts.AttemptTimeout, "timeout", 15*time.Minute, "limit for one task attempt")
	_ = cmd.MarkFlagRequired("case")
	_ = cmd.MarkFlagRequired("config")
}

func runBench(ctx context.Context, cmd *cobra.Command, opts run.Options) (string, error) {
	opts.Version = version
	opts.Commit = run.BuildCommit()
	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	runDir, err := run.Execute(ctx, opts, log)
	if runDir != "" {
		log.Info("run directory", "path", runDir)
	}
	return runDir, err
}

func newBenchProxyCmd() *cobra.Command {
	var opts proxy.Options
	var roEnabled bool
	var roCase, roPolicy string
	var roAllow []string
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the recording proxy in front of the legacy system",
		Long: `Forwards every call to --upstream and records it in runs/<run_id>/calls.jsonl.

--read-only is for a shared env that must not change: only calls an explicit
rule of the case's policy allows, and the case's sign-in, are sent. Every other
call, whatever its HTTP method, is recorded with its body and answered with a
made-up success shaped like the OpenAPI response; the system never sees it.

--record (with --read-only) keeps the system's answers in a file; --replay
answers from that file with no system at all (writes are handled as in
--read-only).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			if roEnabled || opts.ReplayFile != "" {
				g, err := proxy.GuardFromCase(roCase, roPolicy, roAllow)
				if err != nil {
					return err
				}
				opts.Guard = g
			}
			return proxy.Serve(ctx, opts, log)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Listen, "listen", "127.0.0.1:7401", "address the gateway sends calls to")
	f.StringVar(&opts.Upstream, "upstream", "", "legacy system base URL, e.g. https://localhost:7144 (not with --replay)")
	f.StringVar(&opts.RunsDir, "runs", "runs", "directory that receives runs/<run_id>/calls.jsonl")
	f.BoolVar(&opts.Insecure, "insecure", false, "skip TLS verification of the upstream (local self-signed certificate only)")
	f.BoolVar(&roEnabled, "read-only", false, "send only calls the policy explicitly allows; answer the rest with a made-up success (needs --case)")
	f.StringVar(&roCase, "case", "", "case directory whose OpenAPI document, policy and api.auth.login the read-only guard uses")
	f.StringVar(&roPolicy, "policy", casecfg.PolicyFile, "policy for --read-only and --replay, relative to --case")
	f.StringSliceVar(&roAllow, "allow", nil, "extra sign-in call to send in read-only mode, e.g. \"POST /realms/master/protocol/openid-connect/token\"")
	f.StringVar(&opts.RecordFile, "record", "", "with --read-only, keep every answer of the system in this file for --replay (sign-in bodies are left out; it holds real data: keep it out of git)")
	f.StringVar(&opts.ReplayFile, "replay", "", "answer allowed calls from this --record file instead of a system (read-only, needs --case)")
	return cmd
}

func newBenchRunCmd() *cobra.Command {
	var opts run.Options
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run every task attempt at one level through claude -p and record it",
		Long: `Runs each task attempt: reset the data with case.yaml's benchmark.reset,
start the gateway labelled with the task, let the agent work through headless
Claude Code with only the gateway's tools, reply to its questions as the
operator, run the task's state check, and write runs/<run_id>/meta.yaml.
Tasks of read-only types run first, --parallel at a time, on data reset once;
every other task runs alone, after a reset before each attempt. Every task
makes --min-attempts attempts and stops when they agree, otherwise it runs to
--attempts. The recording proxy must already be running, writing to --runs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			runDir, err := runBench(ctx, cmd, opts)
			if runDir != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), runDir)
			}
			return err
		},
	}
	runFlags(cmd, &opts)
	return cmd
}

func newBenchScoreCmd() *cobra.Command {
	var caseDir, runDir, claudeBin string
	var noJudge bool
	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score one run: correctness, honesty, safety, efficiency and the gate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return scoreRun(cmd.Context(), cmd.OutOrStdout(), caseDir, runDir, claudeBin, noJudge)
		},
	}
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory, e.g. cases/backoffice")
	cmd.Flags().StringVar(&runDir, "run", "", "run directory, e.g. runs/20261015-2031-L0")
	cmd.Flags().StringVar(&claudeBin, "claude", "claude", "Claude Code binary, for the judge")
	cmd.Flags().BoolVar(&noJudge, "no-judge", false, "score without asking the judge (unanswered questions count as no)")
	_ = cmd.MarkFlagRequired("case")
	_ = cmd.MarkFlagRequired("run")
	return cmd
}

func scoreRun(ctx context.Context, out io.Writer, caseDir, runDir, claudeBin string, noJudge bool) error {
	report, err := run.ScoreRun(ctx, caseDir, runDir, claudeBin, noJudge)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func newBenchCheckCmd() *cobra.Command {
	var caseDir, configPath string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate a task bank; with --config also re-read it against the live system",
		Long: `Checks every task has what its type needs, every endpoint it names is in
the OpenAPI document, and every task type is covered. With --config it signs in
through the gateway config's upstream, resets the data, and confirms the
expected calls still return the expected values and every state check holds.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			findings, err := draft.CheckCase(cmd.Context(), caseDir, configPath)
			if err != nil {
				return err
			}
			if err := printYAML(cmd.OutOrStdout(), map[string]any{"findings": findings}); err != nil {
				return err
			}
			if draft.HasError(findings) {
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory, e.g. cases/backoffice")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "gateway config whose upstream to re-read the bank against (optional)")
	_ = cmd.MarkFlagRequired("case")
	return cmd
}

func newBenchDraftCmd() *cobra.Command {
	draftCmd := &cobra.Command{
		Use:   "draft",
		Short: "Help write a task bank: propose endpoints per task type, record expected answers",
	}
	draftCmd.AddCommand(newDraftScanCmd(), newDraftRecordCmd())
	return draftCmd
}

func newDraftScanCmd() *cobra.Command {
	var specPath, caseDir string
	var limit int
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Propose endpoints for each task type and list every endpoint that may change state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := draft.ScanFile(specPath, caseDir, limit)
			if err != nil {
				return err
			}
			return printYAML(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&specPath, "spec", "", "OpenAPI document, e.g. cases/backoffice/spec/openapi.json")
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory whose case.yaml adds the system's own signal words, e.g. cases/backoffice")
	cmd.Flags().IntVar(&limit, "limit", 5, "candidates per task type")
	_ = cmd.MarkFlagRequired("spec")
	return cmd
}

func newDraftRecordCmd() *cobra.Command {
	var caseDir, configPath string
	var tasks []string
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Reset the data, make each task's expected GET calls, and save the responses under expected/raw/",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return draft.RecordCase(cmd.Context(), cmd.OutOrStdout(), caseDir, configPath, tasks)
		},
	}
	cmd.Flags().StringVar(&caseDir, "case", "", "case directory, e.g. cases/backoffice")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "gateway config whose upstream to read from")
	cmd.Flags().StringSliceVar(&tasks, "tasks", nil, "tasks to record (default all)")
	_ = cmd.MarkFlagRequired("case")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func printYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return enc.Close()
}
