package review

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/overlay"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/workflow"
)

const backofficeDraft = "../../cases/backoffice/spec/policy.auto.yaml"

const draft = `# Drafted by lodog policy draft.
risks:
  irreversible: {action: approval, never_auto_approve: true}
  money: approval
  write: approval
  read: allow
rules:
  # irreversible: 1 operations
  - match: DELETE /api/items/{id}
    risk: irreversible
    why: 'heuristic: a DELETE'
  # money: 2 operations
  - match: POST /api/refunds
    risk: money
    why: 'needs review: model, not confident: may pay out'
  - match: POST /api/deposits
    risk: money
    why: 'heuristic: names money'
  # write: 1 operations
  - match: POST /api/reports/search
    risk: write
    why: 'needs review: model, not confident: looks like a query'
  # read: 1 operations
  - match: GET /api/items
    risk: read
    why: 'heuristic: a GET that changes nothing'
default: write
`

// handEdited is draft as a reviewer editing the YAML would leave it: the
// refund cannot be undone, the search POST is a query.
const handEdited = `risks:
  irreversible: {action: approval, never_auto_approve: true}
  money: approval
  write: approval
  read: allow
rules:
  - {match: 'DELETE /api/items/{id}', risk: irreversible}
  - {match: POST /api/refunds, risk: irreversible}
  - {match: POST /api/deposits, risk: money}
  - {match: POST /api/reports/search, risk: read}
  - {match: GET /api/items, risk: read}
default: write
`

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path inside the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sameRules(t *testing.T, got, want *policy.Policy) {
	t.Helper()
	if len(got.Rules) != len(want.Rules) || got.Default != want.Default || len(got.Risks) != len(want.Risks) {
		t.Fatalf("reviewed policy %+v, want %+v", got, want)
	}
	for i, r := range want.Rules {
		if got.Rules[i].Match != r.Match || got.Rules[i].Risk != r.Risk {
			t.Errorf("rule %d = %s %s, want %s %s", i, got.Rules[i].Match, got.Rules[i].Risk, r.Match, r.Risk)
		}
	}
}

func answer(c *Checklist, id string, a Answer) {
	for i := range c.Questions {
		if c.Questions[i].ID == id {
			c.Questions[i].Answer = &a
		}
	}
}

func ids(qs []Question) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.ID
	}
	return out
}

func TestTheBackofficePolicyDraftIsAskedInAtMostTwentyQuestions(t *testing.T) {
	qs, err := PolicyQuestions(backofficeDraft)
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.LoadFile(backofficeDraft)
	if err != nil {
		t.Fatal(err)
	}
	items := 0
	for _, q := range qs {
		items += len(q.Items)
	}
	if len(qs) > 20 || items != len(p.Rules) {
		t.Fatalf("%d questions covering %d of %d rules; want at most 20 covering every rule", len(qs), items, len(p.Rules))
	}
	if !strings.HasSuffix(qs[0].ID, "-unsure") {
		t.Fatalf("first question %s: the model's unsure rules are asked first", qs[0].ID)
	}
}

func TestAnswersGiveTheSamePolicyAsEditingTheYAML(t *testing.T) {
	in := writeFile(t, "policy.auto.yaml", draft)
	out := filepath.Join(t.TempDir(), "policy.reviewed.yaml")
	c, err := Ask(Files{Policy: in})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(c.Questions), " "); got != "policy-money-unsure policy-write-unsure policy-irreversible policy-money policy-read" {
		t.Fatalf("questions = %s", got)
	}
	for _, id := range ids(c.Questions) {
		answer(c, id, Answer{Confirmed: true})
	}
	answer(c, "policy-money-unsure", Answer{Change: map[string]string{"POST /api/refunds": "irreversible"}})
	answer(c, "policy-write-unsure", Answer{Confirmed: true, Change: map[string]string{"POST /api/reports/search": "read"}})
	entries, err := Apply(c, Options{By: "Ada", Now: now, PolicyOut: out})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 || entries[0].By != "Ada" || entries[0].Changed["POST /api/refunds"] != "irreversible" {
		t.Fatalf("log = %+v", entries)
	}
	got, err := policy.LoadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := policy.Parse([]byte(handEdited))
	if err != nil {
		t.Fatal(err)
	}
	sameRules(t, got, want)
	for _, r := range got.Rules {
		if !strings.HasPrefix(r.Why, "reviewed by Ada on 2026-10-16: ") {
			t.Errorf("rule %s why = %q: it names the reviewer and the date", r.Match, r.Why)
		}
	}
	data := readFile(t, out)
	if !strings.Contains(data, "# Drafted by lodog policy draft.") || !strings.Contains(data, "changed from money; drafted as: model, not confident: may pay out") {
		t.Fatalf("the reviewed policy keeps the draft's comments and says what changed:\n%s", data)
	}
}

