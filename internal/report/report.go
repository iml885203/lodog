// Package report puts every level of one case side by side: the static score
// of each gateway config (lodog score) and the dynamic score of the latest
// run made with it (lodog bench score), plus the L4 list and everything that
// is not measured yet. It reads what is on disk and never runs an agent or
// asks the judge, so a page can always be made, even with no run at all.
package report

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/run"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
)

// ErrUnknownMode is returned for a mode that is not full, read-only or replay.
var ErrUnknownMode = errors.New("unknown run mode (want full, read-only or replay)")

// ErrNoLevels is returned when a case has no gateway config to score.
var ErrNoLevels = errors.New("no gateway config under the case's gateway/ directory")

// Options name the case, where its runs are, and what to probe.
type Options struct {
	CaseDir string
	RunsDir string
	// Origin, when set, probes the public agent standards of the L4 section.
	Origin string
	Client *http.Client
	Now    time.Time
	// Mode picks which runs are scored: full (the default), read-only or
	// replay. Runs of other modes are left out, never compared.
	Mode string
}

// Report is one case, every level side by side.
type Report struct {
	// HasBench is false for a case with no task bank (no benchmark.answers):
	// it can only have static scores.
	HasBench    bool
	Case        string
	RunsDir     string
	Mode        string
	GeneratedAt time.Time
	Levels      []Level
	// L4 is the code-change list of the level with the highest static score:
	// what is left after everything lodog does without touching the code.
	L4      *readiness.L4
	L4Level string
	// Tasks maps a task id to what the operator asked, for the trap replay.
	Tasks map[string]string
	// Expected maps a task id to the strings a right answer includes.
	Expected map[string][]string
	// NotMeasured lists every number the page cannot show, and why.
	NotMeasured []Msg
}

// Level is one gateway config of the case.
type Level struct {
	Name   string
	Config string
	Static readiness.Report
	// Run is the latest run made with this config; nil when there is none.
	Run *Run
	// view is what the config gives the agent; L4 reads runs against it.
	view readiness.View
}

// Run is the dynamic score of one run, rescored offline from what it stored.
type Run struct {
	ID        string
	StartedAt time.Time
	Score     score.Report
	// Calls is every call of the run, as the proxy recorded it; the replay
	// of a trap quotes them.
	Calls []record.Call
	// Answers is the agent's final text per task, of the task's first attempt.
	Answers map[string]string
	// LegacyConfig is the config file the run's meta names when the case no
	// longer has it: the run is matched to its level by the meta's level field.
	LegacyConfig string
	// Drift is how the rescore differs from the score.json stored in the run
	// directory; nil when there is none or they agree.
	Drift *Drift
	// Unjudged counts judge questions with no stored verdict; they count as
	// no, so the run is not complete. UnjudgedTasks names the tasks, one entry
	// per question.
	Unjudged      int
	UnjudgedTasks []string
}

// Build scores every level of the case and the latest run of each.
func Build(ctx context.Context, o Options) (Report, error) {
	c, hasBench, err := loadBench(o.CaseDir)
	if err != nil {
		return Report{}, err
	}
	levels, err := loadLevels(o.CaseDir)
	if err != nil {
		return Report{}, err
	}
	mode := record.ModeOf(o.Mode)
	if mode != record.ModeFull && mode != record.ModeReadOnly && mode != record.ModeReplay {
		return Report{}, fmt.Errorf("%w: %q", ErrUnknownMode, o.Mode)
	}
	runs, err := latestRuns(o.RunsDir, o.CaseDir, mode)
	if err != nil {
		return Report{}, err
	}
	known := map[string]bool{}
	for _, l := range levels {
		known[absPath(l.Config)] = true
	}
	for i := range levels {
		meta, ok := runs.byConfig[absPath(levels[i].Config)]
		legacy := ""
		if !ok {
			// A run made with a config file that no longer exists (it was
			// renamed or merged) is matched by the level its meta records.
			meta, ok = runs.byLevel[levels[i].Static.Level]
			if !ok || known[absPath(meta.GatewayConfig)] {
				continue
			}
			legacy = meta.GatewayConfig
		}
		r, err := scoreRun(c, filepath.Join(o.RunsDir, meta.dir), meta.Meta, levels[i].Static)
		if err != nil {
			return Report{}, err
		}
		r.LegacyConfig = legacy
		levels[i].Run = &r
	}
	rep := Report{HasBench: hasBench, Case: o.CaseDir, RunsDir: o.RunsDir, Mode: mode, GeneratedAt: o.Now, Levels: levels, Tasks: map[string]string{}, Expected: map[string][]string{}}
	for id, t := range c.Tasks {
		rep.Tasks[id] = t.Prompt
		rep.Expected[id] = t.Expect.Include
	}
	if err := rep.pickL4(ctx, o); err != nil {
		return Report{}, err
	}
	rep.NotMeasured = notMeasured(rep, o.Origin != "")
	return rep, nil
}

