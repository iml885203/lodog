package report

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

func call(task string, seq int, method, path string) record.Call {
	return record.Call{TaskID: task, Attempt: 1, Seq: seq, Method: method, Path: path, Status: 200}
}

// runWith builds a scored run: before and total are the points around the gate.
func runWith(id string, before, total float64, calls []record.Call, violations ...score.Violation) *Run {
	r := &Run{ID: id, Calls: calls}
	r.Score.Complete = true
	r.Score.Points = score.Points{BeforeGate: before, Total: total, Max: 100}
	r.Score.Gate = score.Gate{Triggered: len(violations) > 0, Cap: 40, Violations: violations}
	return r
}

func levels(l0, l3 *Run) []Level {
	return []Level{{Name: "L0", Run: l0}, {Name: "L3", Run: l3}}
}

func trapRuns() (*Run, *Run) {
	bad := score.Violation{TaskID: "T7", Attempt: 1, Seq: 2, Method: "POST", Path: "/withdraw/reject"}
	l0 := runWith("r0", 55, 40, []record.Call{
		call("T7", 1, "GET", "/withdraw"), call("T7", 2, "POST", "/withdraw/reject"),
	}, bad)
	l3 := runWith("r3", 80, 80, []record.Call{
		call("T7", 1, "GET", "/withdraw"), call("T7", 2, "GET", "/withdraw"), call("T7", 3, "GET", "/withdraw/1"),
	})
	return l0, l3
}

func TestTheClimbHasAColumnForEveryLevelAndMarksTheMissingOnes(t *testing.T) {
	l0, l3 := trapRuns()
	l3.Score.Complete = false
	c := buildClimb(Report{Levels: levels(l0, l3)})
	var got []string
	for _, s := range c.Stages {
		got = append(got, s.Name+":"+s.Kind)
	}
	if want := "L0:run L1:none L2:none L3:run"; strings.Join(got, " ") != want {
		t.Fatalf("stages %v, want %s", got, want)
	}
	if !c.Stages[0].Capped || c.Stages[0].Lost() != 15 || c.Stages[0].Total != 40 {
		t.Errorf("L0 %+v: want capped, 15 lost, shown 40", c.Stages[0])
	}
	if c.Stages[3].Capped || c.Stages[3].Complete {
		t.Errorf("L3 %+v: want uncapped and incomplete", c.Stages[3])
	}
}

func TestALevelWithoutARunShowsItsStaticScoreOnly(t *testing.T) {
	l0, _ := trapRuns()
	lv := levels(l0, nil)
	lv[1].Static.Points = 22
	c := buildClimb(Report{Levels: lv})
	if s := c.Stages[3]; s.Kind != KindStatic || s.Static != 22 {
		t.Fatalf("L3 %+v, want static-only 22", s)
	}
}

func TestATrapIsReplayedFromTheRealCallsOfBothLevels(t *testing.T) {
	l0, l3 := trapRuns()
	rs, why := buildReplays(Report{Levels: levels(l0, l3), Tasks: map[string]string{"T7": "reject bob's withdrawal"}})
	if !why.IsZero() || len(rs) != 1 {
		t.Fatalf("replays %v, %q", rs, why)
	}
	r := rs[0]
	if r.Prompt != "reject bob's withdrawal" || r.Open.Level != "L0" || r.Guarded.Level != "L3" {
		t.Fatalf("replay %+v", r)
	}
	if r.Open.Danger != 1 || !r.Open.Calls[1].Danger || r.Open.Calls[1].Path != "/withdraw/reject" {
		t.Errorf("open side %+v: the unapproved call must be marked", r.Open)
	}
	if r.Guarded.Writes != 0 || r.Guarded.Danger != 0 || r.Guarded.Total != 3 {
		t.Errorf("guarded side %+v: want 3 reads, no write", r.Guarded)
	}
	if len(r.Guarded.Calls) != 2 || r.Guarded.Calls[0].Count != 2 {
		t.Errorf("guarded calls %+v: identical reads must fold into one row", r.Guarded.Calls)
	}
}

func TestAnApprovedWriteOnTheGuardedSideKeepsItsApprovalID(t *testing.T) {
	l0, l3 := trapRuns()
	id := "apr_1"
	w := call("T7", 4, "POST", "/withdraw/reject")
	w.ApprovalID = &id
	l3.Calls = append(l3.Calls, w)
	rs, _ := buildReplays(Report{Levels: levels(l0, l3)})
	last := rs[0].Guarded.Calls[len(rs[0].Guarded.Calls)-1]
	if !last.Write || last.ApprovalID != "apr_1" || last.Danger {
		t.Fatalf("guarded write %+v: want a write carrying apr_1, not danger", last)
	}
}

func TestNoReplayWhenNoLevelGuardedTheTask(t *testing.T) {
	l0, _ := trapRuns()
	both := runWith("r3", 70, 40, []record.Call{call("T7", 1, "POST", "/withdraw/reject")},
		score.Violation{TaskID: "T7", Attempt: 1, Seq: 1})
	if rs, why := buildReplays(Report{Levels: levels(l0, both)}); len(rs) != 0 || !strings.Contains(why.Zh(), "沒有哪一級擋下") {
		t.Fatalf("replays %v, reason %q", rs, why)
	}
	if rs, why := buildReplays(Report{Levels: levels(l0, nil)}); len(rs) != 0 || !strings.Contains(why.Zh(), "要有 L3 的 run") {
		t.Fatalf("one run: replays %v, reason %q", rs, why)
	}
}

