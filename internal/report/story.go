package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/readiness"
)

// Kinds of a column in the climb.
const (
	// KindRun is a level with a run: before-gate and total score are known.
	KindRun = "run"
	// KindStatic is a level with a gateway config but no run: only the static score.
	KindStatic = "static"
	// KindNone is a level the case has no gateway config for.
	KindNone = "none"
)

// replayCalls is how many calls a side of the replay lists before it folds
// the rest into a count.
const replayCalls = 8

// Stage is one column of the hero: one level of the climb L0 to L3.
type Stage struct {
	Name string
	Kind string
	// Static is the static score; Before and Total are the dynamic run's score
	// before and after the safety gate. Zero unless Kind is KindRun.
	Static, Before, Total float64
	Capped                bool
	Complete              bool
	RunID                 string
	// Violations counts the dangerous calls made without approval; Changed
	// counts the attempts that left data modified.
	Violations, Changed int
	// Legacy is true when the run was made with a config file the case no
	// longer has (matched by level).
	Legacy bool
	// Drifted is true when the stored score.json of the run differs from the
	// rescore the page shows.
	Drifted bool
	// Start is when the climb reaches this level, in seconds.
	Start float64
}

// Lost is what the gate took away.
func (s Stage) Lost() float64 {
	if !s.Capped {
		return 0
	}
	return s.Before - s.Total
}

// Climb is the hero: the levels in order with the safety threshold.
type Climb struct {
	Stages []Stage
	Cap    float64
}

// ReplayCall is one call of a replayed attempt.
type ReplayCall struct {
	Seq          int
	Method, Path string
	// Count is how many identical reads this row stands for.
	Count          int
	Status         int
	Danger         bool
	Write          bool
	ApprovalID     string
	LegacyNoAnswer bool
}

// ReplaySide is one level's attempt at the trapped task.
type ReplaySide struct {
	Level, RunID string
	// Kind is the L-number of the level, Level the name of its config.
	Kind  string
	Calls []ReplayCall
	// Folded counts the calls beyond the first replayCalls.
	Folded int
	// Total, Writes and Danger count every call of the attempt, folded or not.
	Total, Writes, Danger int
	Changed               bool
}

// Replay is one trap, played twice: the same task where the level at the
// left let the dangerous call through and the one at the right did not.
type Replay struct {
	TaskID, Prompt string
	Open, Guarded  ReplaySide
}

// Story is everything the animated part of the page shows.
type Story struct {
	Climb   Climb
	Owner   Owner
	Dash    Dash
	Replays []Replay
	// NoReplay says why the second act has nothing to play; empty when it has.
	NoReplay Msg
}

// BuildStory derives the hero and the trap replays from the report alone.
func BuildStory(r Report) Story {
	s := Story{Climb: buildClimb(r)}
	s.Replays, s.NoReplay = buildReplays(r)
	s.Owner = BuildOwner(r, s.Replays)
	s.Dash = BuildDash(r, s)
	return s
}

func buildClimb(r Report) Climb {
	c := Climb{Cap: capOf(r)}
	byName := map[string]Level{}
	for _, l := range storyLevels(r) {
		byName[levelKey(l)] = l
	}
	names := make([]string, 0, len(levelsOfLodog))
	for _, l := range levelsOfLodog {
		names = append(names, string(l))
	}
	// A config named outside L0..L3 still gets a column.
	extra := []string{}
	for _, l := range storyLevels(r) {
		if k := levelKey(l); !contains(names, k) {
			extra = append(extra, k)
		}
	}
	for _, n := range append(names, extra...) {
		l, ok := byName[n]
		switch {
		case !ok:
			c.Stages = append(c.Stages, Stage{Name: n, Kind: KindNone})
		case l.Run == nil:
			c.Stages = append(c.Stages, Stage{Name: n, Kind: KindStatic, Static: l.Static.Points})
		default:
			p, g := l.Run.Score.Points, l.Run.Score.Gate
			c.Stages = append(c.Stages, Stage{
				Name: n, Kind: KindRun, Static: l.Static.Points, Before: p.BeforeGate, Total: p.Total,
				Capped: g.Triggered, Complete: l.Run.Score.Complete, RunID: l.Run.ID,
				Violations: len(g.Violations), Changed: len(g.StateChanged), Drifted: l.Run.Drift != nil, Legacy: l.Run.LegacyConfig != "",
			})
		}
	}
	schedule(c.Stages, c.Cap)
	return c
}

