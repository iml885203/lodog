package report

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iml885203/lodog/internal/bench/score"
)

// levelKey is the level a config serves: its L-number, or its name when the
// score did not record one.
func levelKey(l Level) string {
	if l.Static.Level != "" {
		return l.Static.Level
	}
	return l.Name
}

// Tone of the verdict: good, bad or none (nothing measured to judge).
const (
	ToneGood = "good"
	ToneBad  = "bad"
)

// Side is one level's side of a before/after example.
type Side struct {
	// Level is the L-number of the side; the page names it from the dictionary.
	Level string
	// Badge is the one-line outcome of this side.
	Badge Msg
	// Tone is good, bad or empty (neutral).
	Tone string
	// Text is a real piece of the record (the start of an answer, a call) or a
	// sentence about it.
	Text Msg
	// Code is true when Text is a call, shown as code.
	Code  bool
	Calls int
}

// Contrast is one of the owner's three questions with a real before/after.
type Contrast struct {
	Question Msg
	// Available is false when the data holds no example; Note says why.
	Available     bool
	Note          Msg
	TaskID        string
	Prompt        string
	Before, After Side
	// Summary is one sentence stating the difference in plain words.
	Summary Msg
}

// Owner is the before/after examples of the three questions.
type Owner struct {
	Contrasts []Contrast
}

// storyLevels are the levels with one entry per level: where a level has more
// than one gateway config, the one with a run (the latest) stands for it.
func storyLevels(r Report) []Level {
	best := map[string]Level{}
	order := []string{}
	for _, l := range r.Levels {
		key := levelKey(l)
		cur, seen := best[key]
		if !seen {
			order = append(order, key)
			best[key] = l
			continue
		}
		switch {
		case cur.Run == nil && l.Run != nil:
			best[key] = l
		case cur.Run != nil && l.Run != nil && l.Run.StartedAt.After(cur.Run.StartedAt):
			best[key] = l
		}
	}
	out := make([]Level, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return levelKey(out[i]) < levelKey(out[j]) })
	return out
}

// BuildOwner derives the owner-facing page from the report alone: every
// sentence is made from numbers and records of the runs, none is written per case.
func BuildOwner(r Report, replays []Replay) Owner {
	var runs []Level
	for _, l := range storyLevels(r) {
		if l.Run != nil {
			runs = append(runs, l)
		}
	}
	return Owner{Contrasts: []Contrast{understandContrast(r, runs), efficiencyContrast(r, runs), safetyContrast(replays)}}
}

func toneOfRight(right bool) string {
	if right {
		return ToneGood
	}
	return ToneBad
}

// numberText prints whole numbers whole, others with one decimal.
func numberText(f float64) string {
	if f == float64(int(f)) {
		return fmt.Sprint(int(f))
	}
	return fmt.Sprintf("%.1f", f)
}

func taskByID(run *Run, id string) (score.TaskResult, bool) {
	for _, t := range run.Score.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return score.TaskResult{}, false
}

func isRead(t score.TaskType) bool {
	return t == score.TypeLookup || t == score.TypeList || t == score.TypeCrossDomain ||
		t == score.TypeNotFound || t == score.TypeRecoverableError
}

// understandContrast is the first task a read got wrong at one level and right
// at the next: what a clearer description changed.
func understandContrast(r Report, runs []Level) Contrast {
	c := Contrast{Question: M("q.read")}
	for i := 0; i+1 < len(runs); i++ {
		lo, hi := runs[i], runs[i+1]
		for _, t := range lo.Run.Score.Tasks {
			u, ok := taskByID(hi.Run, t.ID)
			if !ok || !isRead(t.Type) || t.AttemptsRun == 0 || u.AttemptsRun == 0 || t.Correctness >= 1 || u.Correctness < 1 {
				continue
			}
			c.Available, c.TaskID, c.Prompt = true, t.ID, r.Tasks[t.ID]
			c.Before = sideOf(lo, t.ID, t.Correctness >= 1)
			c.After = sideOf(hi, t.ID, true)
			c.Summary = M("sum.read", LevelArg(c.Before.Level), LevelArg(c.After.Level))
			return c
		}
	}
	c.Note = M("note.read")
	return c
}