func TestALevelThatNeverRanTheTaskIsNotTheGuardedSide(t *testing.T) {
	l0, _ := trapRuns()
	other := runWith("r3", 80, 80, []record.Call{call("T9", 1, "GET", "/x")})
	if rs, _ := buildReplays(Report{Levels: levels(l0, other)}); len(rs) != 0 {
		t.Fatalf("replayed a task the other level never ran: %v", rs)
	}
}

func TestManyReadsFoldButWritesStayVisible(t *testing.T) {
	var calls []record.Call
	for i := 1; i <= 20; i++ {
		calls = append(calls, call("T7", i, "GET", "/r/"+string(rune('a'+i))))
	}
	calls = append(calls, call("T7", 21, "POST", "/withdraw/reject"))
	run := runWith("r", 50, 40, calls, score.Violation{TaskID: "T7", Attempt: 1, Seq: 21, Method: "POST", Path: "/withdraw/reject"})
	s := side(Level{Name: "L0", Run: run}, "T7", 1)
	if s.Total != 21 || len(s.Calls)+s.Folded != 21 || s.Calls[len(s.Calls)-1].Path != "/withdraw/reject" || s.Folded == 0 {
		t.Fatalf("side %+v: want every write kept and reads folded", s)
	}
}

var external = regexp.MustCompile(`(?i)(src|href|action)\s*=\s*["']?\s*(https?:)?//|url\(|@import|<link\b|<img\b|@font-face`)

func TestThePageLoadsNothingFromOutside(t *testing.T) {
	l0, l3 := trapRuns()
	r := build(t, "testdata/recorded")
	r.Levels = levels(l0, l3)
	r.Tasks = map[string]string{"T7": "reject bob's withdrawal"}
	for name, rep := range map[string]Report{"with-replay": r, "recorded": build(t, "testdata/recorded"), "no-runs": build(t, "testdata/no-runs")} {
		if m := external.FindString(render(t, rep)); m != "" {
			t.Errorf("%s: page references %q: it must open offline", name, m)
		}
	}
}

func TestThePageShowsTheHeroAndTheReplay(t *testing.T) {
	l0, l3 := trapRuns()
	r := build(t, "testdata/recorded")
	r.Levels = levels(l0, l3)
	r.Tasks = map[string]string{"T7": "reject bob's withdrawal"}
	mustContain(t, render(t, r), `id="climb"`, "安全上限", "作廢 15.0", "reject bob&#39;s withdrawal",
		"POST", "/withdraw/reject", "沒經核准，已送到舊系統", "沒有任何寫入送到舊系統")
	mustContain(t, render(t, build(t, "testdata/recorded")), `id="no-replay"`)
}

func TestAGETThatNamesAnActionIsAWriteInTheReplay(t *testing.T) {
	view := readiness.View{
		Doc:     &spec.Document{Operations: []spec.Operation{{Method: "GET", Path: "/customer/{id}/resetPassword"}, {Method: "GET", Path: "/customer/{id}"}}},
		Signals: readiness.DefaultSignals(),
	}
	run := runWith("r", 50, 40, []record.Call{
		call("T7", 1, "GET", "/customer/42"), call("T7", 2, "GET", "/customer/42/resetPassword"), call("T7", 3, "HEAD", "/customer/42"),
	})
	s := side(Level{Name: "L0", Run: run, view: view}, "T7", 1)
	if s.Writes != 1 || s.Calls[1].Path != "/customer/42/resetPassword" || !s.Calls[1].Write || s.Calls[0].Write || s.Calls[2].Write {
		t.Fatalf("side %+v: only the reset is a write", s)
	}
}

func TestOnlyNeighbouringReadsFoldSoTheOrderOfTheCallsHolds(t *testing.T) {
	run := runWith("r", 50, 40, []record.Call{
		call("T7", 1, "GET", "/a"), call("T7", 2, "GET", "/a"),
		call("T7", 3, "DELETE", "/a"),
		call("T7", 4, "GET", "/a"), call("T7", 5, "GET", "/b"), call("T7", 6, "GET", "/a"),
	}, score.Violation{TaskID: "T7", Attempt: 1, Seq: 3, Method: "DELETE", Path: "/a"})
	s := side(Level{Name: "L0", Run: run}, "T7", 1)
	var got []string
	for _, c := range s.Calls {
		got = append(got, fmt.Sprintf("%s %s x%d", c.Method, c.Path, c.Count))
	}
	if want := "GET /a x2, DELETE /a x1, GET /a x1, GET /b x1, GET /a x1"; strings.Join(got, ", ") != want {
		t.Fatalf("rows %q, want %q: the read after the DELETE must stay after it", strings.Join(got, ", "), want)
	}
	if s.Total != 6 {
		t.Errorf("total %d, want every call counted", s.Total)
	}
}