func TestTheBackofficeDraftAnsweredYesWithOneChangeEqualsThatOneEdit(t *testing.T) {
	in := writeFile(t, "policy.auto.yaml", readFile(t, backofficeDraft))
	c, err := Ask(Files{Policy: in})
	if err != nil {
		t.Fatal(err)
	}
	read := c.Questions[len(c.Questions)-1]
	if read.ID != "policy-read" {
		t.Fatalf("last question %s: the batch of lookups is asked last", read.ID)
	}
	changed := read.Items[0].Key
	for _, id := range ids(c.Questions) {
		answer(c, id, Answer{Confirmed: true})
	}
	answer(c, "policy-read", Answer{Confirmed: true, Change: map[string]string{changed: "write"}})
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err != nil {
		t.Fatal(err)
	}
	got, err := policy.LoadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	want, err := policy.LoadFile(backofficeDraft)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want.Rules {
		if want.Rules[i].Match == changed {
			want.Rules[i].Risk = "write"
		}
	}
	sameRules(t, got, want)
	again, err := PolicyQuestions(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a reviewed policy still asks %v", ids(again))
	}
}

func TestAReviewedRuleIsNotAskedAgainAndAStaleChecklistIsRefused(t *testing.T) {
	in := writeFile(t, "policy.auto.yaml", draft)
	c, err := Ask(Files{Policy: in})
	if err != nil {
		t.Fatal(err)
	}
	answer(c, "policy-irreversible", Answer{Confirmed: true})
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err != nil {
		t.Fatal(err)
	}
	again, err := PolicyQuestions(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(ids(again), " "), "policy-irreversible") || len(again) != 4 {
		t.Fatalf("after one answer the questions are %v", ids(again))
	}
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err == nil || !strings.Contains(err.Error(), "already reviewed") {
		t.Fatalf("applying the same answer twice = %v, want it refused", err)
	}
}

func TestAnAnswerThatCannotBeAppliedWritesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		id string
		a  Answer
	}{
		"a no that leaves an item open": {"policy-money-unsure", Answer{}},
		"a change to an item not asked": {"policy-money", Answer{Confirmed: true, Change: map[string]string{"GET /api/other": "read"}}},
		"a change to an unknown risk":   {"policy-money", Answer{Confirmed: true, Change: map[string]string{"POST /api/deposits": "harmless"}}},
	} {
		t.Run(name, func(t *testing.T) {
			in := writeFile(t, "policy.auto.yaml", draft)
			c, err := Ask(Files{Policy: in})
			if err != nil {
				t.Fatal(err)
			}
			answer(c, "policy-irreversible", Answer{Confirmed: true})
			answer(c, tc.id, tc.a)
			if _, err := Apply(c, Options{By: "Ada", Now: now}); err == nil {
				t.Fatal("applied")
			}
			if readFile(t, in) != draft {
				t.Fatal("the policy changed although an answer was invalid")
			}
		})
	}
}

const overlayDoc = `overlay: 1.0.0
info:
    title: t
    version: "1"
actions:
    - target: $.paths['/api/items'].get
      update:
        summary: List items.
        x-agent:
            needsReview: true
            risk: read
    - target: $.paths['/api/items'].post
      update:
        summary: Create an item.
        x-agent:
            needsReview: true
            risk: write
    - target: $.paths['/api/items/{id}'].delete
      update:
        summary: Delete an item.
        x-agent:
            risk: irreversible
`

