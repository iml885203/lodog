package workflowdraft

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shopCase is a case directory holding the shop document.
func shopCase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := "overlay: 1.0.0\ninfo: {title: shop, version: \"1\"}\nactions: []\n"
	if err := os.WriteFile(filepath.Join(dir, "spec", "overlay.yaml"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// answering is a model that gives answer and records the directory it ran in.
func answering(answer string, ranIn *string) func(context.Context, string, []string, string, ...string) ([]byte, error) {
	return func(_ context.Context, dir string, _ []string, _ string, _ ...string) ([]byte, error) {
		*ranIn = dir
		return json.Marshal(map[string]string{"result": answer})
	}
}

func TestRunToFileWritesTheDraftWithAReviewHeaderAndReportsWhatNeedsReview(t *testing.T) {
	dir := shopCase(t)
	var ranIn string
	var out bytes.Buffer
	err := RunToFile(t.Context(), FileOptions{
		CaseDir: dir, Source: "shop", Model: "m1", ClaudeBin: "claude",
		Exec: answering(answerWith(goodFind, goodBonus), &ranIn),
	}, &out)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	path := filepath.Join(dir, "spec", "workflows.arazzo.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // G304: a test temp path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# Drafted by `lodog workflows draft`") || !strings.Contains(string(data), "model m1.") {
		t.Errorf("header missing:\n%.300s", data)
	}
	if !strings.Contains(out.String(), "wrote "+path+": 2 task tools, 1 need review") || !strings.Contains(out.String(), "  review: give_bonus") {
		t.Errorf("report = %q", out.String())
	}
	if ranIn == "" || strings.HasPrefix(ranIn, dir) {
		t.Errorf("the model ran in %q; it must run in an empty directory outside the case", ranIn)
	}
	if _, err := os.Stat(ranIn); !os.IsNotExist(err) {
		t.Errorf("the model's work directory %s was left behind", ranIn)
	}
}

func TestRunToFileKeepsAReviewedFileUnlessForced(t *testing.T) {
	dir := shopCase(t)
	path := filepath.Join(dir, "spec", "workflows.arazzo.yaml")
	if err := os.WriteFile(path, []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	var ranIn string
	exec := answering(answerWith(goodFind), &ranIn)
	err := RunToFile(t.Context(), FileOptions{CaseDir: dir, Exec: exec}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), path+" exists and may hold reviewed edits; pass --force to overwrite") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "reviewed" || ranIn != "" { //nolint:gosec // G304: a test temp path
		t.Fatalf("file %q, model asked in %q: nothing should change before the refusal", got, ranIn)
	}
	if err := RunToFile(t.Context(), FileOptions{CaseDir: dir, Source: "shop", Force: true, Exec: exec}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunToFileNeedsACase(t *testing.T) {
	if err := RunToFile(t.Context(), FileOptions{}, &bytes.Buffer{}); err == nil || err.Error() != "give --case" {
		t.Fatalf("err = %v", err)
	}
}