// efficiencyContrast is the read with the biggest drop in calls to the legacy
// system between the first level and a later one that answered it at least as well.
func efficiencyContrast(r Report, runs []Level) Contrast {
	c := Contrast{Question: M("q.choose")}
	if len(runs) < 2 {
		c.Note = M("note.choose.few")
		return c
	}
	base := runs[0]
	bestDrop, bestAt := 0, -1
	var bestTask string
	for _, t := range base.Run.Score.Tasks {
		if !isRead(t.Type) || t.AttemptsRun == 0 {
			continue
		}
		from := callsOf(base.Run, t.ID)
		for i, l := range runs[1:] {
			u, ok := taskByID(l.Run, t.ID)
			if !ok || u.AttemptsRun == 0 || u.Correctness < t.Correctness {
				continue
			}
			if drop := from - callsOf(l.Run, t.ID); drop > bestDrop {
				bestDrop, bestAt, bestTask = drop, i+1, t.ID
			}
		}
	}
	if bestAt < 0 {
		c.Note = M("note.choose")
		return c
	}
	t, _ := taskByID(base.Run, bestTask)
	u, _ := taskByID(runs[bestAt].Run, bestTask)
	c.Available, c.TaskID, c.Prompt = true, bestTask, r.Tasks[bestTask]
	c.Before, c.After = sideOf(base, bestTask, t.Correctness >= 1), sideOf(runs[bestAt], bestTask, u.Correctness >= 1)
	c.Before.Text, c.After.Text = Msg{}, Msg{}
	c.Before.Badge = M("badge.calls", c.Before.Calls, judgeArg(t.Correctness >= 1))
	c.After.Badge = M("badge.calls", c.After.Calls, judgeArg(u.Correctness >= 1))
	c.Before.Tone, c.After.Tone = "", ""
	if c.After.Calls < c.Before.Calls {
		c.After.Tone = ToneGood
	}
	c.Summary = M("sum.choose", LevelArg(c.Before.Level), c.Before.Calls, LevelArg(c.After.Level), c.After.Calls)
	return c
}

// callsOf counts the calls a run made for a task, of its first attempt.
func callsOf(run *Run, task string) int {
	first := 0
	n := 0
	for _, c := range run.Calls {
		if c.TaskID != task {
			continue
		}
		if first == 0 || c.Attempt < first {
			first, n = c.Attempt, 0
		}
		if c.Attempt == first {
			n++
		}
	}
	return n
}

func sideOf(l Level, task string, right bool) Side {
	badge := M("badge.wrong")
	if right {
		badge = M("badge.right")
	}
	return Side{
		Level: levelKey(l), Badge: badge, Tone: toneOfRight(right),
		Text: Raw(excerpt(l.Run.Answers[task], 110)), Calls: callsOf(l.Run, task),
	}
}

// excerpt is the start of an answer in one line, markup stripped.
func excerpt(s string, limit int) string {
	s = strings.NewReplacer("**", "", "`", "", "#", "", "|", " ", "\\n", " ").Replace(s)
	s = strings.Join(strings.Fields(strings.NewReplacer("\n", " ", "-", " ").Replace(s)), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

func safetyContrast(replays []Replay) Contrast {
	c := Contrast{Question: M("q.safe")}
	if len(replays) == 0 {
		c.Note = M("note.safe")
		return c
	}
	rp := replays[0]
	c.Available, c.TaskID, c.Prompt = true, rp.TaskID, rp.Prompt
	c.Before = Side{Level: rp.Open.Kind, Badge: M("badge.unsafe"), Tone: ToneBad}
	c.After = Side{Level: rp.Guarded.Kind, Badge: M("badge.safe"), Tone: ToneGood}
	for _, call := range rp.Open.Calls {
		if call.Danger {
			c.Before.Text, c.Before.Code = Raw(call.Method+" "+call.Path), true
			break
		}
	}
	c.After.Text = M("text.approved")
	if rp.Guarded.Writes == 0 {
		c.After.Text = M("text.nowrite")
	}
	c.Summary = M("sum.safe", LevelArg(c.Before.Level), LevelArg(c.After.Level))
	return c
}

// judgeArg is the word for a right or wrong answer, as a Msg argument.
func judgeArg(right bool) string {
	if right {
		return "@judge.right"
	}
	return "@judge.wrong"
}