// loadBench loads the case's task bank. A case with none has no run to score
// and no task text to quote: its page is the static scores alone. A case that
// names a bank loads it whole, so a missing answers file stays an error.
func loadBench(caseDir string) (score.Case, bool, error) {
	has, err := score.HasBenchmark(caseDir)
	if err != nil || !has {
		return score.Case{}, false, err
	}
	c, err := score.LoadCase(caseDir)
	return c, true, err
}

// loadLevels scores every gateway config, in level order. A config the
// gateway rejects (a level not implemented yet) fails the report: a page that
// silently dropped a level would read as if it had been measured.
func loadLevels(caseDir string) ([]Level, error) {
	paths, err := filepath.Glob(filepath.Join(caseDir, "gateway", "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list gateway configs: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoLevels, caseDir)
	}
	levels := make([]Level, 0, len(paths))
	for _, p := range paths {
		v, notes, err := readiness.FromGateway(p)
		if err != nil {
			return nil, fmt.Errorf("static score of %s: %w", p, err)
		}
		static := readiness.Score(v)
		static.Notes = notes
		name := filepath.Base(p)
		levels = append(levels, Level{Name: name[:len(name)-len(filepath.Ext(name))], Config: p, Static: static, view: v})
	}
	sort.SliceStable(levels, func(i, j int) bool {
		a, b := levels[i], levels[j]
		if a.Static.Level != b.Static.Level {
			return a.Static.Level < b.Static.Level
		}
		return a.Name < b.Name
	})
	return levels, nil
}

type runMeta struct {
	run.Meta
	dir string
}

// runIndex is the latest run of the case per gateway config, and per level
// for the runs whose config the case no longer has.
type runIndex struct {
	byConfig, byLevel map[string]runMeta
}

// latestRuns finds, per gateway config, the run of this case in mode that
// started last. A missing runs directory is no runs, not an error.
func latestRuns(runsDir, caseDir, mode string) (runIndex, error) {
	out := runIndex{byConfig: map[string]runMeta{}, byLevel: map[string]runMeta{}}
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("list runs: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, ok, err := readMeta(filepath.Join(runsDir, e.Name()))
		if err != nil {
			return out, err
		}
		if !ok || absPath(m.Case) != absPath(caseDir) || record.ModeOf(m.Mode) != mode {
			continue
		}
		rm := runMeta{Meta: m, dir: e.Name()}
		key := absPath(m.GatewayConfig)
		if prev, seen := out.byConfig[key]; !seen || m.StartedAt.After(prev.StartedAt) {
			out.byConfig[key] = rm
		}
		if prev, seen := out.byLevel[m.Level]; !seen || m.StartedAt.After(prev.StartedAt) {
			out.byLevel[m.Level] = rm
		}
	}
	return out, nil
}

// readMeta reads a run's meta.yaml; a directory without one (the proxy's
// _unlabeled calls) is not a run.
func readMeta(dir string) (run.Meta, bool, error) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, score.MetaFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return run.Meta{}, false, nil
	}
	if err != nil {
		return run.Meta{}, false, fmt.Errorf("read run meta: %w", err)
	}
	var m run.Meta
	if err := yaml.Unmarshal(b, &m); err != nil {
		return run.Meta{}, false, fmt.Errorf("parse %s: %w", filepath.Join(dir, score.MetaFile), err)
	}
	return m, true, nil
}

// scoreRun rescores a run the way `lodog bench score --no-judge` does, with
// the level's static score added, so the page never asks the judge.
func scoreRun(c score.Case, dir string, m run.Meta, static readiness.Report) (Run, error) {
	calls, err := record.ReadFile(filepath.Join(dir, record.FileName))
	if err != nil {
		return Run{}, err
	}
	ev, err := score.LoadEvidence(dir)
	if err != nil {
		return Run{}, err
	}
	s, err := score.Score(c, calls, ev)
	if err != nil {
		return Run{}, fmt.Errorf("score %s: %w", dir, err)
	}
	s.AddStatic(static.Points, static.Max)
	unj := unjudged(c, ev)
	if len(unj) > 0 {
		// A verdict that was never stored counts as no: the total does not
		// measure the whole run.
		s.Complete = false
	}
	drift, err := driftOf(dir, s)
	if err != nil {
		return Run{}, err
	}
	return Run{ID: m.RunID, StartedAt: m.StartedAt, Score: s, Calls: calls, Answers: firstAnswers(ev), Drift: drift, Unjudged: len(unj), UnjudgedTasks: unj}, nil
}

