package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
	"github.com/iml885203/lodog/internal/llm"
)

// Options are the `lodog bench run` flags.
type Options struct {
	CaseDir       string
	GatewayConfig string
	RunsDir       string
	RunID         string
	Tasks         []string
	Attempts      int
	// MinAttempts is how many attempts every task makes before they are
	// compared; a task whose finished attempts agree stops there.
	MinAttempts int
	// Parallel is how many shareable attempts run at once.
	Parallel int
	// NoJudge measures attempts without asking the judge, as `lodog bench
	// --no-judge` scores them.
	NoJudge bool
	NoReset bool
	// ReadOnly runs against a proxy that sends no writes: no reset, no state
	// checks, and the run records the proxy's mode.
	ReadOnly       bool
	ClaudeBin      string
	AttemptTimeout time.Duration
	Version        string
	// Commit is the lodog commit the run was made with (BuildCommit); it is
	// written to meta.yaml.
	Commit string
}

const gatewayReadyTimeout = 15 * time.Second

// Execute loads the case and gateway config, checks the running system
// against the case's pinned versions, signs in once so a wrong password cannot
// lock the account, and runs every attempt. It returns the run directory.
func Execute(ctx context.Context, o Options, log *slog.Logger) (string, error) {
	c, err := score.LoadCase(o.CaseDir)
	if err != nil {
		return "", err
	}
	cfg, err := casecfg.LoadConfig(o.GatewayConfig)
	if err != nil {
		return "", err
	}
	plan, err := newPlan(o, c, cfg)
	if err != nil {
		return "", err
	}
	plan.Header.Mode, err = runMode(ctx, o.ReadOnly, cfg.Upstream, cfg.UpstreamInsecureTLS, plan.RunID)
	if err != nil {
		return "", err
	}
	plan.Header.Versions, err = CheckVersions(ctx, c.Versions, ObserveCommand)
	if err != nil {
		return "", err
	}
	session, err := NewSession(ctx, cfg, os.Getenv, plan.RunID)
	if err != nil {
		return "", err
	}
	deps, err := newDeps(ctx, o, &plan, cfg, session, log)
	if err != nil {
		return "", err
	}
	return plan.RunDir, Run(ctx, plan, deps)
}

func newPlan(o Options, c score.Case, cfg *casecfg.Config) (Plan, error) {
	if len(c.Reset) == 0 && !o.NoReset && !o.ReadOnly {
		return Plan{}, fmt.Errorf("%w: case.yaml has no benchmark.reset; add one, or pass --no-reset", ErrPreflight)
	}
	runID := o.RunID
	if runID == "" {
		runID = time.Now().Format("20060102-1504") + "-" + string(cfg.Level)
	}
	if !record.ValidRunID(runID) {
		return Plan{}, fmt.Errorf("%w: %q", record.ErrInvalidRunID, runID)
	}
	ids, err := taskIDs(c, o.Tasks)
	if err != nil {
		return Plan{}, err
	}
	attempts := o.Attempts
	if attempts == 0 {
		attempts = c.Rubric.RunsPerTask
	}
	if attempts < 1 || o.MinAttempts < 0 || o.Parallel < 0 {
		return Plan{}, fmt.Errorf("%w: --attempts must be at least 1, --min-attempts and --parallel not negative", ErrPreflight)
	}
	if c.Rubric.AgentModel == "" || c.Rubric.QuestionJudgeModel == "" || c.Rubric.Reply == "" {
		return Plan{}, fmt.Errorf("%w: rubric needs agent.model and simulated_user", score.ErrIncompleteCase)
	}
	return Plan{
		RunID: runID, Level: string(cfg.Level), RunDir: filepath.Join(o.RunsDir, runID),
		Case: c, TaskIDs: ids, Attempts: attempts, MinAttempts: o.MinAttempts, Parallel: o.Parallel,
		AttemptTimeout: o.AttemptTimeout,
		Header: Meta{
			RunID: runID, Level: string(cfg.Level), Case: o.CaseDir, GatewayConfig: o.GatewayConfig,
			RubricVersion: c.Rubric.Version, AgentModel: c.Rubric.AgentModel, QuestionJudgeModel: c.Rubric.QuestionJudgeModel,
			AttemptsPerTask: attempts, LodogCommit: o.Commit,
		},
	}, nil
}

