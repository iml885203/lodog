package run

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/iml885203/lodog/internal/bench/grade"
	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/llm"
	"github.com/iml885203/lodog/internal/readiness"
)

// ScoreRun scores one finished run of the case in caseDir: it reads the run's
// recorded calls and evidence, asks the judge the questions the evidence left
// open (unless noJudge, when they count as no) and adds the static score of
// the gateway config the run used.
func ScoreRun(ctx context.Context, caseDir, runDir, claudeBin string, noJudge bool) (score.Report, error) {
	c, err := score.LoadCase(caseDir)
	if err != nil {
		return score.Report{}, err
	}
	calls, err := record.ReadFile(filepath.Join(runDir, record.FileName))
	if err != nil {
		return score.Report{}, err
	}
	ev, err := score.LoadEvidence(runDir)
	if err != nil {
		return score.Report{}, err
	}
	if !noJudge {
		judge := ClaudeJudge{Claude: llm.Claude{Bin: claudeBin, Model: c.Rubric.JudgeModel, Effort: c.Rubric.JudgeEffort, WorkDir: runDir, Exec: llm.Command}}
		if err := grade.Judge(ctx, c, &ev, runDir, judge, c.Rubric.JudgeVotes); err != nil {
			return score.Report{}, err
		}
	}
	var view readiness.View
	if ev.GatewayConfig != "" {
		if view, _, err = readiness.FromGateway(ev.GatewayConfig); err != nil {
			return score.Report{}, fmt.Errorf("static score: %w", err)
		}
		ev.Exposure = ExposureOf(view)
	}
	report, err := score.Score(c, calls, ev)
	if err != nil {
		return score.Report{}, fmt.Errorf("score %s: %w", runDir, err)
	}
	if ev.GatewayConfig != "" {
		static := readiness.Score(view)
		report.AddStatic(static.Points, static.Max)
	}
	return report, nil
}

// ExposureOf is what the level's gateway gives the agent, as the scorer asks
// for it when it tells a hidden trap from one the gateway stopped.
func ExposureOf(view readiness.View) func(method, path string) score.Exposure {
	return func(method, path string) score.Exposure {
		switch reached, hidden := view.Reach(method, path); {
		case reached:
			return score.ExposureOffered
		case hidden:
			return score.ExposureHidden
		}
		return score.ExposureAbsent
	}
}