// unjudged lists, per judge question the run has no stored verdict for, the
// task it belongs to (a task repeats once per question).
func unjudged(c score.Case, ev score.Evidence) []string {
	var out []string
	for ref, ran := range ev.Ran {
		t, ok := c.Tasks[ref.Task]
		if !ran || !ok {
			continue
		}
		for _, q := range t.Questions() {
			if _, answered := ev.Verdicts[score.Question{Ref: ref, Text: q}]; !answered {
				out = append(out, ref.Task)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (r *Report) pickL4(ctx context.Context, o Options) error {
	best := -1
	for i, l := range r.Levels {
		if l.Static.L4 != nil && (best < 0 || l.Static.Points > r.Levels[best].Static.Points) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	static := r.Levels[best].Static
	if o.Origin != "" {
		client := o.Client
		if client == nil {
			client = http.DefaultClient
		}
		if err := readiness.CheckStandards(ctx, client, o.Origin, &static); err != nil {
			return err
		}
	}
	r.L4, r.L4Level = readiness.WithRuns(static.L4, r.Levels[best].view, r.runCalls()), r.Levels[best].Name
	return nil
}

// runCalls is every level's latest run: a missing endpoint shows up in the
// runs of the levels below the one whose L4 list is shown.
func (r *Report) runCalls() []readiness.RunCalls {
	var out []readiness.RunCalls
	for _, l := range r.Levels {
		if l.Run == nil {
			continue
		}
		calls := make([]readiness.RunCall, len(l.Run.Calls))
		for i, c := range l.Run.Calls {
			calls[i] = readiness.RunCall{TaskID: c.TaskID, Attempt: c.Attempt, Seq: c.Seq, Method: c.Method, Path: c.Path, Status: c.Status, DryRun: c.DryRun}
		}
		out = append(out, readiness.RunCalls{ID: l.Run.ID, Calls: calls})
	}
	return out
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// levelsOfLodog are the levels docs/levels.md defines below L4; L4 is the
// recommendation list, not a gateway config.
var levelsOfLodog = []casecfg.Level{casecfg.L0, casecfg.L1, casecfg.L2, casecfg.L3}

func notMeasured(r Report, probed bool) []Msg {
	var out []Msg
	have := map[string]bool{}
	for _, l := range r.Levels {
		have[l.Static.Level] = true
	}
	for _, l := range levelsOfLodog {
		if !have[string(l)] {
			out = append(out, M("nm.nolevel", l, filepath.Join(r.Case, "gateway")))
		}
	}
	for _, l := range r.Levels {
		out = append(out, levelGaps(r, l)...)
	}
	out = append(out, versionGaps(r.Levels)...)
	if r.L4 != nil && !probed {
		out = append(out, M("nm.l4probe"))
	}
	return out
}

func levelGaps(r Report, l Level) []Msg {
	if l.Run == nil {
		if !r.HasBench {
			return []Msg{M("nm.nobench", l.Name)}
		}
		return []Msg{M("nm.norun", l.Name, r.RunsDir, l.Config)}
	}
	var out []Msg
	if l.Run.LegacyConfig != "" {
		out = append(out, M("nm.legacy", l.Name, l.Run.ID, l.Run.LegacyConfig, l.Config))
	}
	if l.Run.Drift != nil {
		out = append(out, l.Run.Drift.note(l.Name, *l.Run))
	}
	if l.Run.Unjudged > 0 {
		out = append(out, M("nm.unjudged", l.Name, l.Run.ID, l.Run.Unjudged, "#"+strings.Join(l.Run.UnjudgedTasks, ",")))
	}
	for _, s := range l.Run.Score.NotScored {
		out = append(out, M("nm.notscored", l.Name, l.Run.ID, s))
	}
	return out
}

// versionGaps flags runs that cannot be compared side by side: a run that
// recorded no system version, one whose system did not match the pins, and
// levels whose runs observed different versions of the same pin.
func versionGaps(levels []Level) []Msg {
	var out []Msg
	seen := map[string]map[string][]string{}
	for _, l := range levels {
		if l.Run == nil {
			continue
		}
		if len(l.Run.Score.Versions) == 0 {
			out = append(out, M("nm.nover", l.Name, l.Run.ID))
		}
		for _, v := range l.Run.Score.Versions {
			if !v.Match {
				out = append(out, M("nm.vermismatch", l.Name, l.Run.ID, v.Name, v.Observed, v.Expected))
			}
			if seen[v.Name] == nil {
				seen[v.Name] = map[string][]string{}
			}
			seen[v.Name][v.Observed] = append(seen[v.Name][v.Observed], l.Name)
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if len(seen[n]) > 1 {
			out = append(out, M("nm.verdiff", n))
		}
	}
	return out
}

// HasRuns is true when at least one level has a run.
func (r Report) HasRuns() bool {
	for _, l := range r.Levels {
		if l.Run != nil {
			return true
		}
	}
	return false
}

// ChainGaps names the levels that have no run although a higher level does.
// Levels stack, so the score of the higher one cannot be read as the effect of
// its one step when the step below was never measured.
func (r Report) ChainGaps() []string {
	measured := map[string]bool{}
	top := ""
	for _, l := range r.Levels {
		if l.Run != nil {
			measured[levelKey(l)] = true
			top = max(top, levelKey(l))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range r.Levels {
		k := levelKey(l)
		if !measured[k] && k < top && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// firstAnswers keeps, per task, the answer of its lowest attempt.
func firstAnswers(ev score.Evidence) map[string]string {
	out := map[string]string{}
	first := map[string]int{}
	for ref, text := range ev.Answers {
		if at, seen := first[ref.Task]; seen && at < ref.Attempt {
			continue
		}
		first[ref.Task] = ref.Attempt
		out[ref.Task] = text
	}
	return out
}
