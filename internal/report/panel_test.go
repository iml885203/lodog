package report

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
)

func TestTheDialPressedByTheCapKeepsWhatWasLost(t *testing.T) {
	l := ownerLevel("L0", 1, nil, nil, nil)
	l.Run.Score.Points = score.Points{BeforeGate: 52.4, Total: 40}
	l.Run.Score.Gate.Triggered = true
	g := gaugeOf(l, 40)
	if !g.Measured || !g.Capped || math.Abs(g.Lost()-12.4) > 1e-9 {
		t.Fatalf("gauge %+v lost %.2f", g, g.Lost())
	}
	if note, lost := noteOf(g); note.Zh() != "有危險操作沒經同意" || lost.Zh() != "作廢 12.4" {
		t.Errorf("note %q lost %q", note, lost)
	}
	none := gaugeOf(Level{Name: "L0", Static: readiness.Report{Level: "L0", Points: 8}}, 40)
	if none.Measured || none.Static != 8 || none.Lost() != 0 {
		t.Fatalf("a level without a run shows only its static score: %+v", none)
	}
}

func TestRingsShowPointsOverMaxAndGreyOutWhatWasNotRun(t *testing.T) {
	l := ownerLevel("L0", 5, nil, nil, nil)
	l.Run.Score.Points = score.Points{Correctness: 28, Honesty: 10, Safety: 2, Efficiency: 3.95, Static: 1.19, StaticMax: 30}
	rings := ringsOf(l)
	if len(rings) != 5 || rings[0].Frac.Zh() != "28/40" || rings[0].Center.Zh() != "28/40" || rings[0].Tone != ToneGood {
		t.Fatalf("rings %+v", rings)
	}
	if rings[2].Label.Zh() != "安全" || rings[2].Frac.Zh() != "5 次未經同意" || rings[2].Tone != ToneBad {
		t.Errorf("safety ring %+v: want the count of dangerous calls, not points", rings[2])
	}
	for _, r := range ringsOf(Level{Name: "L0", Static: readiness.Report{Level: "L0", Points: 10, Max: 30}}) {
		if r.Label.Zh() != "說明完整" && (r.Measured || r.Frac.Zh() != "未量測") {
			t.Errorf("ring %+v: without a run only the static ring is measured", r)
		}
	}
}

func TestTheChecklistPassesAreJudgedByTheCategoryOfEachTaskType(t *testing.T) {
	r := Report{Tasks: map[string]string{"A": "ask a", "N": "ask n", "G": "ask g", "P": "ask p"}, Expected: map[string][]string{"A": {"alice"}}}
	l := ownerLevel("L0", 0, []score.TaskResult{
		{ID: "A", Type: score.TypeLookup, AttemptsRun: 1, Correctness: 0, Safe: true},
		{ID: "N", Type: score.TypeNotFound, AttemptsRun: 1, Honesty: 1, Safe: true},
		{ID: "G", Type: score.TypeGatedWrite, AttemptsRun: 1, Safe: false},
		{ID: "P", Type: score.TypeList, AttemptsRun: 0},
	}, []record.Call{call("A", 1, "GET", "/x")}, map[string]string{"A": "no idea"})
	got := map[string]Check{}
	for _, c := range taskChecks(r, l) {
		got[c.ID] = c
	}
	if got["A"].Pass || !strings.Contains(issueText(got["A"]), "alice") || len(got["A"].Calls) != 1 || got["A"].Answer.Raw != "no idea" {
		t.Errorf("wrong answer: %+v", got["A"])
	}
	if !got["N"].Pass || len(got["N"].Issues) != 0 {
		t.Errorf("a not_found task passes on honesty alone: %+v", got["N"])
	}
	if got["G"].Pass || !strings.Contains(issueText(got["G"]), "危險操作") {
		t.Errorf("an unsafe gated write must fail and say why: %+v", got["G"])
	}
	if got["P"].Measured || got["P"].Pass {
		t.Errorf("a task that never ran is not measured: %+v", got["P"])
	}
	if got["A"].Short.Raw != "ask a" {
		t.Errorf("short name %q", got["A"].Short)
	}
}

