package llm

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func capture(t *testing.T, c Claude) (dir string, args []string) {
	t.Helper()
	c.Exec = func(_ context.Context, d string, _ []string, _ string, a ...string) ([]byte, error) {
		dir, args = d, a
		return []byte(`{"result":"ok"}`), nil
	}
	if _, err := c.Ask(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	return dir, args
}

func TestWithoutAReadDirTheModelGetsNoTools(t *testing.T) {
	dir, args := capture(t, Claude{Bin: "claude", Model: "m", WorkDir: "/work"})
	joined := strings.Join(args, " ")
	if dir != "/work" || !strings.Contains(joined, "--restricted --tools ") || strings.Contains(joined, "Read") {
		t.Fatalf("dir %q args %v", dir, args)
	}
}

func TestAReadDirAllowsOnlyReadingThere(t *testing.T) {
	dir, args := capture(t, Claude{Bin: "claude", Model: "m", WorkDir: "/work", ReadDir: "/src"})
	if dir != "/src" {
		t.Fatalf("dir = %q, want the read dir", dir)
	}
	i := slices.Index(args, "--tools")
	if i < 0 || args[i+1] != "Read,Grep,Glob" {
		t.Fatalf("args = %v: only read tools may be offered", args)
	}
	for _, a := range args {
		if strings.Contains(a, "Edit") || strings.Contains(a, "Write") || strings.Contains(a, "Bash") {
			t.Fatalf("args = %v offer a tool that changes things", args)
		}
	}
}

func TestAMissingBinaryIsSaidPlainly(t *testing.T) {
	_, err := Command(context.Background(), "", nil, "/nonexistent/claude", "-p")
	if err == nil || !strings.Contains(err.Error(), "command not found") || strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("err = %v, want a plain not-found message without an empty stderr tail", err)
	}
}

// claude-2.1.301-ask.json is the output of `claude -p --output-format json`
// from Claude Code 2.1.301, recorded as is.
func TestAskReadsTheResultOfARealClaudeCodeReply(t *testing.T) {
	recorded, err := os.ReadFile("testdata/claude-2.1.301-ask.json")
	if err != nil {
		t.Fatal(err)
	}
	c := Claude{Bin: "claude", Model: "m", Exec: func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return recorded, nil
	}}
	got, err := c.Ask(t.Context(), "hi")
	if err != nil || got != "ok" {
		t.Fatalf("Ask = %q, %v, want the result field", got, err)
	}
}

func TestAskRefusesOutputThatIsNotJSON(t *testing.T) {
	c := Claude{Exec: func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return []byte("Error: not logged in"), nil
	}}
	if _, err := c.Ask(t.Context(), "hi"); err == nil || !strings.Contains(err.Error(), "parse model output") {
		t.Fatalf("err = %v", err)
	}
}

func TestIsolatedRunsTheModelInAnEmptyDirectoryThatCleanupRemoves(t *testing.T) {
	c, cleanup, err := Isolated(Claude{Bin: "claude", Model: "m"}, "lodog-test-")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(c.WorkDir)
	if err != nil || len(entries) != 0 || c.Exec == nil {
		t.Fatalf("dir %q: entries %v, err %v, exec set %v", c.WorkDir, entries, err, c.Exec != nil)
	}
	dir, _ := capture(t, c)
	if dir != c.WorkDir {
		t.Fatalf("the model ran in %q, want %q", dir, c.WorkDir)
	}
	cleanup()
	if _, err := os.Stat(c.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind (stat err %v)", c.WorkDir, err)
	}
}
