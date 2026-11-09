package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/review"
)

const reviewDraft = `risks:
  irreversible: {action: approval, never_auto_approve: true}
  write: approval
  read: allow
rules:
  - match: DELETE /api/items/{id}
    risk: irreversible
    why: 'heuristic: a DELETE'
  - match: GET /api/items
    risk: read
    why: 'heuristic: a GET'
default: write
`

func runLodog(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String() + errOut.String(), err
}

func TestReviewAsksTheCaseDraftsAndAppliesTheAnswers(t *testing.T) {
	caseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseDir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	drafted := filepath.Join(caseDir, review.DraftPolicy)
	if err := os.WriteFile(drafted, []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	checklist, page := filepath.Join(caseDir, "review.yaml"), filepath.Join(caseDir, "review.html")
	if out, err := runLodog(t, "review", "ask", "--case", caseDir, "-o", checklist, "--html", page); err != nil || !strings.Contains(out, "2 questions about 2 pending items") {
		t.Fatalf("ask: %v\n%s", err, out)
	}
	if _, err := os.Stat(page); err != nil {
		t.Fatal(err)
	}
	c, err := review.Load(checklist)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Questions {
		c.Questions[i].Answer = &review.Answer{Confirmed: true}
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reviewed := filepath.Join(caseDir, "spec", "policy.reviewed.yaml")
	if out, err := runLodog(t, "review", "apply", checklist, "--by", "Ada", "--policy-out", reviewed); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(drafted); string(got) != reviewDraft { //nolint:gosec // G304: a path inside the test's own temp dir
		t.Fatal("the draft changed although --policy-out names another file")
	}
	log, err := os.ReadFile(filepath.Join(caseDir, "review-log.yaml")) //nolint:gosec // G304: a path inside the test's own temp dir
	if err != nil || strings.Count(string(log), "by: Ada") != 2 {
		t.Fatalf("review log: %v\n%s", err, log)
	}
}
