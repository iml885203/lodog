package policydraft

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every operation of this document is settled by heuristics, so no model is asked.
const settledDoc = `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{
	"/api/items":{"get":{}},
	"/api/items/{id}":{"delete":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}}}}`

func noModel(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, errors.New("the model must not be asked")
}

func writeDoc(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(path, []byte(settledDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunToFileWithoutAnOutPrintsThePolicyAndSummarisesOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := RunToFile(t.Context(), FileOptions{OpenAPI: writeDoc(t), Exec: noModel}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "risk: irreversible") || !strings.Contains(stdout.String(), "risk: read") {
		t.Errorf("policy on stdout:\n%s", stdout.String())
	}
	if stderr.Len() == 0 || strings.Contains(stderr.String(), "risk: irreversible") {
		t.Errorf("stderr should hold the summary only:\n%s", stderr.String())
	}
}

func TestRunToFileWithAnOutWritesTheFileAndLeavesStdoutEmpty(t *testing.T) {
	out := filepath.Join(t.TempDir(), "policy.auto.yaml")
	var stdout bytes.Buffer
	if err := RunToFile(t.Context(), FileOptions{OpenAPI: writeDoc(t), Out: out, Exec: noModel}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out) //nolint:gosec // G304: a test temp path
	if err != nil || !strings.Contains(string(data), "risk: irreversible") || stdout.Len() != 0 {
		t.Fatalf("file %q, stdout %q, err %v", data, stdout.String(), err)
	}
}

func TestRunToFileNamesAMissingDocument(t *testing.T) {
	err := RunToFile(t.Context(), FileOptions{OpenAPI: "/nonexistent/openapi.json", Exec: noModel}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/openapi.json") {
		t.Fatalf("err = %v", err)
	}
}
