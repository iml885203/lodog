package report

import (
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
)

// ownerLevel is a level with a run: its tasks, calls, answers and how many
// dangerous calls it made without approval.
func ownerLevel(name string, dangerous int, tasks []score.TaskResult, calls []record.Call, answers map[string]string) Level {
	run := runWith("run-"+name, 60, 60, calls)
	run.Score.Tasks = tasks
	run.Score.TasksRun = len(tasks)
	run.Answers = answers
	for i := 0; i < dangerous; i++ {
		run.Score.Gate.Violations = append(run.Score.Gate.Violations, score.Violation{TaskID: "W", Attempt: 1, Seq: i + 1})
	}
	return Level{Name: name, Static: readiness.Report{Level: name}, Run: run}
}

func task(id string, typ score.TaskType, correct float64) score.TaskResult {
	return score.TaskResult{ID: id, Type: typ, AttemptsRun: 1, Correctness: correct}
}

func reads(taskID string, n int) []record.Call {
	var out []record.Call
	for i := 1; i <= n; i++ {
		out = append(out, call(taskID, i, "GET", "/x"))
	}
	return out
}

func TestTheUnderstandExampleIsATaskWrongBeforeAndRightAfterWithTheRealAnswers(t *testing.T) {
	r := Report{
		Tasks: map[string]string{"A": "who is alice"}, Expected: map[string][]string{"A": {"alice"}},
		Levels: []Level{
			ownerLevel("L0", 0, []score.TaskResult{task("A", score.TypeLookup, 0), task("W", score.TypeGatedWrite, 0)}, nil, map[string]string{"A": "I could not tell."}),
			ownerLevel("L1", 0, []score.TaskResult{task("A", score.TypeLookup, 1), task("W", score.TypeGatedWrite, 1)}, nil, map[string]string{"A": "**alice** is player 1"}),
		},
	}
	c := BuildOwner(r, nil).Contrasts[0]
	if !c.Available || c.TaskID != "A" || c.Before.Tone != ToneBad || c.After.Tone != ToneGood || c.Before.Text.Raw != "I could not tell." || c.After.Text.Raw != "alice is player 1" {
		t.Fatalf("contrast %+v", c)
	}
}

func TestTheUnderstandExampleIgnoresWritesAndSaysSoWhenThereIsNone(t *testing.T) {
	r := Report{Levels: []Level{
		ownerLevel("L0", 0, []score.TaskResult{task("W", score.TypeGatedWrite, 0)}, nil, nil),
		ownerLevel("L1", 0, []score.TaskResult{task("W", score.TypeGatedWrite, 1)}, nil, nil),
	}}
	c := BuildOwner(r, nil).Contrasts[0]
	if c.Available || !strings.Contains(c.Note.Zh(), "沒有哪一題") {
		t.Fatalf("contrast %+v: a write that turned right is not a reading example", c)
	}
}

func TestTheEfficiencyExampleIsTheBiggestDropInCallsAtLeastAsRight(t *testing.T) {
	r := Report{Tasks: map[string]string{"A": "a", "B": "b"}, Levels: []Level{
		ownerLevel("L0", 0, []score.TaskResult{task("A", score.TypeList, 1), task("B", score.TypeList, 1)}, append(reads("A", 21), reads("B", 5)...), nil),
		ownerLevel("L2", 0, []score.TaskResult{task("A", score.TypeList, 1), task("B", score.TypeList, 0)}, append(reads("A", 12), reads("B", 1)...), nil),
	}}
	c := BuildOwner(r, nil).Contrasts[1]
	if !c.Available || c.TaskID != "A" || c.Before.Calls != 21 || c.After.Calls != 12 || c.After.Tone != ToneGood {
		t.Fatalf("contrast %+v: want A (21 to 12); B dropped calls but became wrong", c)
	}
	if !strings.Contains(c.Summary.Zh(), "次數少不等於答對") {
		t.Errorf("summary %q must not equate fewer calls with right answers", c.Summary)
	}
}

func TestTheSafetyExampleIsTheFirstReplayAndLinksToTheEvidence(t *testing.T) {
	l0, l3 := trapRuns()
	r := Report{Tasks: map[string]string{"T7": "reject"}, Levels: levels(l0, l3)}
	replays, why := buildReplays(r)
	if len(replays) == 0 {
		t.Fatalf("no replays: %s", why)
	}
	c := BuildOwner(r, replays).Contrasts[2]
	if !c.Available || !c.Before.Code || c.Before.Text.Raw != "POST /withdraw/reject" || !strings.Contains(c.After.Text.Zh(), "沒有收到任何寫入") {
		t.Fatalf("contrast %+v", c)
	}
	none := BuildOwner(r, nil).Contrasts[2]
	if none.Available || none.Note.IsZero() {
		t.Fatalf("no replay must say so: %+v", none)
	}
}

func TestOneConfigStandsForALevelAndTheOneWithARunWins(t *testing.T) {
	hand := Level{Name: "L3", Static: readiness.Report{Level: "L3"}}
	auto := ownerLevel("L3-auto", 0, nil, nil, nil)
	auto.Static.Level = "L3"
	got := storyLevels(Report{Levels: []Level{hand, auto}})
	if len(got) != 1 || got[0].Name != "L3-auto" {
		t.Fatalf("levels %v, want the config with the run", got)
	}
	l0 := ownerLevel("L0", 0, nil, nil, nil)
	l2 := Level{Name: "L2", Static: readiness.Report{Level: "L2"}}
	if gaps := (Report{Levels: []Level{l0, hand, l2, auto}}).ChainGaps(); strings.Join(gaps, ",") != "L2" {
		t.Fatalf("gaps %v: L3 has a run through its other config, only L2 is missing", gaps)
	}
}

func TestAnswersAreCutToOneLineWithoutMarkup(t *testing.T) {
	if got := excerpt("**Yes**\n\n| a | b |\n- item `x`", 100); got != "Yes a b item x" {
		t.Fatalf("excerpt %q", got)
	}
	if got := excerpt(strings.Repeat("字", 200), 10); got != strings.Repeat("字", 10)+"…" {
		t.Fatalf("excerpt %q", got)
	}
}
