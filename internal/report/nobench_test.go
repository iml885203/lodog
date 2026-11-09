package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/initcase"
)

// A case made by `lodog init` without --with-bench has no task bank. The
// report is still the static scores of its four levels, as `report --help`
// promises.
func initWithoutBench(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "petshop")
	err := initcase.Generate(context.Background(), initcase.Options{
		Dir:      dir,
		Swagger:  "../initcase/testdata/petshop.json",
		Upstream: "https://petshop.test/api/v3",
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// What enrich, workflows draft and policy draft write, by hand: they ask a
	// model, and the report only reads the files.
	spec := map[string]string{
		"overlay.yaml": `overlay: 1.0.0
info: {title: petshop overlay, version: "1"}
actions:
  - target: $.paths['/pet'].post
    update:
      summary: Add a pet to the store.
`,
		"workflows.arazzo.yaml": `arazzo: 1.0.1
info: {title: petshop task tools, version: "1", description: Start with get_pet.}
sourceDescriptions:
  - {name: shop, url: openapi.json, type: openapi}
workflows:
  - workflowId: get_pet
    summary: Read one pet.
    inputs:
      type: object
      properties:
        petId: {type: integer, description: The pet's id.}
      required: [petId]
    steps:
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1pet~1{petId}/get'
        parameters:
          - {name: petId, in: path, value: $inputs.petId}
    x-agent: {risk: read}
`,
		"policy.yaml": `risks:
  read: allow
  write: approval
default: write
rules:
  - match: "GET /pet/*"
    risk: read
  - match: "GET /store/inventory"
    risk: read
  - match: "POST /pet"
    risk: write
`,
	}
	for name, body := range spec {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestACaseWithoutABenchGetsAStaticOnlyReportPage(t *testing.T) {
	dir := initWithoutBench(t)
	rep, err := Build(context.Background(), Options{CaseDir: dir, RunsDir: filepath.Join(dir, "no-runs"), Now: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rep.Levels) != 4 || rep.HasRuns() {
		t.Fatalf("levels %d, has runs %v: want the four static levels and no run", len(rep.Levels), rep.HasRuns())
	}
	page := render(t, rep)
	mustContain(t, page, `id="static-only"`)
	if strings.Contains(page, "NaN") || strings.Contains(page, "<no value>") {
		t.Error("the page carries a broken value")
	}
}

func TestAPageWithoutABenchSaysSoInsteadOfShowingAnEmptyBenchmark(t *testing.T) {
	dir := initWithoutBench(t)
	rep, err := Build(context.Background(), Options{CaseDir: dir, RunsDir: filepath.Join(dir, "no-runs"), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasBench {
		t.Fatal("a case with no answers reports a bench")
	}
	page := render(t, rep)
	mustContain(t, page, `id="no-bench"`)
	with := build(t, "testdata/no-runs")
	if !with.HasBench || strings.Contains(render(t, with), `id="no-bench"`) {
		t.Error("a case with a bench must not carry the no-bench note")
	}
}

func TestACaseWithAnAnswersFileThatIsMissingStillFails(t *testing.T) {
	dir := initWithoutBench(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile("case.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("case.yaml", append(b, []byte("\nbenchmark:\n  answers: expected/answers.yaml\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), Options{CaseDir: dir, RunsDir: filepath.Join(dir, "no-runs"), Now: time.Now()}); err == nil {
		t.Fatal("a case that names an answers file it does not have was reported as if it had no bench")
	}
}

func levelsData(t *testing.T, page string) map[string]struct {
	Measured bool    `json:"measured"`
	Static   float64 `json:"static"`
	Scale    float64 `json:"scale"`
} {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script type="application/json" id="levels-data">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the page has no levels-data")
	}
	var out map[string]struct {
		Measured bool    `json:"measured"`
		Static   float64 `json:"static"`
		Scale    float64 `json:"scale"`
	}
	if err := json.Unmarshal([]byte(m[1]), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func noBenchReport(t *testing.T) Report {
	t.Helper()
	dir := initWithoutBench(t)
	rep, err := Build(context.Background(), Options{CaseDir: dir, RunsDir: filepath.Join(dir, "no-runs"), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestAPageWithoutABenchHasTheWithLodogViewWithEveryLevelsStaticScore(t *testing.T) {
	rep := noBenchReport(t)
	page := render(t, rep)
	mustContain(t, page, `id="tab-after"`, `id="view-after"`)
	data := levelsData(t, page)
	for _, l := range rep.Levels {
		k := levelKey(l)
		mustContain(t, page, `id="chip-`+k+`"`, `id="lv-after-`+k+`"`, `id="c-after-`+k+`-`)
		got, ok := data[k]
		if !ok || got.Measured || got.Static != l.Static.Points || got.Static <= 0 {
			t.Errorf("%s: levels-data %+v, want its static score %.2f and not measured", k, got, l.Static.Points)
		}
		if got.Scale != 30 {
			t.Errorf("%s: scale %v, want 30", k, got.Scale)
		}
	}
	if len(data) != 4 {
		t.Errorf("levels-data has %d levels, want 4", len(data))
	}
}

func TestTheDialOfAPageWithoutABenchIsOutOf30AndSaysStaticOnly(t *testing.T) {
	page := render(t, noBenchReport(t))
	for _, id := range []string{"dial-now", "dial-after"} {
		mustContain(t, page, `id="`+id+`" `)
		re := regexp.MustCompile(`id="` + id + `"[^>]*data-scale="30"`)
		if !re.MatchString(page) {
			t.Errorf("%s is not drawn on the 30-point scale", id)
		}
	}
	// the end of the dial reads 30, never 100, and the number is labelled static
	mustContain(t, page, `<span class="endmax">30</span>`, `data-k="dial.of.static"`)
	if strings.Contains(page, `<span class="endmax">100</span>`) {
		t.Error("a static-only dial ends at 100")
	}
}

func TestTheNoBenchNoteIsOneQuietLineThatSaysHowToAddABank(t *testing.T) {
	page := render(t, noBenchReport(t))
	mustContain(t, page, `id="no-bench"`, "lodog init --with-bench", "docs/init.md")
	if strings.Contains(page, `class="banner" id="static-only"`) {
		t.Error("the no-bench note is a loud banner again")
	}
	// the list of what is not measured points to the task bank, not to a run the case cannot make
	i := strings.Index(page, `id="b-not-measured"`)
	if i < 0 {
		t.Fatal("no not-measured list")
	}
	list := page[i:]
	list, _, _ = strings.Cut(list, "</ul>")
	mustContain(t, list, `data-k="nm.nobench"`)
	if strings.Contains(list, "nm.norun") {
		t.Error("the not-measured list still asks for a run of a case that has no task bank")
	}
}

func TestAPageWithABenchStaysOnTheHundredScale(t *testing.T) {
	page := render(t, build(t, "testdata/recorded"))
	mustContain(t, page, `<span class="endmax">100</span>`)
	if strings.Contains(page, `data-scale="30"`) {
		t.Error("a measured dial is drawn on the 30-point scale")
	}
}

// A case with no task bank cannot be benchmarked, so none of its no-bench
// wording may tell the owner to run lodog bench.
var runBench = regexp.MustCompile(`(?i)(run|跑)\s*lodog bench`)

func TestNoBenchWordingNeverTellsTheOwnerToRunABenchInEitherLanguage(t *testing.T) {
	for lang, dict := range map[string]map[string]string{"zh": zh, "en": en} {
		for _, k := range []string{"banner.nobench.h", "banner.nobench.b", "nm.nobench"} {
			text, ok := dict[k]
			if !ok || text == "" {
				t.Errorf("%s: %s is missing", lang, k)
			}
			if runBench.MatchString(text) {
				t.Errorf("%s %s tells the owner to run lodog bench: %q", lang, k, text)
			}
		}
		if !strings.Contains(dict["chip.static"], "/ 30") {
			t.Errorf("%s chip.static %q does not say / 30", lang, dict["chip.static"])
		}
	}
}
