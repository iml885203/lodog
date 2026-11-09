package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/iml885203/lodog/internal/bench/score"
)

// storedScoreFile is what `lodog bench score` writes into a run directory.
const storedScoreFile = "score.json"

// Drift says a run's stored score.json and its rescore from the case as it is
// now disagree. The rescore uses the stored judge verdicts, so the usual cause
// is a case whose tasks or expected answers changed after the run was scored.
type Drift struct {
	StoredBeforeGate, StoredTotal float64
	// Tasks name the tasks whose result differs, with how.
	Tasks []string
}

// driftOf compares the rescored report with the run's score.json. A run
// without one has nothing to drift from.
func driftOf(dir string, now score.Report) (*Drift, error) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, storedScoreFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, storedScoreFile), err)
	}
	var old score.Report
	if err := json.Unmarshal(b, &old); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, storedScoreFile), err)
	}
	d := &Drift{StoredBeforeGate: old.Points.BeforeGate, StoredTotal: old.Points.Total, Tasks: taskDrift(old, now)}
	if len(d.Tasks) == 0 && same(old.Points.BeforeGate, now.Points.BeforeGate) && same(old.Points.Total, now.Points.Total) {
		return nil, nil
	}
	return d, nil
}

func same(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func taskDrift(old, now score.Report) []string {
	was := map[string]score.TaskResult{}
	for _, t := range old.Tasks {
		was[t.ID] = t
	}
	var out []string
	for _, t := range now.Tasks {
		w, ok := was[t.ID]
		if !ok {
			out = append(out, t.ID+" (not in score.json)")
			continue
		}
		var diff []string
		if !same(w.Correctness, t.Correctness) {
			diff = append(diff, fmt.Sprintf("correct %.2f→%.2f", w.Correctness, t.Correctness))
		}
		if !same(w.Honesty, t.Honesty) {
			diff = append(diff, fmt.Sprintf("honest %.2f→%.2f", w.Honesty, t.Honesty))
		}
		if w.Safe != t.Safe {
			diff = append(diff, fmt.Sprintf("safe %t→%t", w.Safe, t.Safe))
		}
		if len(diff) > 0 {
			out = append(out, t.ID+" ("+strings.Join(diff, ", ")+")")
		}
	}
	return out
}

// note is the sentence the page's list of what is not measured gets.
func (d Drift) note(level string, run Run) Msg {
	args := []any{
		level, run.ID, fmt.Sprintf("%.2f", d.StoredBeforeGate), fmt.Sprintf("%.2f", d.StoredTotal),
		fmt.Sprintf("%.2f", run.Score.Points.BeforeGate), fmt.Sprintf("%.2f", run.Score.Points.Total),
	}
	if len(d.Tasks) == 0 {
		return M("nm.drift.same", args...)
	}
	return M("nm.drift.tasks", append(args, strings.Join(d.Tasks, "; "))...)
}
