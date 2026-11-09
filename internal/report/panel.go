package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/readiness"
)

// Gauge is the big dial: one level's score, pressed down by the safety cap or
// breaking through it.
type Gauge struct {
	Level string
	// Total is the score; Before is the score before the cap.
	Total, Before, Cap float64
	Capped, Complete   bool
	// Measured is false when the level has no run: only the static score exists.
	Measured bool
	Static   float64
}

// Scale is the dial's full mark: 100 for a measured level, the static maximum
// for a level that only has its static score.
func (g Gauge) Scale() float64 {
	if g.Measured {
		return TotalMax
	}
	return readiness.Max
}

// ArcPct is how much of the dial's arc the score fills, in percent of the scale.
func (g Gauge) ArcPct() float64 {
	if g.Measured {
		return g.Total / g.Scale() * 100
	}
	return g.Static / g.Scale() * 100
}

// Lost is what the cap took away.
func (g Gauge) Lost() float64 {
	if !g.Capped {
		return 0
	}
	return g.Before - g.Total
}

// Ring is one sub-score: a ring filled to Pct with its x/y underneath.
type Ring struct {
	Label        Msg
	Center, Frac Msg
	Pct          float64
	// Tone is good, bad or mid; Measured false draws the grey "not checked" ring.
	Tone     string
	Measured bool
}

// HowTo says how a failing check gets fixed: by a level of lodog (no change to
// the legacy system) or by a change to the legacy code (L4).
type HowTo struct {
	// Kind is lodog, code or none.
	Kind string
	Text Msg
}

// Check is one row of the checklist: a task or a static check.
type Check struct {
	ID string
	// Short is the name on the collapsed row.
	Short Msg
	Pass  bool
	// Measured is false when the task was not run.
	Measured bool
	// Changed is true when the check passes here and failed one level below.
	Changed bool
	Goal    Msg
	// Issues say what is wrong, one sentence each.
	Issues   []Msg
	How      HowTo
	Resource string
	// Calls are the calls of the task's first attempt, Answer the start of the
	// agent's answer: the audit details.
	Calls  []ReplayCall
	Answer string
	// Prompt is the text to paste to a coding agent, in each language.
	Prompt map[string]string
}

// Category groups checks under a heading with its x/y.
type Category struct {
	Name          Msg
	Passed, Total int
	Checks        []Check
	NewlyPassed   int
}

// LevelView is everything one level shows: dial, rings, checklist.
type LevelView struct {
	Key string
	// Change is what lodog added at this level, in one line.
	Change Msg
	Gauge  Gauge
	Rings  []Ring
	Cats   []Category
	// Example is the before/after the level solved, when the data holds one.
	Example *Contrast
	// Note is the headline beside the dial, Sub the line under it.
	Note, Sub Msg
	// LostText is what the dial says once the cap pressed the level down.
	LostText Msg
	// NewlyPassed counts the checks that turned right at this level.
	NewlyPassed int
	// Formula and BeforeText are the line under the dial: the score's parts.
	Formula, BeforeText Msg
}

// Improve is the dialog of ways to raise the score.
type Improve struct {
	// Items are the changes to the legacy system, in paste-ready form.
	Items []ImproveItem
	// Text is every item in one block to copy.
	Text string
	// Count is the number of items that still add static points.
	Count int
	Gain  float64
}

// ImproveItem is one change in the Goal / Issue / Fix form.
type ImproveItem struct {
	Goal, Issue, Fix string
	Counted          bool
	Gain             float64
}

// LevelGuide is one row of the "what each level does" table.
type LevelGuide struct {
	Level string
	// Gets is what the agent becomes at this level; Do is what the owner does.
	Gets, Do Msg
	// Tech is the commands and files, for the small expander.
	Tech Msg
	// Does is docs/levels.md's one-liner for the level, in Chinese as the doc has it.
	Does string
	// Prompt is the text to paste to the owner's own agent.
	Prompt Msg
}

