package review

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// everyKind asks one question of each kind the page words by itself: policy,
// policy the model was unsure of, overlay (read and not), overlay with no risk,
// workflows. The checklist's files are real so the page can read the policy.
func everyKind(t *testing.T) *Checklist {
	t.Helper()
	c, err := Ask(Files{
		Policy:    writeFile(t, "policy.auto.yaml", draft),
		Overlay:   writeFile(t, "overlay.yaml", overlayDoc),
		Workflows: writeFile(t, "workflows.arazzo.yaml", workflowsDoc),
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Questions = append(c.Questions, Question{
		ID: "overlay-unset", Source: SourceOverlay, Proposed: unsetRisk, Choices: []string{"read", "write"},
		Ask: "The model described these 1 operations but gave no risk.", Items: []Item{{Key: "GET /api/x"}},
	})
	return c
}

func render(t *testing.T, c *Checklist, o PageOptions) string {
	t.Helper()
	var b bytes.Buffer
	if err := RenderHTML(&b, c, o); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestThePageSaysHowToAnswerWithTheRealFileAndBothWays(t *testing.T) {
	c := everyKind(t)
	yamlPath := filepath.Join(t.TempDir(), "my review.yaml")
	html := render(t, c, PageOptions{Checklist: yamlPath})
	for _, want := range []string{
		`id="how"`,
		"lodog-onboard",             // way 1
		"lodog review apply",        // way 2
		yamlPath,                    // the file to edit, by its real path
		"answer: {confirmed: true}", // the copyable example
		"id: " + c.Questions[0].ID,  // answers the page's own first question
		`lodog review apply &#39;` + yamlPath + `&#39;`, // the command names the same file, quoted for a path with a space
		`--by &#34;&lt;your name&gt;&#34;`,              // the person is told to say who they are
		`data-copy="example"`, `data-copy="command"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestEveryQuestionIsExplainedInPlainWordsAndItsItemsAreFolded(t *testing.T) {
	c := everyKind(t)
	html := render(t, c, PageOptions{})
	if len(c.Questions) < 5 {
		t.Fatalf("the fixture asks only %d questions", len(c.Questions))
	}
	for _, q := range c.Questions {
		_, sec, _ := strings.Cut(html, `<section id="`+q.ID+`"`)
		sec, _, _ = strings.Cut(sec, "</section>")
		for _, label := range []string{"q.decide", "q.suggest", "q.yes", "q.no"} {
			if !strings.Contains(sec, `data-k="`+label+`"`) {
				t.Errorf("%s lacks the %s line", q.ID, label)
			}
		}
		if strings.Contains(sec, "<details open") {
			t.Errorf("%s shows its items unfolded", q.ID)
		}
		// the sentence that carries the decision comes from the dictionary, not from the checklist's English ask
		if strings.Contains(sec, q.Ask) && !strings.Contains(sec, "q.orig") {
			t.Errorf("%s shows the English ask as the explanation", q.ID)
		}
	}
	if strings.Count(html, "<details") != len(c.Questions) {
		t.Errorf("%d folds for %d questions", strings.Count(html, "<details"), len(c.Questions))
	}
}

func TestBothLanguagesHaveTheSameKeysAndEveryKeyThePageUsesHasText(t *testing.T) {
	for k := range zh {
		if en[k] == "" {
			t.Errorf("key %q has no English", k)
		}
	}
	for k := range en {
		if zh[k] == "" {
			t.Errorf("key %q has no Chinese", k)
		}
	}
	html := render(t, everyKind(t), PageOptions{Log: []Entry{{By: "Ada", Question: "q"}}})
	used := regexp.MustCompile(`data-k="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(used) < 20 {
		t.Fatalf("only %d keys used", len(used))
	}
	for _, m := range used {
		for lang, d := range dicts {
			if d[m[1]] == "" {
				t.Errorf("page uses %q, which has no %s text", m[1], lang)
			}
		}
	}
	// a placeholder is filled in both languages, whatever the question
	for lang := range dicts {
		for _, m := range regexp.MustCompile(`<span class="i" data-k="([^"]+)"(?: data-a="([^"]*)")?`).FindAllStringSubmatch(html, -1) {
			args := []string(nil)
			if m[2] != "" {
				args = parseArgs(t, m[2])
			}
			if got := text(lang, m[1], args); regexp.MustCompile(`\{\d\}`).MatchString(got) {
				t.Errorf("%s text of %s has an unfilled placeholder: %s", lang, m[1], got)
			}
		}
	}
}

func parseArgs(t *testing.T, escaped string) []string {
	t.Helper()
	raw := strings.NewReplacer("&#34;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'").Replace(escaped)
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	var out []string
	for _, p := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(raw, -1) {
		out = append(out, p[1])
	}
	return out
}

func TestThePageCarriesBothDictionariesAndSwitchesInTheBrowser(t *testing.T) {
	html := render(t, everyKind(t), PageOptions{})
	for _, want := range []string{`id="i18n"`, `data-lang="zh"`, `data-lang="en"`, "navigator.language", "lodog-report-lang", "How to answer", "怎麼作答"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestTheEnglishChecklistSentenceIsKeptForTheItemsAndNotTheExplanation(t *testing.T) {
	c := everyKind(t)
	html := render(t, c, PageOptions{})
	if !strings.Contains(html, "DELETE /api/items/{id}") || !strings.Contains(html, c.Questions[0].Ask) {
		t.Error("an item and the original ask are kept as written")
	}
}

func TestThePageSaysWhoAppliedHowManyAnswersWhen(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, ReviewLog)
	if err := AppendLog(log, []Entry{{At: now, By: "Ada", Question: "a"}, {At: now, By: "Ada", Question: "b"}, {At: now, By: "Bo", Question: "c"}}); err != nil {
		t.Fatal(err)
	}
	policy := writeFile(t, "policy.auto.yaml", draft)
	page := filepath.Join(dir, "review.html")
	if err := AskToFiles(AskOptions{Files: Files{Policy: policy}, Out: filepath.Join(dir, "review.yaml"), HTML: page}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(page) //nolint:gosec // G304: a path the test just made
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{`id="applied"`, `data-k="applied"`, "Ada, Bo", "3"} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if !strings.Contains(got, now.UTC().Format("2006-01-02 15:04")) {
		t.Error("the page does not say when")
	}
	if strings.Contains(render(t, everyKind(t), PageOptions{}), `id="applied"`) {
		t.Error("a page with no review log claims something was applied")
	}
}

func TestAChecklistGivenByARelativePathIsShownByItsFullPath(t *testing.T) {
	html := render(t, everyKind(t), PageOptions{Checklist: "review.yaml"})
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wd, "review.yaml"); !strings.Contains(html, want) {
		t.Errorf("page lacks the full path %s", want)
	}
}

func sectionOf(t *testing.T, html, id string) string {
	t.Helper()
	_, sec, ok := strings.Cut(html, `<section id="`+id+`"`)
	if !ok {
		t.Fatalf("page lacks question %s", id)
	}
	sec, _, _ = strings.Cut(sec, "</section>")
	return sec
}

func argsOf(t *testing.T, sec, key string) []string {
	t.Helper()
	m := regexp.MustCompile(`data-k="` + regexp.QuoteMeta(key) + `" data-a="([^"]*)"`).FindStringSubmatch(sec)
	if m == nil {
		t.Fatalf("no %s with arguments in:\n%s", key, sec)
	}
	return parseArgs(t, m[1])
}

func TestAnIrreversibleQuestionSaysARealPersonApprovesNotAnAutomatedOne(t *testing.T) {
	sec := sectionOf(t, render(t, everyKind(t), PageOptions{}), "policy-irreversible")
	if got := argsOf(t, sec, "policy.decide")[2]; got != "@effect.approval_never_auto" {
		t.Errorf("effect = %q", got)
	}
	if !strings.Contains(text("zh", "policy.decide", []string{"1", "x", "@effect.approval_never_auto", ""}), "真人") {
		t.Error("the Chinese effect does not say a person approves")
	}
}

func TestTheNoLineListsTheChoicesAPersonCanPick(t *testing.T) {
	c := everyKind(t)
	html := render(t, c, PageOptions{})
	for _, q := range c.Questions {
		if len(q.Choices) == 0 {
			continue
		}
		sec := sectionOf(t, html, q.ID)
		kind := kindOf(q)
		no := kind + ".no"
		if kind == "unsure" {
			no = "policy.no"
		}
		if kind == "workflows" {
			continue // its no line names drop and keep, not a list
		}
		got := argsOf(t, sec, no)[3]
		if got != strings.Join(q.Choices, ", ") {
			t.Errorf("%s: choices shown %q, asked %v", q.ID, got, q.Choices)
		}
	}
}

func TestOnlyAReadQuestionWarnsThatNobodyWillApproveItsCalls(t *testing.T) {
	html := render(t, everyKind(t), PageOptions{})
	read, write := sectionOf(t, html, "overlay-read"), sectionOf(t, html, "overlay-write")
	if !strings.Contains(read, `data-k="overlay.yes.read"`) || strings.Contains(read, `data-k="overlay.yes"`) {
		t.Error("an overlay read question lacks its warning")
	}
	if !strings.Contains(write, `data-k="overlay.yes"`) || strings.Contains(write, "overlay.yes.read") {
		t.Error("an overlay write question carries the read warning")
	}
	for _, lang := range []string{"zh", "en"} {
		if !strings.Contains(text(lang, "overlay.yes.read", nil), map[string]string{"zh": "不經核准", "en": "nobody approving"}[lang]) {
			t.Errorf("%s warning does not say calls run unapproved", lang)
		}
	}
}

func TestAnAnsweredQuestionSaysRightOrNotRightAndWhatChanged(t *testing.T) {
	c := everyKind(t)
	answer(c, "policy-money", Answer{Confirmed: true})
	answer(c, "policy-write-unsure", Answer{Change: map[string]string{"POST /api/reports/search": "read"}})
	html := render(t, c, PageOptions{})
	if got := argsOf(t, sectionOf(t, html, "policy-money"), "q.answered"); len(got) != 1 || got[0] != "@ans.yes" {
		t.Errorf("a confirmed answer shows %v", got)
	}
	write := sectionOf(t, html, "policy-write-unsure")
	if got := argsOf(t, write, "q.answered"); len(got) != 1 || got[0] != "@ans.no" {
		t.Errorf("a not-right answer shows %v", got)
	}
	if !strings.Contains(write, "POST /api/reports/search") || !strings.Contains(write, "read") {
		t.Error("the change is not shown")
	}
	if strings.Contains(sectionOf(t, html, "policy-irreversible"), "q.answered") {
		t.Error("an unanswered question claims an answer")
	}
}

func TestTheAppliedLineCountsEveryEntryAndNamesTheLatestTime(t *testing.T) {
	log := []Entry{
		{At: now.Add(-48 * time.Hour), By: "Ada", Question: "a"},
		{At: now, By: "Bo", Question: "b"},
		{At: now.Add(-24 * time.Hour), By: "Ada", Question: "c"},
		{At: now.Add(-72 * time.Hour), By: "Ada", Question: "d"},
	}
	html := render(t, everyKind(t), PageOptions{Log: log})
	args := argsOf(t, html, "applied")
	if want := []string{"Ada, Bo", now.UTC().Format("2006-01-02 15:04 UTC"), "4"}; strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("applied = %v, want %v", args, want)
	}
}

func TestTheExampleSitsUnderTheQuestionAndItsCommentsFollowTheLanguage(t *testing.T) {
	c := everyKind(t)
	html := render(t, c, PageOptions{})
	_, ex, _ := strings.Cut(html, `<pre id="example">`)
	ex, _, _ = strings.Cut(ex, "</pre>")
	for _, want := range []string{"questions:\n  - id: " + c.Questions[0].ID + "\n    ", "\n    answer: {confirmed: true}", `data-k="ex.yes"`, `data-k="ex.except"`, `data-k="ex.no"`, `data-k="ex.rest"`} {
		if !strings.Contains(ex, want) {
			t.Errorf("example lacks %q:\n%s", want, ex)
		}
	}
	if strings.Contains(ex, "right:") {
		t.Error("an English comment is written into the Chinese example")
	}
	if got := argsOf(t, html, "how.b.3")[0]; got != strings.Join(c.Questions[0].Choices, ", ") {
		t.Errorf("the choices the example explains = %q", got)
	}
}

func TestEveryRiskTheModelUsesIsExplainedInPlainWords(t *testing.T) {
	html := render(t, everyKind(t), PageOptions{})
	for _, id := range []string{"policy-money", "policy-write-unsure", "policy-irreversible", "overlay-read"} {
		if sec := sectionOf(t, html, id); !strings.Contains(sec, `data-k="q.risk"`) || !strings.Contains(sec, `data-k="risk.`) {
			t.Errorf("%s does not say what its risk means", id)
		}
	}
	for _, risk := range []string{"read", "write", "money", "credential", "irreversible"} {
		if zh["risk."+risk] == "" || en["risk."+risk] == "" {
			t.Errorf("risk %s has no plain-words line in both languages", risk)
		}
	}
}

func TestAChecklistAskedInOneDirectoryIsAppliedFromAnother(t *testing.T) {
	dir := caseWithDrafts(t)
	t.Chdir(dir)
	if err := AskToFiles(AskOptions{CaseDir: ".", Out: "review.yaml", HTML: "review.html"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(filepath.Join(dir, "review.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{c.Files.Policy, c.Files.Overlay, c.Files.Workflows} {
		if f != "" && !filepath.IsAbs(f) {
			t.Errorf("the checklist names %q, which only works from where it was asked", f)
		}
	}
	for i := range c.Questions {
		c.Questions[i].Answer = &Answer{Confirmed: true}
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := ApplyFile(filepath.Join(dir, "review.yaml"), ApplyOptions{By: "Ada", Now: now}, &bytes.Buffer{}); err != nil {
		t.Fatalf("apply from another directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ReviewLog)); err != nil {
		t.Error(err)
	}
}