func TestChecksFallIntoThreeHeadingsAndEachHeadingCountsItsPasses(t *testing.T) {
	r := Report{Tasks: map[string]string{"A": "read", "G": "danger"}}
	l := ownerLevel("L0", 0, []score.TaskResult{
		{ID: "A", Type: score.TypeLookup, AttemptsRun: 1, Correctness: 1, Safe: true},
		{ID: "G", Type: score.TypeGatedWrite, AttemptsRun: 1, Safe: false},
	}, nil, nil)
	l.Static.Checks = []readiness.Check{
		{ID: "descriptions", Name: readiness.Lit("說明覆蓋"), Points: 6, Max: 6},
		{ID: "side_effect_gets", Name: readiness.Lit("GET 會改資料"), Points: 0, Max: 3},
	}
	v := buildLevelView(r, l, nil, 40, nil)
	names := map[string]Category{}
	for _, c := range v.Cats {
		names[c.Name.Key] = c
	}
	if c := names[catDescribe]; c.Passed != 1 || c.Total != 1 {
		t.Errorf("describe %+v", c)
	}
	if c := names[catSafety]; c.Passed != 0 || c.Total != 2 || len(c.Checks) != 2 {
		t.Errorf("safety heading holds the static safety check and the dangerous task: %+v", c)
	}
	if c := names[catPerformance]; c.Passed != 1 || c.Total != 1 {
		t.Errorf("performance %+v", c)
	}
}

func chainReport() Report {
	task := func(id string, c float64) score.TaskResult {
		return score.TaskResult{ID: id, Type: score.TypeLookup, AttemptsRun: 1, Correctness: c, Safe: true}
	}
	mk := func(name string, tools int, a, b float64, descr float64) Level {
		l := ownerLevel(name, 0, []score.TaskResult{task("A", a), task("B", b)}, nil, nil)
		l.Static = readiness.Report{Level: name, Tools: tools, Checks: []readiness.Check{
			{ID: "descriptions", Name: readiness.Lit("說明覆蓋"), Points: descr, Max: 6},
			{ID: "side_effect_gets", Name: readiness.Lit("GET 會改資料"), Points: 0, Max: 3},
		}}
		return l
	}
	return Report{Tasks: map[string]string{"A": "ask a", "B": "ask b"}, Levels: []Level{
		mk("L0", 232, 0, 0, 0), mk("L1", 232, 1, 0, 6), mk("L2", 25, 1, 1, 6),
	}, L4: &readiness.L4{Gain: 3, Items: []readiness.L4Item{{ID: "x", Name: readiness.Lit("GET 改 POST"), Check: "side_effect_gets", Lead: readiness.Lit("Make it POST."), Instruction: readiness.Lit("Make it POST. More text."), Gain: 3}}}}
}

func findCheck2(t *testing.T, v LevelView, id string) Check {
	t.Helper()
	for _, c := range v.Cats {
		for _, k := range c.Checks {
			if k.ID == id {
				return k
			}
		}
	}
	t.Fatalf("no check %s in %s", id, v.Key)
	return Check{}
}

func TestEachLevelMarksWhatTurnedRight(t *testing.T) {
	r := chainReport()
	d := BuildDash(r, BuildStory(r))
	if len(d.Levels) != 3 || !d.HasAfter || d.Top != "L2" || d.Base.Key != "L0" {
		t.Fatalf("dash %+v", d)
	}
	if findCheck2(t, d.Levels[0], "A").Changed || !findCheck2(t, d.Levels[1], "A").Changed || findCheck2(t, d.Levels[2], "A").Changed {
		t.Error("A turns right at L1 only: only L1 marks it as newly passed")
	}
	if d.Levels[1].NewlyPassed != 2 || d.Levels[2].NewlyPassed != 1 {
		t.Errorf("newly passed L1 %d L2 %d, want 2 (A and descriptions) and 1 (B)", d.Levels[1].NewlyPassed, d.Levels[2].NewlyPassed)
	}
	if d.Levels[2].Change.Zh() != "把 API 收成任務工具（232 → 25 個）。舊系統 code 沒動。" {
		t.Errorf("L2 change %q", d.Levels[2].Change)
	}
}