func capOf(r Report) float64 {
	for _, l := range r.Levels {
		if l.Run != nil {
			return l.Run.Score.Gate.Cap
		}
	}
	return 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// buildReplays pairs, per task, a run that made a dangerous call without
// approval with the highest-level run that did not. The calls are the proxy's
// record, nothing is made up. A gateway that holds a call never lets it reach
// the proxy, so the guarded side shows the absence of a write, plus any
// approval id on a write that was let through.
func buildReplays(r Report) ([]Replay, Msg) {
	var runs []Level
	for _, l := range r.Levels {
		if l.Run != nil {
			runs = append(runs, l)
		}
	}
	if len(runs) < 2 {
		return nil, needGuardedRun(runs)
	}
	var out []Replay
	seen := map[string]bool{}
	for i, open := range runs {
		for _, v := range open.Run.Score.Gate.Violations {
			if seen[v.TaskID] {
				continue
			}
			g, ok := guardedFor(runs[i+1:], v.TaskID)
			if !ok {
				continue
			}
			seen[v.TaskID] = true
			out = append(out, Replay{
				TaskID: v.TaskID, Prompt: r.Tasks[v.TaskID],
				Open:    side(open, v.TaskID, v.Attempt),
				Guarded: side(g, v.TaskID, v.Attempt),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	if len(out) == 0 {
		return nil, noReplayReason(runs)
	}
	return out, Msg{}
}

// guardedLevel is the level whose run the second act needs: the one with the
// gateway holding dangerous calls.
const guardedLevel = string(casecfg.L3)

// runList is the levels that have a run, as a list argument.
func runList(runs []Level) string {
	names := make([]string, len(runs))
	for i, l := range runs {
		names[i] = levelKey(l)
	}
	return "#" + strings.Join(names, ",")
}

// needGuardedRun is the second act's text while fewer than two levels have a run.
func needGuardedRun(runs []Level) Msg {
	if len(runs) == 0 {
		return M("noreplay.need.none", guardedLevel)
	}
	return M("noreplay.need", guardedLevel, runList(runs))
}

// noReplayReason says why two or more runs still give no pair to replay.
func noReplayReason(runs []Level) Msg {
	dangerous := false
	for _, l := range runs {
		dangerous = dangerous || len(l.Run.Score.Gate.Violations) > 0
	}
	hasGuarded := false
	for _, l := range runs {
		hasGuarded = hasGuarded || levelKey(l) == guardedLevel
	}
	switch {
	case !dangerous:
		return M("noreplay.safe", runList(runs))
	case !hasGuarded:
		return M("noreplay.noguard", guardedLevel, runList(runs))
	default:
		return M("noreplay.both", guardedLevel, runList(runs))
	}
}

// guardedFor is the highest level among the later ones that ran the task and
// made no dangerous call in it.
func guardedFor(later []Level, task string) (Level, bool) {
	for i := len(later) - 1; i >= 0; i-- {
		l := later[i]
		if !ranTask(l.Run.Calls, task) {
			continue
		}
		clean := true
		for _, v := range l.Run.Score.Gate.Violations {
			if v.TaskID == task {
				clean = false
			}
		}
		if clean {
			return l, true
		}
	}
	return Level{}, false
}

func ranTask(calls []record.Call, task string) bool {
	for _, c := range calls {
		if c.TaskID == task {
			return true
		}
	}
	return false
}

func side(l Level, task string, attempt int) ReplaySide {
	bad := dangerSeqs(l.Run.Score.Gate, task, attempt)
	s := ReplaySide{Level: l.Name, Kind: levelKey(l), RunID: l.Run.ID, Changed: changedAttempt(l.Run.Score.Gate, task, attempt)}
	var calls []record.Call
	for _, c := range l.Run.Calls {
		if c.TaskID == task && c.Attempt == attempt {
			calls = append(calls, c)
		}
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].Seq < calls[j].Seq })
	for _, c := range calls {
		rc := replayCall(c, bad[c.Seq], l.view)
		s.Total++
		if rc.Write {
			s.Writes++
		}
		if rc.Danger {
			s.Danger++
		}
		// Only neighbours fold: the rows keep the order of calls.jsonl, so a
		// read after a write stays after it. A write is always its own row.
		if n := len(s.Calls); n > 0 && foldable(s.Calls[n-1], rc) {
			s.Calls[n-1].Count++
			continue
		}
		s.Calls = append(s.Calls, rc)
	}
	s.foldReads()
	return s
}

// replayCall marks a call a write by readiness's judgement of the operation it
// hit, so a GET that names an action (GET /customer/{id}/resetPassword) is a
// write like the POST it stands in for. HEAD never is.
func replayCall(c record.Call, danger bool, v readiness.View) ReplayCall {
	rc := ReplayCall{
		Seq: c.Seq, Method: c.Method, Path: c.Path, Status: c.Status, Count: 1,
		Danger: danger, Write: c.Method != "HEAD" && v.CallChangesState(c.Method, c.Path), LegacyNoAnswer: c.Error != "",
	}
	if c.ApprovalID != nil {
		rc.ApprovalID = *c.ApprovalID
	}
	return rc
}

// foldReads keeps every write and danger row; the reads beyond the cap fold
// into a count.
func (s *ReplaySide) foldReads() {
	if len(s.Calls) <= replayCalls {
		return
	}
	kept := make([]ReplayCall, 0, replayCalls)
	for _, c := range s.Calls {
		if c.Danger || c.Write || len(kept) < replayCalls-s.Writes {
			kept = append(kept, c)
		} else {
			s.Folded += c.Count
		}
	}
	s.Calls = kept
}

// dangerSeqs are the seq numbers of the attempt's unapproved dangerous calls.
func dangerSeqs(g score.Gate, task string, attempt int) map[int]bool {
	bad := map[int]bool{}
	for _, v := range g.Violations {
		if v.TaskID == task && v.Attempt == attempt {
			bad[v.Seq] = true
		}
	}
	return bad
}

func changedAttempt(g score.Gate, task string, attempt int) bool {
	return contains(g.StateChanged, fmt.Sprintf("%s attempt %d", task, attempt))
}

// foldable is true when next repeats prev: the same read, answered the same way.
func foldable(prev, next ReplayCall) bool {
	return !prev.Write && !prev.Danger && !next.Write && !next.Danger &&
		prev.Method == next.Method && prev.Path == next.Path && prev.Status == next.Status
}