// Dash is the whole page: the system as it is, then each level lodog adds.
type Dash struct {
	// Levels are the levels in order, each with its own view.
	Levels []LevelView
	// Base is the view of the lowest level with a run: the system as it is.
	Base LevelView
	// HasAfter is true when a level above the base has a run.
	HasAfter bool
	// Top is the key of the highest level with a run; Default is the level the
	// second view opens at: the first step lodog adds.
	Top, Default string
	Improve      Improve
	Guide        []LevelGuide
}

// levelGuide is the table "what changes for the agent, what you do". The
// commands are the ones docs/getting-started.md uses (in Tech and in the
// prompt); Does is docs/levels.md's one-liner. A test keeps both in step with
// the docs.
var levelGuide = []LevelGuide{
	{"L0", M("g.L0.gets"), M("g.L0.do"), M("g.L0.tech"), "只有原本的 swagger", agentPrompt("L0", "lodog init", "lodog serve")},
	{"L1", M("g.L1.gets"), M("g.L1.do"), M("g.L1.tech", casecfg.OverlayFile), "每支 API 都寫清楚用途和參數", agentPrompt("L1", "lodog enrich", "lodog review")},
	{"L2", M("g.L2.gets"), M("g.L2.do"), M("g.L2.tech", casecfg.WorkflowsFile), "收斂成任務 tool", agentPrompt("L2", "lodog workflows draft")},
	{"L3", M("g.L3.gets"), M("g.L3.do"), M("g.L3.tech", casecfg.PolicyFile), "危險操作要人核准", agentPrompt("L3", "lodog policy draft")},
}

// agentPrompt is what the owner pastes to their own agent to reach a level
// with the lodog-onboard skill (lodog skill install writes it).
func agentPrompt(level string, commands ...string) Msg {
	for i, c := range commands {
		commands[i] = "`" + c + "`"
	}
	return M("g.prompt", level, "#"+strings.Join(commands, ","))
}

// guideRows are the table's rows: the levels, then the change to the legacy code.
func guideRows(im Improve) []LevelGuide {
	rows := append([]LevelGuide{}, levelGuide...)
	l4 := LevelGuide{Level: "L4", Gets: M("g.L4.gets"), Do: M("g.L4.do"), Tech: M("g.L4.tech"), Does: "系統本身支援 agent", Prompt: Raw(im.Text)}
	if im.Gain > 0 {
		l4.Gets = M("g.L4.gets.n", numberText(roundTo(im.Gain)))
	}
	return append(rows, l4)
}

// pickViews is the level the first view shows (the lowest with a run) and the
// one the second opens at (the first step above it, else the top).
func pickViews(levels []LevelView, top string) (base LevelView, second string) {
	base = levels[0]
	for _, v := range levels {
		if v.Gauge.Measured {
			base = v
			break
		}
	}
	second = top
	for _, v := range levels {
		if v.Gauge.Measured && v.Key > base.Key {
			second = v.Key
			break
		}
	}
	if second == "" && len(levels) > 1 {
		// no run anywhere: the first step above the system as it is
		second = levels[1].Key
	}
	return base, second
}

// BuildDash derives the views from the report.
func BuildDash(r Report, st Story) Dash {
	levels := storyLevels(r)
	d := Dash{Improve: buildImprove(r)}
	d.Guide = guideRows(d.Improve)
	capPts := st.Climb.Cap
	passes := make([]map[string]bool, len(levels))
	for i, l := range levels {
		passes[i] = passIndex(l)
	}
	l4 := map[string]readiness.L4Item{}
	if r.L4 != nil {
		for _, it := range r.L4.Items {
			if it.Check != "" {
				l4[it.Check] = it
			}
		}
	}
	runs := 0
	for i, l := range levels {
		var prevPass map[string]bool
		var prev *Level
		if i > 0 {
			prev, prevPass = &levels[i-1], passes[i-1]
		}
		v := buildLevelView(r, l, prev, capPts, prevPass)
		for ci := range v.Cats {
			for ki := range v.Cats[ci].Checks {
				c := &v.Cats[ci].Checks[ki]
				c.How = howOf(*c, levels, passes, i, l4)
				c.Prompt = map[string]string{}
				for _, lang := range Langs {
					c.Prompt[lang] = promptOf(*c, l4, lang)
				}
			}
		}
		v.Example = exampleFor(levelKey(l), st.Owner.Contrasts)
		d.Levels = append(d.Levels, v)
		if l.Run != nil {
			runs++
			d.Top = levelKey(l)
		}
	}
	// A case with no task bank can never have a run, so its second view is each
	// level's static score; a case with a bank gets it once a second level has a run.
	d.HasAfter = runs >= 2 || (!r.HasBench && len(d.Levels) >= 2)
	d.Base, d.Default = pickViews(d.Levels, d.Top)
	return d
}