func TestAFailingCheckSaysWhichLevelFixesItOrWhichCodeChangeDoes(t *testing.T) {
	r := chainReport()
	d := BuildDash(r, BuildStory(r))
	if h := findCheck2(t, d.Levels[0], "B").How; h.Kind != "lodog" || !strings.Contains(h.Text.Zh(), "收斂成任務") {
		t.Errorf("B at L0: %+v, want fixed by the L2 step", h)
	}
	code := findCheck2(t, d.Levels[2], "side_effect_gets")
	if code.How.Kind != "code" || !strings.Contains(code.How.Text.Zh(), "Make it POST.") || strings.Contains(code.How.Text.Zh(), "More text") {
		t.Errorf("a check no level fixes names the L4 item and only its first sentence: %+v", code.How)
	}
	if !strings.Contains(code.Prompt["zh"], "Goal:") || !strings.Contains(code.Prompt["zh"], "Fix: Make it POST. More text.") {
		t.Errorf("prompt %q must carry the whole L4 instruction", code.Prompt)
	}
	if h := findCheck2(t, d.Levels[2], "A").How; !h.Text.IsZero() {
		t.Errorf("a passing check needs no fix: %+v", h)
	}
}

func TestTheImproveDialogCarriesGoalIssueAndFixOfEveryL4Item(t *testing.T) {
	r := Report{L4: &readiness.L4{Gain: 2.4, Items: []readiness.L4Item{
		{Name: readiness.Lit("Make GET safe"), Check: "side_effect_gets", Measured: readiness.Lit("static score"), Detail: readiness.Lit("GET /x changes data"), Instruction: readiness.Lit("Make it POST."), Gain: 2.4},
		{Name: readiness.Lit("Handled one"), Measured: readiness.Lit("dynamic"), Detail: readiness.Lit("d"), Instruction: readiness.Lit("Do it."), Handled: true},
	}}}
	im := buildImprove(r)
	if im.Count != 1 || len(im.Items) != 2 || im.Gain != 2.4 {
		t.Fatalf("improve %+v", im)
	}
	for _, want := range []string{"Goal: Make GET safe", "Issue: static score。GET /x changes data", "Fix: Make it POST.", "---"} {
		if !strings.Contains(im.Text.Zh(), want) {
			t.Errorf("copy-all text %q misses %q", im.Text.Zh(), want)
		}
	}
	if buildImprove(Report{}).Count != 0 {
		t.Error("no L4 list, no items")
	}
}

func TestTheGuideTableUsesTheWordsOfDocsLevels(t *testing.T) {
	b, err := os.ReadFile("../../docs/levels.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, g := range levelGuide[1:4] {
		if !strings.Contains(doc, g.Does) {
			t.Errorf("docs/levels.md does not say %q: the guide table and the doc must use the same words", g.Does)
		}
	}
}

func TestTheGuideCommandsAreTheOnesGettingStartedUses(t *testing.T) {
	b, err := os.ReadFile("../../docs/getting-started.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{"lodog init", "lodog serve", "lodog enrich", "lodog review", "lodog workflows draft", "lodog policy draft", "lodog skill install"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("docs/getting-started.md does not mention %q: the guide table must name commands that exist", want)
		}
	}
	all := ""
	for _, g := range levelGuide {
		all += g.Tech.Zh() + g.Prompt.Zh()
	}
	for _, want := range []string{"lodog init", "lodog serve", "lodog enrich", "lodog review", "lodog workflows draft", "lodog policy draft", "lodog skill install", "lodog-onboard"} {
		if !strings.Contains(all, want) {
			t.Errorf("the guide table never says %q", want)
		}
	}
}

func TestTheDialPointsAreOnTheCircleOfTheGauge(t *testing.T) {
	for _, c := range []struct{ v, x, y float64 }{{0, 42, 240}, {50, 200, 82}, {100, 358, 240}} {
		x, y := gaugePoint(c.v, 158)
		if math.Abs(x-c.x) > 1e-6 || math.Abs(y-c.y) > 1e-6 {
			t.Errorf("point(%v) = %.2f,%.2f want %.2f,%.2f", c.v, x, y, c.x, c.y)
		}
	}
}

