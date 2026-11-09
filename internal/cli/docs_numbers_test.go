package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// The numbers that describe a case (how many tools, which static score, how
// many operations, how many items wait for review) are written down in two
// places only: docs/static-score.md and the case's README. Every one of them
// carries a marker comment, and this test recomputes it from the repo:
//
//	<!-- lodog-number: score cases/backoffice/gateway/L2.yaml static=24.83 tools=25 -->
//	<!-- lodog-number: operations cases/backoffice/spec/openapi.json 232 -->
//	<!-- lodog-number: needs-review-rules cases/keycloak/spec/policy.auto.yaml 32 -->
//	<!-- lodog-number: needs-review-workflows cases/keycloak/spec/workflows.arazzo.yaml 3 -->
//
// A score marker on a table row also pins what the row shows: the row must
// have a cell with that static score (or "<score> / 30") and that tool count.
// So a number that drifts from `lodog score`, or a marker that is dropped,
// turns this test red instead of waiting for someone to notice.

// The case paths in gateway configs are relative to where lodog runs, so the
// test runs from the repository root.
const repoRoot = "."

var markerRe = regexp.MustCompile(`<!-- lodog-number: (\S+) (.*?) -->`)

type marker struct {
	file string
	line string
	kind string
	args []string
}

func readMarkers(t *testing.T, rel string) []marker {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, rel)) //nolint:gosec // G304: a fixed list of repo docs
	if err != nil {
		t.Fatal(err)
	}
	var out []marker
	for _, line := range strings.Split(string(data), "\n") {
		for _, m := range markerRe.FindAllStringSubmatch(line, -1) {
			out = append(out, marker{file: rel, line: line, kind: m[1], args: strings.Fields(m[2])})
		}
	}
	return out
}

// scoreOf runs `lodog score` and reads the first line of its output.
func scoreOf(t *testing.T, path string) (static string, tools int) {
	t.Helper()
	args := []string{"score", path}
	if strings.HasSuffix(path, ".yaml") {
		args = []string{"score", "-c", path}
	}
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("lodog %s: %v", strings.Join(args, " "), err)
	}
	m := regexp.MustCompile(`^靜態分數 ([0-9.]+) / 30（.*，(\d+) 個 tool）`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("lodog %s: unexpected first line: %q", strings.Join(args, " "), strings.SplitN(out.String(), "\n", 2)[0])
	}
	tools, _ = strconv.Atoi(m[2])
	return m[1], tools
}

func rowHasCell(line, want string, alsoWithMax bool) bool {
	line = markerRe.ReplaceAllString(line, "")
	for _, cell := range strings.Split(line, "|") {
		cell = strings.TrimSpace(cell)
		if cell == want || (alsoWithMax && cell == want+" / 30") {
			return true
		}
	}
	return false
}

func checkScore(t *testing.T, m marker) {
	t.Helper()
	if len(m.args) < 2 {
		t.Fatalf("%s: marker %q needs a path and static=/tools=", m.file, m.args)
	}
	path := m.args[0]
	static, tools := scoreOf(t, filepath.Join(repoRoot, path))
	onRow := strings.HasPrefix(strings.TrimSpace(m.line), "|")
	for _, kv := range m.args[1:] {
		k, v, _ := strings.Cut(kv, "=")
		got := static
		if k == "tools" {
			got = strconv.Itoa(tools)
		} else if k != "static" {
			t.Errorf("%s: unknown score key %q", m.file, k)
			continue
		}
		if v != got {
			t.Errorf("%s: %s %s is %s in the doc, lodog score gives %s", m.file, path, k, v, got)
		}
		if onRow && !rowHasCell(m.line, v, k == "static") {
			t.Errorf("%s: the row for %s has no cell %s (its marker says it shows it)", m.file, path, v)
		}
	}
}

func checkCount(t *testing.T, m marker) {
	t.Helper()
	path, want := m.args[0], m.args[1]
	n := 0
	switch m.kind {
	case "operations":
		doc, err := spec.LoadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		n = len(doc.Operations)
	case "needs-review-rules":
		p, err := policy.LoadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Rules {
			if strings.HasPrefix(r.Why, "needs review") {
				n++
			}
		}
	case "needs-review-workflows":
		d, err := workflow.LoadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range d.Workflows {
			if w.Agent.NeedsReview {
				n++
			}
		}
	default:
		t.Errorf("%s: unknown marker kind %q", m.file, m.kind)
		return
	}
	if got := strconv.Itoa(n); got != want {
		t.Errorf("%s: %s %s is %s in the doc, the file gives %s", m.file, m.kind, path, want, got)
	}
}

func TestTheNumbersInTheDocsAreTheOnesLodogScoreGives(t *testing.T) {
	t.Chdir("../..")
	pinned := map[string]bool{} // score paths pinned in docs/static-score.md
	for _, f := range []string{"docs/static-score.md", "cases/backoffice/README.md", "cases/keycloak/README.md"} {
		ms := readMarkers(t, f)
		if len(ms) == 0 {
			t.Fatalf("%s has no lodog-number marker: the numbers it states are not pinned", f)
		}
		for _, m := range ms {
			if m.kind == "score" {
				checkScore(t, m)
				if f == "docs/static-score.md" {
					pinned[m.args[0]] = true
				}
			} else {
				checkCount(t, m)
			}
		}
	}

	// docs/static-score.md pins every level of every case, so a new gateway
	// config cannot be added without its number.
	configs, err := filepath.Glob(filepath.Join(repoRoot, "cases", "*", "gateway", "*.yaml"))
	if err != nil || len(configs) == 0 {
		t.Fatalf("no gateway configs found: %v", err)
	}
	var missing []string
	for _, c := range configs {
		rel, _ := filepath.Rel(repoRoot, c)
		if !pinned[filepath.ToSlash(rel)] {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("docs/static-score.md has no lodog-number marker for: %s", fmt.Sprint(missing))
	}
}
