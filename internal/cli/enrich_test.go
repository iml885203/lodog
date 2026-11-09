package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/llm"
)

const enrichDoc = `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{"/a":{"get":{"tags":["T"]}}}}`

func runEnrich(t *testing.T, exec llm.Exec, args ...string) (string, error) {
	t.Helper()
	prev := enrichExec
	enrichExec = exec
	t.Cleanup(func() { enrichExec = prev })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(enrichDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"enrich", "--case", dir}, args...))
	err := root.Execute()
	return out.String() + errOut.String(), err
}

func TestEnrichHandsItsFlagsToTheDraft(t *testing.T) {
	var asked []string
	exec := func(_ context.Context, _ string, _ []string, bin string, args ...string) ([]byte, error) {
		asked = append(asked, bin+" "+strings.Join(args, " "))
		return []byte(`{"result":"{\"operations\":[{\"method\":\"GET\",\"path\":\"/a\",\"summary\":\"s\",\"risk\":\"read\",\"confidence\":\"high\"}]}"}`), nil
	}
	out, err := runEnrich(t, exec, "--claude", "my-claude", "--model", "m1")
	if err != nil || !strings.Contains(out, "wrote ") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "my-claude ") || !strings.Contains(asked[0], "--model m1") {
		t.Fatalf("asked %v, want --claude and --model passed to the model call", asked)
	}
}