func exampleFor(level string, cs []Contrast) *Contrast {
	idx := map[string]int{"L1": 0, "L2": 1, "L3": 2}
	i, ok := idx[level]
	if !ok || i >= len(cs) || !cs[i].Available {
		return nil
	}
	c := cs[i]
	return &c
}

// passIndex is, per check of a level, whether it passes there. Static checks
// and tasks have separate id spaces: "s:" and "t:".
func passIndex(l Level) map[string]bool {
	out := map[string]bool{}
	for _, c := range l.Static.Checks {
		out["s:"+c.ID] = c.Points >= c.Max-0.005
	}
	if l.Run != nil {
		for _, t := range l.Run.Score.Tasks {
			if t.AttemptsRun > 0 {
				out["t:"+t.ID] = taskPass(t)
			}
		}
	}
	return out
}

func taskPass(t score.TaskResult) bool {
	pass := t.Correctness >= 1
	if cat := categoryOf(t.Type); cat == score.CategoryHonesty {
		pass = t.Honesty >= 1
	} else if cat == score.CategorySafety {
		pass = t.Safe
	}
	return pass && t.Safe
}

func passKey(c Check) string {
	if c.Resource == resourceTask {
		return "t:" + c.ID
	}
	return "s:" + c.ID
}

func buildLevelView(r Report, l Level, prev *Level, capPts float64, prevPass map[string]bool) LevelView {
	k := levelKey(l)
	v := LevelView{Key: k, Gauge: gaugeOf(l, capPts), Rings: ringsOf(l)}
	v.Note, v.LostText = noteOf(v.Gauge)
	v.Sub = subOf(v.Gauge)
	v.Formula, v.BeforeText = footOf(l, v.Gauge)
	v.Change = changeOf(k, prev, l)
	checks := staticChecks(l)
	if l.Run != nil {
		checks = append(checks, taskChecks(r, l)...)
	}
	cats := map[string]*Category{}
	for _, c := range checks {
		name := staticCategory(c.ID)
		if c.Resource == resourceTask {
			name = taskCategoryName(l, c.ID)
		}
		cat, ok := cats[name]
		if !ok {
			cat = &Category{Name: M(name)}
			cats[name] = cat
		}
		if was, seen := prevPass[passKey(c)]; seen && !was && c.Pass && c.Measured {
			c.Changed = true
			cat.NewlyPassed++
			v.NewlyPassed++
		}
		cat.Checks = append(cat.Checks, c)
		if c.Measured {
			cat.Total++
			if c.Pass {
				cat.Passed++
			}
		}
	}
	for _, n := range []string{catDescribe, catSafety, catPerformance} {
		if c, ok := cats[n]; ok {
			v.Cats = append(v.Cats, *c)
		}
	}
	return v
}

// The checklist's three headings.
const (
	catDescribe    = "cat.describe"
	catSafety      = "cat.safety"
	catPerformance = "cat.performance"
)

const (
	resourceStatic = "docs/static-score.md"
	resourceTask   = "docs/benchmark-task-types.md"
)

func staticCategory(id string) string {
	switch id {
	case "risk_declared", "side_effect_gets", "auth_declared", "agent_identity":
		return catSafety
	}
	return catDescribe
}