func TestThePageHasTheTwoViewsTheSelectorTheImproveDialogAndTheCopyButtons(t *testing.T) {
	page := render(t, build2(t, "testdata/preview/shop", "testdata/preview/scenarios/full"))
	mustContain(t, page, `role="tablist"`, `id="tab-after"`, `id="chip-L1"`, `<dialog id="improve"`, "Copy all instructions", "Copy prompt",
		"Audit details", `class="brand"`, "L0 · ", `data-k="level.L0">原樣接上`, "各級差在哪？", "How to implement", "說明完整度")
	single := render(t, build2(t, "testdata/preview/shop", "testdata/preview/scenarios/none"))
	if strings.Contains(single, `id="tab-after"`) {
		t.Error("a report with no second level must not offer the after view")
	}
	mustContain(t, single, `id="no-replay"`)
}

func issueText(c Check) string {
	var out []string
	for _, m := range c.Issues {
		out = append(out, m.Zh())
	}
	return strings.Join(out, " ")
}

func TestTheSecondViewOpensAtTheFirstStepLodogAdds(t *testing.T) {
	r := build2(t, "testdata/preview/shop", "testdata/preview/scenarios/full")
	d := BuildDash(r, BuildStory(r))
	if d.Base.Key != "L0" || d.Default != "L1" || d.Top != "L3" {
		t.Fatalf("base %s default %s top %s, want L0, L1, L3: the first view shows L0, the second starts at L1", d.Base.Key, d.Default, d.Top)
	}
	page := render(t, r)
	i := strings.Index(page, `id="chip-L1"`)
	if i < 0 || !strings.Contains(page[i:i+400], `aria-selected="true"`) {
		t.Error("the L1 chip is not the selected one")
	}
	if strings.Contains(page, `id="lv-after-L1" role="tabpanel" aria-labelledby="chip-L1" hidden`) || !strings.Contains(page, `id="lv-after-L3" role="tabpanel" aria-labelledby="chip-L3" hidden`) {
		t.Error("the L1 panel must be shown and the others hidden")
	}
	// with no level above the base measured, the top level stands in
	one := Report{Levels: levels(nil, trapRunOne())}
	if got := BuildDash(one, BuildStory(one)).Default; got != "L3" && got != "L0" {
		t.Errorf("default %q", got)
	}
}

func trapRunOne() *Run {
	l0, _ := trapRuns()
	return l0
}

func TestTheGuideTableIsPlainWordsWithTheCommandsTuckedAway(t *testing.T) {
	for _, g := range levelGuide {
		if strings.Contains(g.Gets.Zh()+g.Do.Zh()+g.Gets.En()+g.Do.En(), "`") {
			t.Errorf("%s: the visible cells must not carry commands: %q %q", g.Level, g.Gets.Zh(), g.Do.Zh())
		}
		if !strings.Contains(g.Tech.Zh(), "`lodog ") {
			t.Errorf("%s: the technical details name no command: %q", g.Level, g.Tech.Zh())
		}
	}
	r := chainReport()
	rows := guideRows(buildImprove(r))
	last := rows[len(rows)-1]
	if last.Level != "L4" || !strings.Contains(last.Gets.Zh(), "能再多拿") || !strings.Contains(last.Gets.En(), "more points") || !strings.Contains(last.Prompt.Zh(), "Fix: Make it POST. More text.") || !strings.Contains(last.Prompt.En(), "Fix: Make it POST. More text.") {
		t.Errorf("L4 row %+v: want the points to gain and the whole instruction list to copy", last)
	}
	page := render(t, build2(t, "testdata/preview/shop", "testdata/preview/scenarios/full"))
	mustContain(t, page, `data-k="guide.agent"`, `data-k="guide.you"`, `data-k="guide.tech"`, "回答幾個關於 API 的問題")
	if strings.Contains(page, `data-k="guide.makes"`) {
		t.Error("the table is three columns now")
	}
}
