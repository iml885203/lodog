package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/llm"
)

const fileDoc = `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{
	"/a":{"get":{"tags":["T"]}},
	"/b":{"get":{"tags":["T"]}}}}`

var promptOp = regexp.MustCompile(`(?m)^(GET) (/\S+)  \[tag`)

// modelAnswering answers every operation the prompt lists except skip, and
// records the prompts it was asked.
func modelAnswering(skip string, prompts *[]string) llm.Exec {
	return func(_ context.Context, _ string, _ []string, _ string, args ...string) ([]byte, error) {
		prompt := args[1]
		*prompts = append(*prompts, prompt)
		var ops []map[string]string
		for _, m := range promptOp.FindAllStringSubmatch(prompt, -1) {
			if m[2] == skip {
				continue
			}
			ops = append(ops, map[string]string{"method": m[1], "path": m[2], "summary": "s", "risk": "read", "confidence": "high"})
		}
		answer, _ := json.Marshal(map[string]any{"operations": ops})
		return json.Marshal(map[string]string{"result": string(answer)})
	}
}

// writeCase makes a case directory holding the document and, when given, a policy.
func writeCase(t *testing.T, casePolicy string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(fileDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if casePolicy != "" {
		if err := os.WriteFile(filepath.Join(dir, "spec", "policy.yaml"), []byte(casePolicy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runFile(t *testing.T, o FileOptions) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := RunToFile(t.Context(), o, &out)
	return out.String(), err
}

func TestRunToFileFailsWhenAnOperationStaysUndescribed(t *testing.T) {
	var prompts []string
	dir := writeCase(t, "")
	out, err := runFile(t, FileOptions{CaseDir: dir, Exec: modelAnswering("/b", &prompts)})
	if err == nil || !strings.Contains(err.Error(), "still have no description") {
		t.Fatalf("err = %v, want the incomplete overlay reported", err)
	}
	if !strings.Contains(out, "missing: GET /b") {
		t.Errorf("output does not list the missing operation:\n%s", out)
	}
	if len(prompts) < 2 {
		t.Errorf("the skipped operation was asked for %d times, want a retry", len(prompts)-1)
	}
}

func TestRunToFileWritesTheOverlayWhereTheCaseKeepsIt(t *testing.T) {
	var prompts []string
	dir := writeCase(t, "")
	out, err := runFile(t, FileOptions{CaseDir: dir, Exec: modelAnswering("", &prompts)})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "spec", "overlay.yaml")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote "+filepath.Join(dir, "spec", "overlay.yaml")+": 2 actions for 2 operations") {
		t.Errorf("summary = %q", out)
	}
}

func TestRunToFileKeepsAReviewedOverlayUnlessForced(t *testing.T) {
	var prompts []string
	dir := writeCase(t, "")
	overlay := filepath.Join(dir, "spec", "overlay.yaml")
	if err := os.WriteFile(overlay, []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runFile(t, FileOptions{CaseDir: dir, Exec: modelAnswering("", &prompts)})
	if err == nil || !strings.Contains(err.Error(), overlay+" exists and may hold reviewed edits; pass --force to overwrite") {
		t.Fatalf("err = %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("the model was asked %d times before the refusal", len(prompts))
	}
	if got, _ := os.ReadFile(overlay); string(got) != "reviewed" { //nolint:gosec // G304: a test temp path
		t.Fatalf("overlay overwritten: %q", got)
	}
	if out, err := runFile(t, FileOptions{CaseDir: dir, Force: true, Exec: modelAnswering("", &prompts)}); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestRunToFileNeedsACaseOrBothPaths(t *testing.T) {
	if _, err := runFile(t, FileOptions{SpecPath: "x.json"}); err == nil || err.Error() != "give --case, or both --spec and --out" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunToFileOffersTheRiskLevelsOfTheCasePolicy(t *testing.T) {
	var prompts []string
	if out, err := runFile(t, FileOptions{CaseDir: writeCase(t, ""), Exec: modelAnswering("", &prompts)}); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(prompts[0], "one of read, write, money, irreversible, credential.") {
		t.Fatalf("without a policy the prompt should offer the default levels:\n%s", prompts[0])
	}

	prompts = nil
	policyYAML := "default: bulk\nrisks:\n  read: allow\n  bulk: approval\n"
	if out, err := runFile(t, FileOptions{CaseDir: writeCase(t, policyYAML), Exec: modelAnswering("", &prompts)}); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(prompts[0], "one of read, bulk.") {
		t.Fatalf("the case's spec/policy.yaml should set the levels:\n%s", prompts[0])
	}
}

func TestRunToFilePolicyOverridesTheCasePolicy(t *testing.T) {
	var prompts []string
	flagPolicy := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(flagPolicy, []byte("default: other\nrisks:\n  read: allow\n  other: approval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeCase(t, "default: bulk\nrisks:\n  read: allow\n  bulk: approval\n")
	if out, err := runFile(t, FileOptions{CaseDir: dir, PolicyPath: flagPolicy, Exec: modelAnswering("", &prompts)}); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(prompts[0], "one of read, other.") {
		t.Fatalf("the given policy should set the levels:\n%s", prompts[0])
	}
}