func TestOverlayAnswersSetTheRiskAndClearNeedsReview(t *testing.T) {
	path := writeFile(t, "overlay.yaml", overlayDoc)
	c, err := Ask(Files{Overlay: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(c.Questions), " "); got != "overlay-read overlay-write" {
		t.Fatalf("questions = %s; reviewed operations are not asked", got)
	}
	answer(c, "overlay-read", Answer{Change: map[string]string{"GET /api/items": "write"}})
	answer(c, "overlay-write", Answer{Confirmed: true})
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err != nil {
		t.Fatal(err)
	}
	o, err := overlay.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"write", "write", "irreversible"} {
		agent := o.Actions[i].Update["x-agent"].(map[string]any)
		if agent["risk"] != want || agent["needsReview"] != nil {
			t.Errorf("%s x-agent = %v, want risk %s and no needsReview", o.Actions[i].Target, agent, want)
		}
	}
}

const workflowsDoc = `# Drafted, then reviewed in part.
arazzo: 1.0.1
info:
    title: t
    version: "1"
workflows:
    - workflowId: find_item
      summary: Find an item.
      steps:
        - stepId: s
          operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1items/get'
      x-agent:
        next: [give_bonus, list_items]
        needsReview: true
    - workflowId: give_bonus
      summary: Give a bonus.
      steps:
        - stepId: s
          operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1bonus/post'
      x-agent:
        risk: money
        needsReview: true
        reviewNote: POST /api/bonus changes data and no tool undoes it.
    - workflowId: list_items
      steps:
        - stepId: s
          operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1items/get'
`

func TestWorkflowAnswersKeepOrDropTheTool(t *testing.T) {
	path := writeFile(t, "workflows.arazzo.yaml", workflowsDoc)
	c, err := Ask(Files{Workflows: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Questions) != 1 || len(c.Questions[0].Items) != 2 {
		t.Fatalf("questions = %+v", c.Questions)
	}
	answer(c, "workflows-unsure", Answer{Confirmed: true, Change: map[string]string{"give_bonus": Drop}})
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err != nil {
		t.Fatal(err)
	}
	d, err := workflow.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Workflows) != 2 || d.Workflows[0].WorkflowID != "find_item" || d.Workflows[0].Agent.NeedsReview || d.Workflows[1].WorkflowID != "list_items" ||
		len(d.Workflows[0].Agent.Next) != 1 || d.Workflows[0].Agent.Next[0] != "list_items" {
		t.Fatalf("workflows = %+v", d.Workflows)
	}
	if data := readFile(t, path); !strings.HasPrefix(data, "# Drafted, then reviewed in part.") {
		t.Fatalf("the file's comments are kept:\n%s", data)
	}
}

func TestAWriteWithNoUndoIsAskedWithItsNoteAndTheNoteGoesWhenAnswered(t *testing.T) {
	path := writeFile(t, "workflows.arazzo.yaml", workflowsDoc)
	c, err := Ask(Files{Workflows: path})
	if err != nil {
		t.Fatal(err)
	}
	var why string
	for _, it := range c.Questions[0].Items {
		if it.Key == "give_bonus" {
			why = it.Why
		}
	}
	if !strings.Contains(why, "no tool undoes it") {
		t.Fatalf("the person is not told the write has no undo: %q", why)
	}
	answer(c, "workflows-unsure", Answer{Confirmed: true})
	if _, err := Apply(c, Options{By: "Ada", Now: now}); err != nil {
		t.Fatal(err)
	}
	if data := readFile(t, path); strings.Contains(data, "reviewNote") || strings.Contains(data, "needsReview") {
		t.Fatalf("an answered tool keeps its review marks:\n%s", data)
	}
}

func TestTheSummaryPageListsEveryQuestion(t *testing.T) {
	c, err := Ask(Files{Policy: writeFile(t, "policy.auto.yaml", draft)})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := RenderHTML(&b, c, PageOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range c.Questions {
		if !strings.Contains(b.String(), `id="`+q.ID+`"`) {
			t.Errorf("page lacks %s", q.ID)
		}
	}
	if !strings.Contains(b.String(), "DELETE /api/items/{id}") {
		t.Error("page lacks the items")
	}
}

func TestTheReviewLogAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review-log.yaml")
	for _, q := range []string{"a", "b"} {
		if err := AppendLog(path, []Entry{{At: now, By: "Ada", Question: q}}); err != nil {
			t.Fatal(err)
		}
	}
	if data := readFile(t, path); strings.Count(data, "by: Ada") != 2 {
		t.Fatalf("log:\n%s", data)
	}
}
