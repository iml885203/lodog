package review

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func caseWithDrafts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(DraftPolicy, draft)
	write("spec/policy.yaml", "default: read\n")
	return dir
}

func TestCaseFilesPreferTheDraftedPolicyAndSkipWhatIsMissing(t *testing.T) {
	dir := caseWithDrafts(t)
	got := CaseFiles(dir, Files{})
	if got.Policy != filepath.Join(dir, DraftPolicy) || got.Overlay != "" || got.Workflows != "" {
		t.Fatalf("files = %+v", got)
	}
	if err := os.Remove(filepath.Join(dir, DraftPolicy)); err != nil {
		t.Fatal(err)
	}
	if got := CaseFiles(dir, Files{}); got.Policy != filepath.Join(dir, "spec", "policy.yaml") {
		t.Fatalf("without a draft the policy is %q", got.Policy)
	}
}

func TestCaseFilesKeepWhatTheOperatorNamed(t *testing.T) {
	got := CaseFiles(caseWithDrafts(t), Files{Policy: "mine.yaml", Overlay: "o.yaml"})
	if got.Policy != "mine.yaml" || got.Overlay != "o.yaml" {
		t.Fatalf("files = %+v", got)
	}
}

func TestAskToFilesWritesTheChecklistTheSummaryPageAndTheCounts(t *testing.T) {
	dir := caseWithDrafts(t)
	out, page := filepath.Join(dir, "review.yaml"), filepath.Join(dir, "review.html")
	var stdout, stderr bytes.Buffer
	if err := AskToFiles(AskOptions{CaseDir: dir, Out: out, HTML: page}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{out, page} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), " questions about ") || !strings.HasSuffix(stderr.String(), " pending items\n") {
		t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestAskToFilesWithoutAnOutPrintsTheChecklist(t *testing.T) {
	dir := caseWithDrafts(t)
	var stdout bytes.Buffer
	if err := AskToFiles(AskOptions{CaseDir: dir}, &stdout, &bytes.Buffer{}); err != nil || !strings.Contains(stdout.String(), "questions:") {
		t.Fatalf("stdout %q, err %v", stdout.String(), err)
	}
}

func TestApplyFileLogsNextToTheChecklistUnlessToldOtherwise(t *testing.T) {
	dir := caseWithDrafts(t)
	checklist := filepath.Join(dir, "review.yaml")
	if err := AskToFiles(AskOptions{CaseDir: dir, Out: checklist}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(checklist)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Questions {
		c.Questions[i].Answer = &Answer{Confirmed: true}
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reviewed := filepath.Join(dir, "reviewed.yaml")
	if err := ApplyFile(checklist, ApplyOptions{By: "Ada", PolicyOut: reviewed, Now: time.Now()}, &out); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "review-log.yaml")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "logged ") || !strings.HasSuffix(out.String(), " answers to "+logPath+"\n") {
		t.Fatalf("report = %q", out.String())
	}
}

func TestApplyFileNeedsWhoAnswered(t *testing.T) {
	dir := caseWithDrafts(t)
	checklist := filepath.Join(dir, "review.yaml")
	if err := AskToFiles(AskOptions{CaseDir: dir, Out: checklist}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyFile(checklist, ApplyOptions{Now: time.Now()}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "say who answered") {
		t.Fatalf("err = %v", err)
	}
}
