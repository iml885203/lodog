package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/score"
)

// copyRun copies a preview run into a temp runs dir and writes its score.json.
func copyRun(t *testing.T, run string, stored *score.Report) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata/preview/scenarios/full", run)
	if err := os.CopyFS(filepath.Join(dst, run), os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		b, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, run, "score.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestARunWhoseStoredScoreDiffersFromTheRescoreSaysWhichTasks(t *testing.T) {
	now := preview(t, "full", "shop").Levels[0].Run.Score
	old := now
	old.Points.BeforeGate, old.Points.Total = 80, 40
	old.Tasks = append([]score.TaskResult{}, now.Tasks...)
	old.Tasks[0].Correctness = 1 - now.Tasks[0].Correctness
	r := build2(t, "testdata/preview/shop", copyRun(t, "2026-10-16-L0", &old))
	d := r.Levels[0].Run.Drift
	if d == nil || d.StoredBeforeGate != 80 || len(d.Tasks) != 1 || !strings.HasPrefix(d.Tasks[0], old.Tasks[0].ID) {
		t.Fatalf("drift %+v, want the stored 80 and only task %s", d, old.Tasks[0].ID)
	}
	mustContain(t, zhJoin(r.NotMeasured), "score.json 記的是門檻前 80.00", old.Tasks[0].ID+" (correct")
}

func TestARunWhoseStoredScoreMatchesTheRescoreHasNoDrift(t *testing.T) {
	now := preview(t, "full", "shop").Levels[0].Run.Score
	r := build2(t, "testdata/preview/shop", copyRun(t, "2026-10-16-L0", &now))
	if d := r.Levels[0].Run.Drift; d != nil {
		t.Fatalf("drift %+v for a stored score equal to the rescore", d)
	}
	if r := build2(t, "testdata/preview/shop", copyRun(t, "2026-10-16-L0", nil)); r.Levels[0].Run.Drift != nil {
		t.Fatal("a run with no score.json drifted")
	}
}

// zhJoin is a list of sentences as Chinese text, one per line.
func zhJoin(ms []Msg) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Zh())
	}
	return strings.Join(out, "\n")
}

func TestARunMadeWithARenamedConfigIsMatchedByItsLevelAndMarked(t *testing.T) {
	dir := copyRun(t, "2026-10-16-L3", nil)
	rewriteMeta(t, dir, "2026-10-16-L3", "gateway/L3.yaml", "gateway/L3-auto.yaml")
	r := build2(t, "testdata/preview/shop", dir)
	l3 := r.Levels[len(r.Levels)-1]
	if l3.Run == nil || l3.Run.LegacyConfig != "testdata/preview/shop/gateway/L3-auto.yaml" {
		t.Fatalf("L3 run %+v: want it matched by level with the old config name kept", l3.Run)
	}
	mustContain(t, zhJoin(r.NotMeasured), "run 記的設定檔是 testdata/preview/shop/gateway/L3-auto.yaml")
	mustContain(t, render(t, r), `data-k="climb.legacy">舊設定檔名`)
	if !BuildStory(r).Climb.Stages[3].Legacy {
		t.Error("the stage does not say its run has an old config name")
	}
}

func TestARunWhoseConfigStillExistsIsNeverMatchedToAnotherLevel(t *testing.T) {
	// an L3 run that names L2's config belongs to L2's config, not to L3 by level
	dir := copyRun(t, "2026-10-16-L3", nil)
	rewriteMeta(t, dir, "2026-10-16-L3", "gateway/L3.yaml", "gateway/L2.yaml")
	r := build2(t, "testdata/preview/shop", dir)
	for _, l := range r.Levels {
		if l.Name == "L3" && l.Run != nil {
			t.Errorf("L3 got the run of another level's config: %+v", l.Run.ID)
		}
	}
}

// rewriteMeta replaces a text in the meta.yaml of a run copied to a temp dir.
func rewriteMeta(t *testing.T, dir, run, from, to string) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Join(run, "meta.yaml")
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		t.Fatalf("the meta does not contain %q", from)
	}
	if err := root.WriteFile(name, []byte(strings.ReplaceAll(string(b), from, to)), 0o600); err != nil {
		t.Fatal(err)
	}
}