func taskIDs(c score.Case, want []string) ([]string, error) {
	if len(want) == 0 {
		for id := range c.Tasks {
			want = append(want, id)
		}
		sort.Strings(want)
		return want, nil
	}
	for _, id := range want {
		if _, ok := c.Tasks[id]; !ok {
			return nil, fmt.Errorf("%w: unknown task %s", ErrPreflight, id)
		}
	}
	return want, nil
}

func newDeps(ctx context.Context, o Options, p *Plan, cfg *casecfg.Config, s *Session, log *slog.Logger) (Deps, error) {
	if err := os.MkdirAll(p.RunDir, 0o750); err != nil {
		return Deps{}, fmt.Errorf("create run dir: %w", err)
	}
	configs, err := slotConfigs(ctx, cfg, p.slotsNeeded())
	if err != nil {
		return Deps{}, err
	}
	agents := make([]Agent, len(configs))
	for slot, c := range configs {
		mcpConfig, err := writeMCPConfig(p.RunDir, mcpConfigName(slot), c.Listen)
		if err != nil {
			return Deps{}, err
		}
		agents[slot] = ClaudeCLI{Bin: o.ClaudeBin, Model: p.Case.Rubric.AgentModel, MCPConfig: mcpConfig, WorkDir: p.RunDir, Exec: llm.Command}
	}
	version, err := llm.Command(ctx, p.RunDir, llm.Env(), o.ClaudeBin, "--version")
	if err != nil {
		return Deps{}, fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	p.Header.ClaudeVersion = strings.TrimSpace(string(version))
	return Deps{
		Agent: func(slot int) Agent { return agents[slot] },
		Judge: ClaudeJudge{llm.Claude{Bin: o.ClaudeBin, Model: p.Case.Rubric.QuestionJudgeModel, WorkDir: p.RunDir, Exec: llm.Command}},
		Reset: resetter(o, p.Case.Reset),
		StartGateway: func(ctx context.Context, labels gateway.Labels, slot int) (RunningGateway, error) {
			return StartGateway(ctx, configs[slot], labels, o.Version, log)
		},
		Outcome: newMeasurer(o, *p).outcome,
		CheckState: func(ctx context.Context, sc score.StateCheck, attempt int) (bool, error) {
			return s.CheckState(ctx, sc, attempt, time.Now())
		},
		Now: time.Now,
		Log: log,
	}, nil
}

func resetter(o Options, reset []string) func(context.Context) error {
	if o.NoReset || o.ReadOnly {
		return func(context.Context) error { return nil }
	}
	return func(ctx context.Context) error {
		_, err := llm.Command(ctx, "", os.Environ(), reset[0], reset[1:]...)
		return err
	}
}

func writeMCPConfig(dir, name, listen string) (string, error) {
	cfg := map[string]any{"mcpServers": map[string]any{
		ServerName: map[string]string{"type": "http", "url": "http://" + listen + gateway.MCPPath},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode mcp config: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("resolve mcp config path: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	return path, nil
}

// inProcess is a gateway served in this process for one attempt.
type inProcess struct {
	*gateway.Gateway
	stop func() error
}

func (g inProcess) Stop() error { return g.stop() }

// StartGateway serves cfg in-process with the attempt's labels and returns
// once it accepts connections.
func StartGateway(ctx context.Context, cfg *casecfg.Config, labels gateway.Labels, version string, log *slog.Logger) (RunningGateway, error) {
	// The runner plays the operator through DecidePending: nobody is asked at
	// the agent's client and no channel carries the question.
	inProc := *cfg
	inProc.Approvals.InProcess = true
	gw, err := gateway.New(&inProc, os.Getenv, labels, version, log)
	if err != nil {
		return nil, err
	}
	gctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- gw.Serve(gctx) }()
	stop := func() error { cancel(); return <-done }

	deadline := time.Now().Add(gatewayReadyTimeout)
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", cfg.Listen)
		if err == nil {
			_ = conn.Close()
			return inProcess{Gateway: gw, stop: stop}, nil
		}
		select {
		case serr := <-done:
			cancel()
			return nil, fmt.Errorf("gateway stopped before serving: %w", serr)
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(fmt.Errorf("gateway not listening on %s", cfg.Listen), stop())
		}
	}
}
