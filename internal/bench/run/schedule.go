package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iml885203/lodog/internal/bench/score"
)

// Run executes the plan and keeps meta.yaml current after every attempt, so
// an interrupted run still scores the attempts it finished. Shareable tasks
// run first, on data reset once, Plan.Parallel attempts at a time; then every
// other task runs alone, each attempt after its own reset.
func Run(ctx context.Context, p Plan, d Deps) error {
	if err := os.MkdirAll(filepath.Join(p.RunDir, TranscriptDir), 0o750); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &runner{p: p, d: d, cancel: cancel, meta: p.Header}
	r.meta.StartedAt = d.Now().UTC()
	if first, most := p.rounds(); first < most && d.Outcome != nil {
		r.meta.MinAttemptsPerTask = first
	}
	shared, alone := p.split()
	if len(shared) > 0 {
		r.meta.Parallel = p.slots()
	}
	if err := r.write(); err != nil {
		return err
	}
	if err := r.runShared(ctx, shared); err != nil {
		return err
	}
	for _, id := range alone {
		if r.stopped() {
			break
		}
		task := p.Case.Tasks[id]
		r.runTask(task, func(ns []int) []AttemptMeta {
			var out []AttemptMeta
			for _, n := range ns {
				if r.stopped() {
					break
				}
				out = append(out, r.attempt(ctx, task, n, 0, true))
			}
			return out
		})
	}
	if err := r.result(); err != nil {
		return err
	}
	r.meta.FinishedAt = d.Now().UTC()
	return r.write()
}

// split puts the plan's tasks in the order they run: shareable ones first.
func (p Plan) split() (shared, alone []string) {
	for _, id := range p.TaskIDs {
		if p.Case.Tasks[id].Shareable() {
			shared = append(shared, id)
		} else {
			alone = append(alone, id)
		}
	}
	return shared, alone
}

// rounds is how many attempts a task makes before they are compared, and the
// most it makes.
func (p Plan) rounds() (first, most int) {
	if p.MinAttempts > 0 && p.MinAttempts < p.Attempts {
		return p.MinAttempts, p.Attempts
	}
	return p.Attempts, p.Attempts
}

func (p Plan) slots() int {
	return max(1, p.Parallel)
}

// runner holds what attempts running at once share: the manifest, the count
// of failures in a row, and the error that stopped the run.
type runner struct {
	p        Plan
	d        Deps
	cancel   context.CancelFunc
	mu       sync.Mutex
	meta     Meta
	failures int
	err      error
}

// runShared resets the data once and runs every attempt of the shareable
// tasks, at most Plan.Parallel at a time, each in a slot no other running
// attempt holds.
func (r *runner) runShared(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, r.p.AttemptTimeout)
	err := r.d.Reset(rctx)
	cancel()
	if err != nil {
		return fmt.Errorf("reset before the shareable tasks: %w", err)
	}
	free := make(chan int, r.p.slots())
	for slot := range r.p.slots() {
		free <- slot
	}
	var tasks sync.WaitGroup
	for _, id := range ids {
		task := r.p.Case.Tasks[id]
		tasks.Go(func() {
			r.runTask(task, func(ns []int) []AttemptMeta {
				return r.parallel(ctx, task, ns, free)
			})
		})
	}
	tasks.Wait()
	return nil
}

func (r *runner) parallel(ctx context.Context, task score.Task, ns []int, free chan int) []AttemptMeta {
	out := make([]AttemptMeta, len(ns))
	ran := make([]bool, len(ns))
	var attempts sync.WaitGroup
	for i, n := range ns {
		attempts.Go(func() {
			select {
			case slot := <-free:
				defer func() { free <- slot }()
				if !r.stopped() {
					out[i], ran[i] = r.attempt(ctx, task, n, slot, false), true
				}
			case <-ctx.Done():
			}
		})
	}
	attempts.Wait()
	var done []AttemptMeta
	for i, am := range out {
		if ran[i] {
			done = append(done, am)
		}
	}
	return done
}

// runTask makes the task's first round of attempts and, unless they agree,
// the rest up to the most the plan allows.
func (r *runner) runTask(task score.Task, batch func(ns []int) []AttemptMeta) {
	first, most := r.p.rounds()
	done := batch(numbers(1, first))
	if first == most || r.stopped() || r.agree(task, done, first) {
		return
	}
	batch(numbers(first+1, most))
}

// agree reports whether want attempts were measured and all had the same
// outcome. A failed attempt, or one whose outcome could not be measured, has
// none, so the task runs on to the cap rather than stop on a guess.
func (r *runner) agree(task score.Task, done []AttemptMeta, want int) bool {
	var outcomes []score.Outcome
	for _, am := range done {
		if am.Outcome != nil {
			outcomes = append(outcomes, *am.Outcome)
		}
	}
	stop := len(outcomes) >= want && score.Agreement(outcomes) == len(outcomes)
	if stop {
		r.d.Log.Info("attempts agree; no more attempts", "task", task.ID, "attempts", len(outcomes))
	}
	return stop
}

func numbers(from, to int) []int {
	var ns []int
	for n := from; n <= to; n++ {
		ns = append(ns, n)
	}
	return ns
}

// attempt runs one attempt, measures it when the plan compares attempts, and
// records it.
func (r *runner) attempt(ctx context.Context, task score.Task, n, slot int, reset bool) AttemptMeta {
	am, err := runAttempt(ctx, r.p, r.d, task, n, slot, reset)
	if first, most := r.p.rounds(); err == nil && first < most && r.d.Outcome != nil {
		o, oerr := r.d.Outcome(ctx, task, am)
		if oerr != nil {
			r.d.Log.Warn("attempt not measured; the task makes every attempt", "task", task.ID, "attempt", n, "err", oerr)
		} else {
			am.Outcome = &o
		}
	}
	r.record(ctx, am, err)
	return am
}

// record adds the attempt to meta.yaml and stops the run on a fatal error or
// too many failures in a row.
func (r *runner) record(ctx context.Context, am AttemptMeta, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta.Attempts = append(r.meta.Attempts, am)
	if werr := WriteMeta(r.p.RunDir, r.meta); werr != nil {
		r.stop(werr)
		return
	}
	if err == nil {
		r.failures = 0
		return
	}
	r.failures++
	switch {
	case fatal(ctx, err):
		r.stop(fmt.Errorf("%s attempt %d: %w", am.Task, am.Attempt, err))
	case r.failures >= MaxConsecutiveFailures:
		r.stop(fmt.Errorf("%w (%d), last %s attempt %d: %w", ErrConsecutiveFailures, r.failures, am.Task, am.Attempt, err))
	default:
		r.d.Log.Error("attempt failed", "task", am.Task, "attempt", am.Attempt, "err", err)
	}
}

// stop keeps the first error that ends the run and cancels the attempts
// still running. The caller holds mu.
func (r *runner) stop(err error) {
	if r.err == nil {
		r.err = err
		r.cancel()
	}
}

func (r *runner) stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err != nil
}

func (r *runner) result() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *runner) write() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return WriteMeta(r.p.RunDir, r.meta)
}
