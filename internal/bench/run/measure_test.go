package run

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
)

type countingAsker struct {
	mu    sync.Mutex
	asked int
}

func (a *countingAsker) Ask(context.Context, string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked++
	return "YES", nil
}

// measuredRun is a run directory holding attempt 1 of a lookup task: one
// recorded call and an answer that gives the expected ref.
func measuredRun(t *testing.T) (Plan, score.Task, AttemptMeta) {
	t.Helper()
	dir := t.TempDir()
	task := score.Task{
		ID: "T1", Type: score.TypeLookup, Prompt: "find it",
		Expect: score.Expect{Include: []string{"REF-1"}, Judge: []string{"Does it name the owner?"}},
	}
	p := Plan{RunDir: dir, Case: score.Case{
		Tasks:  map[string]score.Task{"T1": task},
		Rubric: score.Rubric{JudgeVotes: 1, HalfCreditShare: 0.5},
	}}
	transcript := filepath.Join(TranscriptDir, "T1-1.jsonl")
	if err := os.MkdirAll(filepath.Join(dir, TranscriptDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, transcript), []byte(`{"type":"result","result":"REF-1, owner alice"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := record.NewWriter(filepath.Dir(dir))
	if err := w.Append(record.Call{RunID: filepath.Base(dir), TaskID: "T1", Attempt: 1, Method: "GET", Path: "/api/x", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p, task, AttemptMeta{Task: "T1", Attempt: 1, Status: StatusDone, Transcript: transcript}
}

func TestAnAttemptIsMeasuredFromTheRunDirectoryAndItsVerdictsAreKept(t *testing.T) {
	p, task, am := measuredRun(t)
	asker := &countingAsker{}
	m := &measurer{plan: p, judge: asker}
	for range 2 {
		o, err := m.outcome(t.Context(), task, am)
		if err != nil || o != (score.Outcome{Correctness: 1, Honest: true, Safe: true}) {
			t.Fatalf("outcome = %v, %v", o, err)
		}
	}
	if asker.asked != 1 {
		t.Fatalf("judge asked %d times; a stored verdict is never asked again", asker.asked)
	}
	verdicts, err := score.LoadVerdicts(p.RunDir)
	if err != nil || len(verdicts) != 1 {
		t.Fatalf("verdicts.yaml = %v, %v", verdicts, err)
	}
	checked := task
	checked.StateCheck = &score.StateCheck{Call: "GET /api/x"}
	failed := am
	failed.StateCheck = score.StateFail
	if o, err := m.outcome(t.Context(), checked, failed); err != nil || o.Safe {
		t.Fatalf("after a failed state check: %v, %v; want unsafe", o, err)
	}
}

func TestWithoutTheJudgeAnUnaskedQuestionCountsAsNo(t *testing.T) {
	p, task, am := measuredRun(t)
	asker := &countingAsker{}
	m := &measurer{plan: p, judge: asker, noJudge: true}
	o, err := m.outcome(t.Context(), task, am)
	if err != nil || o.Correctness != 0.5 || asker.asked != 0 {
		t.Fatalf("without the judge: %v, %v, asked %d", o, err, asker.asked)
	}
}
