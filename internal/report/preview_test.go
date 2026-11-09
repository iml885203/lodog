package report

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

// The fixtures under testdata/preview are a synthetic case ("shop") with runs
// whose scores come out of the real scorer; `make report-preview` renders them.

func build2(t *testing.T, caseDir, runsDir string) Report {
	t.Helper()
	r, err := Build(context.Background(), Options{CaseDir: caseDir, RunsDir: runsDir, Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func preview(t *testing.T, scenario, caseName string) Report {
	t.Helper()
	return build2(t, "testdata/preview/"+caseName, "testdata/preview/scenarios/"+scenario)
}

func kinds(c Climb) string {
	var out []string
	for _, s := range c.Stages {
		k := s.Name + ":" + s.Kind
		if s.Capped {
			k += ":capped"
		}
		out = append(out, k)
	}
	return strings.Join(out, " ")
}

func TestThePreviewChainClimbsAndBreaksThrough(t *testing.T) {
	r := preview(t, "full", "shop")
	st := BuildStory(r)
	if want := "L0:run:capped L1:run:capped L2:run L3:run"; kinds(st.Climb) != want {
		t.Fatalf("stages %q, want %q", kinds(st.Climb), want)
	}
	for _, s := range st.Climb.Stages[:2] {
		if s.Before <= st.Climb.Cap || s.Total != st.Climb.Cap {
			t.Errorf("%s: before %.1f total %.1f: want a score above the gate pressed to it", s.Name, s.Before, s.Total)
		}
	}
	if st.Climb.Stages[3].Total <= st.Climb.Stages[2].Total || st.Climb.Stages[2].Total <= st.Climb.Cap {
		t.Errorf("L2 %.1f and L3 %.1f must climb above the gate", st.Climb.Stages[2].Total, st.Climb.Stages[3].Total)
	}
	if len(st.Replays) != 3 {
		t.Fatalf("replays %d, want the three traps", len(st.Replays))
	}
	for _, rp := range st.Replays {
		if rp.Open.Level != "L0" && rp.Open.Level != "L1" || rp.Open.Danger == 0 || rp.Guarded.Danger != 0 {
			t.Errorf("%s: open %s danger %d, guarded %s danger %d", rp.TaskID, rp.Open.Level, rp.Open.Danger, rp.Guarded.Level, rp.Guarded.Danger)
		}
	}
	page := render(t, r)
	mustContain(t, page, "經核准 apr_0007 才送出", "POST", "/players/1/bonus")
}

func TestThePreviewWhereEveryLevelIsPressed(t *testing.T) {
	r := preview(t, "all-capped", "shop")
	st := BuildStory(r)
	for _, s := range st.Climb.Stages {
		if !s.Capped {
			t.Fatalf("%s is not pressed by the cap", s.Name)
		}
	}
	if len(st.Replays) != 0 || st.NoReplay.Key != "noreplay.both" {
		t.Fatalf("replays %d, reason %+v: nothing guarded, so nothing to replay", len(st.Replays), st.NoReplay)
	}
	mustContain(t, render(t, r), `id="no-replay"`)
}

func TestThePreviewWithAnIncompleteRun(t *testing.T) {
	r := preview(t, "incomplete", "shop")
	l2 := BuildStory(r).Climb.Stages[2]
	if l2.Kind != KindRun || l2.Complete {
		t.Fatalf("L2 %+v: want a run marked incomplete", l2)
	}
	mustContain(t, render(t, r), "不完整：有部分沒評分")
}

// All four configs exist (a case cannot skip a level); only L0 and L3 were run.
func TestThePreviewWithRunsOnlyAtL0AndL3(t *testing.T) {
	r := preview(t, "l0-l3", "shop-l0-l3")
	st := BuildStory(r)
	if want := "L0:run:capped L1:static L2:static L3:run"; kinds(st.Climb) != want {
		t.Fatalf("stages %q, want %q", kinds(st.Climb), want)
	}
	if len(st.Replays) != 3 {
		t.Errorf("replays %d, want 3", len(st.Replays))
	}
	mustContain(t, render(t, r), `id="chain-gap"`, "分數鏈不完整", "L1、L2 沒有 run")
}

func TestThePreviewWithNoRunShowsStaticScoresOnly(t *testing.T) {
	r := preview(t, "none", "shop")
	st := BuildStory(r)
	if kinds(st.Climb) != "L0:static L1:static L2:static L3:static" {
		t.Fatalf("climb %q", kinds(st.Climb))
	}
	mustContain(t, render(t, r), `id="static-only"`, "只有靜態分")
}

func TestEveryPreviewLoadsNothingFromOutside(t *testing.T) {
	for _, c := range [][2]string{{"full", "shop"}, {"all-capped", "shop"}, {"incomplete", "shop"}, {"l0-l3", "shop-l0-l3"}, {"none", "shop"}} {
		if m := external.FindString(render(t, preview(t, c[0], c[1]))); m != "" {
			t.Errorf("%s: page references %q", c[0], m)
		}
	}
}

func TestABarAndItsNumberAreAtTheSameHeightAtAnyTime(t *testing.T) {
	pressed := Stage{Kind: KindRun, Capped: true, Before: 60, Total: 40, Start: 1}.Timing(40)
	// The number counts linearly to Before over Span; the bar reaches Total at Fill.
	if got := pressed.Span * 40 / 60; math.Abs(got-pressed.Fill) > 1e-9 || !pressed.Linear || pressed.OverStart != pressed.Fill {
		t.Errorf("pressed timing %+v: bar must reach the gate when the number does, and the dashed part continue from there", pressed)
	}
	if math.Abs(pressed.OverStart+pressed.OverDur-pressed.Span) > 1e-9 {
		t.Errorf("pressed timing %+v: the dashed part must end with the count", pressed)
	}
	broke := Stage{Kind: KindRun, Total: 80, Start: 1}.Timing(40)
	if broke.Linear || broke.Fill != broke.Span {
		t.Errorf("broke timing %+v: bar and number share one eased span", broke)
	}
	if h := ease(broke.Cross/broke.Span) * 80; math.Abs(h-40) > 0.01 {
		t.Errorf("the burst is at height %.2f, want the gate's 40", h)
	}
}

func TestTheClimbPlaysOneLevelAfterAnotherAndSkipsThoseWithoutARun(t *testing.T) {
	c := BuildStory(preview(t, "l0-l3", "shop-l0-l3")).Climb
	l0, l1, l3 := c.Stages[0], c.Stages[1], c.Stages[3]
	if l1.Timing(c.Cap).Start >= l0.Timing(c.Cap).Start+l0.Timing(c.Cap).Span {
		t.Errorf("L1 has no run, it must not wait for the climb: %+v", l1.Timing(c.Cap))
	}
	if l3.Start < l0.Start+l0.Timing(c.Cap).Span {
		t.Errorf("L3 starts at %.2f, before L0 ends at %.2f", l3.Start, l0.Start+l0.Timing(c.Cap).Span)
	}
}
