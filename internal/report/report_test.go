package report

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// The fixture case (testdata/case) has all four gateway configs (a case does not skip a level). Under
// testdata/recorded, two L0 runs: the later one made an unapproved bonus call,
// the earlier one did not, and neither stored the not_found task's verdict.

func build(t *testing.T, runsDir string) Report {
	t.Helper()
	r, err := Build(context.Background(), Options{CaseDir: "testdata/case", RunsDir: runsDir, Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func render(t *testing.T, r Report) string {
	t.Helper()
	var b bytes.Buffer
	if err := RenderHTML(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func mustContain(t *testing.T, page string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(page, w) {
			t.Errorf("page is missing %q", w)
		}
	}
}

func TestEveryGatewayConfigIsALevelInLevelOrder(t *testing.T) {
	r := build(t, "testdata/no-runs")
	var names []string
	for _, l := range r.Levels {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "L0,L1,L2,L3" {
		t.Fatalf("levels %v, want L0,L1,L2,L3", names)
	}
	if r.Levels[3].Static.Points <= r.Levels[0].Static.Points {
		t.Fatalf("L3 static %.2f not above L0 %.2f: policy and identity did not count", r.Levels[3].Static.Points, r.Levels[0].Static.Points)
	}
}

func TestWithNoRunsThePageSaysItHasOnlyStaticScores(t *testing.T) {
	r := build(t, "testdata/no-runs")
	if r.HasRuns() {
		t.Fatal("a missing runs directory produced a run")
	}
	page := render(t, r)
	mustContain(t, page, `id="static-only"`, `id="by-type-missing"`, "未量測：沒有 run", "8.00", "23.00",
		"L3：testdata/no-runs 下沒有用 testdata/case/gateway/L3.yaml 跑的 run")
	if strings.Contains(page, `class="bad">觸發`) || strings.Contains(page, `class="good">未觸發`) {
		t.Error("a page without runs reports a gate outcome")
	}
}

func TestMissingLevelsAreStated(t *testing.T) {
	r, err := Build(context.Background(), Options{CaseDir: "testdata/case-l0", RunsDir: "testdata/no-runs", Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, render(t, r), "L1：testdata/case-l0/gateway 下沒有這一級的 gateway 設定", "L2：testdata/case-l0/gateway 下沒有這一級的 gateway 設定")
}

func TestTheLatestRunOfALevelIsScoredWithItsStaticScore(t *testing.T) {
	r := build(t, "testdata/recorded")
	l0 := r.Levels[0]
	if l0.Run == nil || l0.Run.ID != "20261015-1000-L0" {
		t.Fatalf("L0 run %+v, want the later run 20261015-1000-L0", l0.Run)
	}
	if r.Levels[3].Run != nil {
		t.Fatal("L3 got a run although none was made with its config")
	}
	p := l0.Run.Score.Points
	if p.Static != l0.Static.Points || p.Max != TotalMax {
		t.Fatalf("static %.2f of max %.0f, want the level's %.2f of %d", p.Static, p.Max, l0.Static.Points, TotalMax)
	}
	if !l0.Run.Score.Gate.Triggered || p.Total != l0.Run.Score.Gate.Cap || p.BeforeGate <= p.Total {
		t.Fatalf("gate %+v points %+v: the unapproved bonus call must cap the total", l0.Run.Score.Gate, p)
	}
	if l0.Run.Unjudged != 1 {
		t.Fatalf("unjudged %d, want the not_found task's one question", l0.Run.Unjudged)
	}
}

func TestThePageShowsTheGateByTypeResultsAndWhatIsUnjudged(t *testing.T) {
	page := render(t, build(t, "testdata/recorded"))
	if strings.Contains(page, `id="static-only"`) {
		t.Error("a page with a run still claims it has only static scores")
	}
	mustContain(t, page,
		`data-k="gate.triggered">觸發`, "T2 第 1 次：POST /players/1/bonus", "總分上限 40.00",
		"gated_write", "安全 0/1", "lookup", "答對 100%",
		"1 個 judge 問題沒有存下的答案")
}

func TestAnIncompleteRunsTotalIsMarkedOnThePage(t *testing.T) {
	r := build2(t, "testdata/preview/shop", "testdata/preview/scenarios/full")
	page := withoutData(render(t, r))
	if strings.Contains(page, "不完整") {
		t.Fatal("a complete run is marked incomplete")
	}
	for i := range r.Levels {
		if r.Levels[i].Run != nil {
			r.Levels[i].Run.Score.Complete = false
		}
	}
	mustContain(t, render(t, r), "不完整：有部分沒評分", `<span class="bad"><span class="i" data-k="pt.incomplete">不完整</span></span>`)
}

func TestAJudgeQuestionWithNoStoredVerdictMakesTheRunIncompleteAndNamesTheTask(t *testing.T) {
	r := build(t, "testdata/recorded")
	run := r.Levels[0].Run
	if run.Score.Complete || run.Unjudged != 1 || strings.Join(run.UnjudgedTasks, ",") != "T3" {
		t.Fatalf("run complete=%v unjudged=%d tasks=%v, want incomplete with T3 unjudged", run.Score.Complete, run.Unjudged, run.UnjudgedTasks)
	}
	mustContain(t, render(t, r), "1 個 judge 問題沒有存下的答案（T3）", `data-k="pt.incomplete">不完整`)
}

func TestTheL4ListComesFromTheBestStaticLevel(t *testing.T) {
	r := build(t, "testdata/no-runs")
	if r.L4 == nil || r.L4Level != "L3" {
		t.Fatalf("L4 from %q, want L3, the level with the highest static score", r.L4Level)
	}
	page := render(t, r)
	mustContain(t, page, `id="l4"`, "依 L3", "貼給 coding agent", "GET /players/{id}/resetPassword", "公開 agent 標準未檢查")
}

func TestL4ItemsThatAddNoStaticPointsAreListedApartAsTheCLIDoes(t *testing.T) {
	r := build(t, "testdata/no-runs")
	var counted, handled *readiness.L4Item
	for i, it := range r.L4.Items {
		switch {
		case it.Counted() && counted == nil:
			counted = &r.L4.Items[i]
		case it.Handled && handled == nil:
			handled = &r.L4.Items[i]
		}
	}
	if counted == nil || handled == nil {
		t.Fatalf("fixture L4 %+v needs an item that adds points and one already full", r.L4.Items)
	}
	page := render(t, r)
	if strings.Contains(page, "+0.00 ") {
		t.Error("an item at full points is listed as +0.00 分")
	}
	group := strings.Index(page, `id="l4-uncounted"><span class="i" data-k="l4.uncounted">靜態分不計、原系統仍建議改`)
	if group < 0 {
		t.Fatal("page has no group for items outside the static score")
	}
	summary := func(it *readiness.L4Item) string {
		return string(spanOf(M("l4.item", TextMsg(it.Name), TextMsg(it.Measured))))
	}
	gain := strings.Index(page, fmt.Sprintf(`+%.2f <span class="i" data-k="l4.pts">分</span>　%s`, counted.Gain, summary(counted)))
	if gain < 0 || gain > group {
		t.Errorf("%s is not listed with its gain before the uncounted group", counted.Name)
	}
	if strings.LastIndex(page, "·　"+summary(handled)) < group {
		t.Errorf("%s is not listed in the uncounted group", handled.Name)
	}
}

func TestTheRunsSystemVersionsAreShownAndAMismatchIsStated(t *testing.T) {
	page := render(t, build(t, "testdata/recorded"))
	mustContain(t, page, "source.commit: abc123", "db@sha256:2（釘住 db@sha256:1）",
		"L0（run 20261015-1000-L0）：database.image 是 db@sha256:2，不是釘住的 db@sha256:1")
}

func TestRunsOfDifferentVersionsOrNoneAreFlagged(t *testing.T) {
	run := func(id, commit string) *Run {
		r := &Run{ID: id}
		if commit != "" {
			r.Score.Versions = []score.ObservedVersion{{Name: "source.commit", Expected: commit, Observed: commit, Match: true}}
		}
		return r
	}
	gaps := zhJoin(versionGaps([]Level{
		{Name: "L0", Run: run("a", "c1")}, {Name: "L1", Run: run("b", "c2")}, {Name: "L3", Run: run("c", "")},
	}))
	for _, want := range []string{"各級的 run 看到的 source.commit 不同", "L3（run c）：沒有記下系統版本"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gaps %q miss %q", gaps, want)
		}
	}
}

func TestTheL4ListCarriesWhatEveryLevelsRunShows(t *testing.T) {
	doc, err := spec.Parse([]byte(`{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{
	 "/tx/reject":{"post":{"responses":{"200":{"description":"ok"}}}},
	 "/txs":{"get":{"parameters":[{"name":"page","in":"query"},{"name":"pageSize","in":"query"}],"responses":{"200":{"description":"ok"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	v := readiness.View{Doc: doc}
	low := Level{Name: "L0", Static: readiness.Score(v), view: v, Run: &Run{ID: "r0", Calls: []record.Call{
		{TaskID: "T1", Attempt: 1, Seq: 1, Method: "POST", Path: "/tx/reject", Status: 200},
		{TaskID: "T1", Attempt: 1, Seq: 2, Method: "GET", Path: "/txs", Status: 200},
	}}}
	best := Level{Name: "L3", Static: readiness.Score(v), view: v}
	best.Static.Points = low.Static.Points + 1
	r := Report{Levels: []Level{low, best}}
	if err := r.pickL4(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	page := render(t, r)
	mustContain(t, page, "依 L3", "GET /txs/{id}", "r0 T1-1：POST /tx/reject 之後翻列表找回剛寫的那筆")
}

func TestThePageShowsHowStableEachLevelAndTaskIs(t *testing.T) {
	r := build(t, "testdata/recorded")
	page := render(t, r)
	mustContain(t, page, `id="stability"`, "題每次結果一致", "答對 ", "每題 1 次")
	run := r.Levels[0].Run
	run.Score.MinRunsPerTask, run.Score.RunsPerTask = 3, 5
	run.Score.Stability = score.Stability{TasksRun: 2, ConsistentTasks: 1, Consistency: 0.83, CorrectnessLow: 20, CorrectnessHigh: 40}
	run.Score.Tasks[0].AttemptsRun, run.Score.Tasks[0].AgreeingAttempts = 5, 3
	mustContain(t, render(t, r), "每題先跑 3 次，不一致補到 5 次", "1/2 題每次結果一致（平均 83%）", "答對 20.00–40.00", `<span class="bad">3/5</span>`)
}

func TestARunWithNoRunBelowItMarksTheChainIncomplete(t *testing.T) {
	r := build(t, "testdata/recorded")
	if gaps := r.ChainGaps(); len(gaps) != 0 {
		t.Fatalf("L0 is the only run, nothing is below it, yet gaps %v", gaps)
	}
	if strings.Contains(render(t, r), `id="chain-gap"`) {
		t.Error("a chain with no gap is marked incomplete")
	}
	r.Levels[3].Run = &Run{ID: "x"}
	if got := strings.Join(r.ChainGaps(), ","); got != "L1,L2" {
		t.Fatalf("L0 and L3 have runs: gaps %q, want L1,L2", got)
	}
	mustContain(t, render(t, r), `id="chain-gap"`, "分數鏈不完整", "L1、L2 沒有 run")
}

// withoutData is the page without its embedded JSON (the dictionaries and the
// levels), which holds every sentence of both languages.
func withoutData(page string) string {
	for {
		i := strings.Index(page, `<script type="application/json"`)
		if i < 0 {
			return page
		}
		j := strings.Index(page[i:], "</script>")
		page = page[:i] + page[i+j+len("</script>"):]
	}
}
