package run

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/iml885203/lodog/internal/bench/grade"
	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/llm"
)

// measurer scores one finished attempt while the run goes on, from the same
// records `lodog bench score` reads: the proxy's calls.jsonl in the run
// directory, the transcript, the state check and the judge's verdicts. The
// verdicts it asks for are saved, so scoring the run later asks none again.
type measurer struct {
	plan    Plan
	judge   grade.Asker
	noJudge bool
	// mu guards verdicts.yaml, which every slot's attempts add to.
	mu sync.Mutex
}

func newMeasurer(o Options, p Plan) *measurer {
	r := p.Case.Rubric
	return &measurer{
		plan:    p,
		judge:   ClaudeJudge{llm.Claude{Bin: o.ClaudeBin, Model: r.JudgeModel, Effort: r.JudgeEffort, WorkDir: p.RunDir, Exec: llm.Command}},
		noJudge: o.NoJudge,
	}
}

func (m *measurer) outcome(ctx context.Context, task score.Task, am AttemptMeta) (score.Outcome, error) {
	ref := score.Ref{Task: task.ID, Attempt: am.Attempt}
	calls, err := record.ReadFile(filepath.Join(m.plan.RunDir, record.FileName))
	if err != nil {
		return score.Outcome{}, fmt.Errorf("read the proxy's calls (is it writing to this run's directory?): %w", err)
	}
	answer, err := score.ReadAnswer(filepath.Join(m.plan.RunDir, am.Transcript))
	if err != nil {
		return score.Outcome{}, err
	}
	ev := score.Evidence{
		Ran: map[score.Ref]bool{ref: true}, Answers: map[score.Ref]string{ref: answer},
		StateChecks: map[score.Ref]bool{}, Mode: m.plan.Header.Mode,
	}
	if am.StateCheck != "" {
		ev.StateChecks[ref] = am.StateCheck == score.StatePass
	}
	ev.Verdicts, err = m.verdicts(ctx, task, ref, answer)
	if err != nil {
		return score.Outcome{}, err
	}
	return score.AttemptOutcome(m.plan.Case, task, am.Attempt, calls, ev), nil
}

// verdicts asks the judge the task's questions about this attempt that have
// no stored verdict, outside the lock so slots judge at the same time, and
// stores the answers. A question the judge gives no verdict for counts as
// no and stays unstored, as in grade.Judge.
func (m *measurer) verdicts(ctx context.Context, task score.Task, ref score.Ref, answer string) (map[score.Question]bool, error) {
	known, err := m.load()
	if err != nil {
		return nil, err
	}
	asked := map[score.Question]bool{}
	for _, q := range task.Questions() {
		key := score.Question{Ref: ref, Text: q}
		if _, done := known[key]; done || m.noJudge {
			continue
		}
		yes, err := grade.Verdict(ctx, m.judge, task, answer, q, m.plan.Case.Rubric.JudgeVotes)
		if errors.Is(err, grade.ErrUnclearVerdict) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("judge %s attempt %d: %w", ref.Task, ref.Attempt, err)
		}
		asked[key] = yes
	}
	return m.save(asked)
}

func (m *measurer) load() (map[score.Question]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return score.LoadVerdicts(m.plan.RunDir)
}

func (m *measurer) save(asked map[score.Question]bool) (map[score.Question]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := score.LoadVerdicts(m.plan.RunDir)
	if err != nil {
		return nil, err
	}
	for k, v := range asked {
		all[k] = v
	}
	if len(asked) > 0 {
		if err := score.SaveVerdicts(m.plan.RunDir, all); err != nil {
			return nil, err
		}
	}
	return all, nil
}