func taskCategoryName(l Level, id string) string {
	for _, t := range l.Run.Score.Tasks {
		if t.ID == id && categoryOf(t.Type) == score.CategorySafety {
			return catSafety
		}
	}
	return catPerformance
}

func noteOf(g Gauge) (note, lost Msg) {
	switch {
	case !g.Measured:
		return M("hero.static"), Msg{}
	case g.Capped:
		return M("hero.capped"), lostText(g.Lost())
	}
	return M("hero.clear"), Msg{}
}

// subOf is the second line under the level's name.
func subOf(g Gauge) Msg {
	if g.Measured && g.Capped {
		return M("hero.sub", numberText(g.Cap))
	}
	return Msg{}
}

func lostText(lost float64) Msg {
	if lost <= 0 {
		return Msg{}
	}
	return M("hero.lost", fmt.Sprintf("%.1f", lost))
}

func changeOf(level string, prev *Level, l Level) Msg {
	switch level {
	case "L0", "L1", "L3":
		return M("change." + level)
	case "L2":
		if prev != nil && prev.Static.Tools > 0 {
			return M("change.L2.n", prev.Static.Tools, l.Static.Tools)
		}
		return M("change.L2")
	}
	return Msg{}
}

func gaugeOf(l Level, capPts float64) Gauge {
	g := Gauge{Level: levelKey(l), Cap: capPts, Static: l.Static.Points}
	if l.Run == nil {
		return g
	}
	pt := l.Run.Score.Points
	g.Measured, g.Total, g.Before = true, pt.Total, pt.BeforeGate
	g.Capped, g.Complete = l.Run.Score.Gate.Triggered, l.Run.Score.Complete
	return g
}

func toneOfRatio(x float64) string {
	switch {
	case x >= 0.7:
		return ToneGood
	case x < 0.4:
		return ToneBad
	}
	return "mid"
}

func ring(label string, value, full float64) Ring {
	r := Ring{Label: M(label), Measured: true}
	if full > 0 {
		r.Pct = value / full * 100
	}
	r.Frac = Raw(numberText(roundTo(value)) + "/" + numberText(full))
	r.Center = r.Frac
	r.Tone = toneOfRatio(r.Pct / 100)
	return r
}

// roundTo is f rounded to one decimal.
func roundTo(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func ringsOf(l Level) []Ring {
	notRun := func(label string) Ring { return Ring{Label: M(label), Center: Raw("–"), Frac: M("ring.none")} }
	if l.Run == nil {
		return []Ring{notRun("ring.correct"), notRun("ring.honest"), notRun("ring.safe"), notRun("ring.efficient"), ring("ring.described", l.Static.Points, l.Static.Max)}
	}
	p := l.Run.Score.Points
	safe := ring("ring.safe", p.Safety, 10)
	if n := len(l.Run.Score.Gate.Violations); n > 0 {
		safe.Frac = M("ring.unapproved", n)
		safe.Center = M("ring.times", n)
		safe.Tone = ToneBad
	}
	return []Ring{ring("ring.correct", p.Correctness, 40), ring("ring.honest", p.Honesty, 10), safe, ring("ring.efficient", p.Efficiency, 10), ring("ring.described", p.Static, p.StaticMax)}
}

// checkName is a static check's name in the dictionary, or the name the score
// gave it when the dictionary has none.
func checkName(c readiness.Check) Msg {
	if _, ok := dicts["zh"]["check."+c.ID]; ok {
		return M("check." + c.ID)
	}
	return Raw(c.Name)
}

func staticChecks(l Level) []Check {
	var out []Check
	for _, c := range l.Static.Checks {
		pass := c.Points >= c.Max-0.005
		name := checkName(c)
		ch := Check{ID: c.ID, Short: name, Pass: pass, Measured: true, Resource: resourceStatic}
		ch.Goal = M("goal.static", "@"+name.Key, numberText(c.Max))
		if name.Key == "" {
			ch.Goal = M("goal.static", name.Raw, numberText(c.Max))
		}
		if !pass {
			ch.Issues = []Msg{M("issue.static", numberText(roundTo(c.Points)), numberText(c.Max), c.Detail)}
		}
		if len(c.Examples) > 0 {
			ch.Answer = strings.Join(c.Examples, "；")
		}
		out = append(out, ch)
	}
	return out
}

func categoryOf(t score.TaskType) score.Category {
	for _, info := range score.TaskTypes {
		if info.Type == t {
			return info.Category
		}
	}
	return score.CategoryAnswer
}

func shortTitle(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

func taskChecks(r Report, l Level) []Check {
	tasks := append([]score.TaskResult{}, l.Run.Score.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	var out []Check
	for _, t := range tasks {
		title := r.Tasks[t.ID]
		if title == "" {
			title = t.ID
		}
		c := Check{ID: t.ID, Short: Raw(shortTitle(title, 28)), Measured: t.AttemptsRun > 0, Goal: Raw(title), Resource: resourceTask}
		if c.Measured {
			c.Pass = taskPass(t)
			c.Issues = issuesOf(r, t, c)
			c.Calls, c.Answer = auditOf(l, t.ID)
		}
		out = append(out, c)
	}
	return out
}

// issuesOf says, in one sentence each, why a task did not pass.
func issuesOf(r Report, t score.TaskResult, c Check) []Msg {
	if c.Pass {
		return nil
	}
	var parts []Msg
	if !t.Safe {
		parts = append(parts, M("issue.unsafe"))
	}
	cat := categoryOf(t.Type)
	if cat == score.CategoryHonesty && t.Honesty < 1 {
		parts = append(parts, M("issue.honesty"))
	}
	if cat == score.CategoryAnswer && t.Correctness < 1 {
		if exp := r.Expected[t.ID]; len(exp) > 0 {
			parts = append(parts, M("issue.wrong.expect", strings.Join(exp, ", ")))
		} else {
			parts = append(parts, M("issue.wrong"))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, M("issue.failed"))
	}
	return parts
}

// auditOf are the calls and the answer of a task's first attempt.
func auditOf(l Level, task string) ([]ReplayCall, string) {
	first := 0
	for _, c := range l.Run.Calls {
		if c.TaskID == task && (first == 0 || c.Attempt < first) {
			first = c.Attempt
		}
	}
	s := side(l, task, first)
	return s.Calls, excerpt(l.Run.Answers[task], 300)
}

// howOf says how a failing check gets fixed: the first higher level where it
// passes (lodog does it), else the L4 item that names it (change the code).
func howOf(c Check, levels []Level, passes []map[string]bool, at int, l4 map[string]readiness.L4Item) HowTo {
	if c.Pass || !c.Measured {
		return HowTo{}
	}
	for j := at + 1; j < len(levels); j++ {
		if ok, seen := passes[j][passKey(c)]; seen && ok {
			k := levelKey(levels[j])
			return HowTo{Kind: "lodog", Text: M("how.lodog", LevelArg(k), k)}
		}
	}
	if it, ok := l4[c.ID]; ok && c.Resource == resourceStatic {
		return HowTo{Kind: "code", Text: M("how.code", it.Name, firstSentence(it.Instruction))}
	}
	return HowTo{Kind: "none", Text: M("how.none")}
}

// firstSentence is the text up to the first full stop or line break.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []struct {
		s    string
		keep int
	}{{"。", len("。")}, {".\n", 1}, {". ", 1}, {"\n", 0}} {
		if i := strings.Index(s, sep.s); i > 0 {
			return strings.TrimSpace(s[:i+sep.keep])
		}
	}
	return s
}

// promptOf is the text to paste to a coding agent: the check's Goal and Issue,
// the fix when the L4 list has one, and what the agent did. Goal and Issue are
// written in the given language; the instruction and the calls are data.
func promptOf(c Check, l4 map[string]readiness.L4Item, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", c.Goal.Text(lang))
	if len(c.Issues) > 0 {
		var parts []string
		for _, m := range c.Issues {
			parts = append(parts, m.Text(lang))
		}
		fmt.Fprintf(&b, "\nIssue: %s\n", strings.Join(parts, " "))
	}
	if it, ok := l4[c.ID]; ok && c.Resource == resourceStatic && !c.Pass {
		fmt.Fprintf(&b, "\nFix: %s\n", it.Instruction)
	}
	if len(c.Calls) > 0 {
		b.WriteString("\nWhat the agent did:\n")
		for _, k := range c.Calls {
			fmt.Fprintf(&b, "- %s %s -> %d\n", k.Method, k.Path, k.Status)
		}
	}
	return strings.TrimSpace(b.String())
}

func buildImprove(r Report) Improve {
	var im Improve
	if r.L4 == nil {
		return im
	}
	im.Gain = r.L4.Gain
	var texts []string
	for _, it := range r.L4.Items {
		item := ImproveItem{Goal: it.Name, Issue: strings.TrimSpace(it.Measured + "。" + it.Detail), Fix: it.Instruction, Counted: it.Counted(), Gain: it.Gain}
		im.Items = append(im.Items, item)
		if item.Counted {
			im.Count++
		}
		texts = append(texts, fmt.Sprintf("Goal: %s\n\nIssue: %s\n\nFix: %s", item.Goal, item.Issue, item.Fix))
	}
	im.Text = strings.Join(texts, "\n\n---\n\n")
	return im
}

// footOf is the line under the dial: static plus dynamic points, and the score
// before the cap.
func footOf(l Level, g Gauge) (formula, before Msg) {
	if !g.Measured {
		return M("foot.static", numberText(roundTo(g.Static))), M("foot.nodynamic")
	}
	pt := l.Run.Score.Points
	formula = M("foot.formula", numberText(roundTo(pt.Static)), numberText(roundTo(pt.BeforeGate-pt.Static)))
	if g.Capped {
		return formula, M("foot.before", numberText(roundTo(g.Before)))
	}
	return formula, M("foot.total", numberText(roundTo(g.Total)))
}

// levelJSON is what the page's script needs per level: the numbers of its dial
// and the sentences around it, as Msgs it renders in the chosen language.
type levelJSON struct {
	Key       string  `json:"key"`
	Measured  bool    `json:"measured"`
	Total     float64 `json:"total"`
	BeforeNum float64 `json:"beforeNum"`
	Static    float64 `json:"static"`
	Scale     float64 `json:"scale"`
	Capped    bool    `json:"capped"`
	Note      Msg     `json:"note"`
	Sub       Msg     `json:"sub"`
	Lost      Msg     `json:"lost"`
	Formula   Msg     `json:"formula"`
	Before    Msg     `json:"before"`
}

func (d Dash) levelsJSON() map[string]levelJSON {
	out := map[string]levelJSON{}
	for _, v := range d.Levels {
		out[v.Key] = levelJSON{
			Key: v.Key, Measured: v.Gauge.Measured, Total: v.Gauge.Total, BeforeNum: v.Gauge.Before, Static: v.Gauge.Static, Scale: v.Gauge.Scale(), Capped: v.Gauge.Capped,
			Note: v.Note, Sub: v.Sub, Lost: v.LostText, Formula: v.Formula, Before: v.BeforeText,
		}
	}
	// the base view's level may not be among the levels of a one-run report
	if _, ok := out[d.Base.Key]; !ok {
		v := d.Base
		out[v.Key] = levelJSON{
			Key: v.Key, Measured: v.Gauge.Measured, Total: v.Gauge.Total, BeforeNum: v.Gauge.Before, Static: v.Gauge.Static, Scale: v.Gauge.Scale(), Capped: v.Gauge.Capped,
			Note: v.Note, Sub: v.Sub, Lost: v.LostText, Formula: v.Formula, Before: v.BeforeText,
		}
	}
	return out
}
