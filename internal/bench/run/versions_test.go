package run

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
)

var pins = []score.VersionPin{
	{Name: "source.commit", Expect: "abc123", Observe: []string{"probe", "commit"}},
	{Name: "database.image", Expect: "db@sha256:1", Observe: []string{"probe", "image"}},
}

func observing(outputs map[string]string, errs map[string]error) Observer {
	return func(_ context.Context, argv []string) (string, error) {
		return outputs[argv[1]], errs[argv[1]]
	}
}

func TestMatchingVersionsAreRecordedAndTheRunStarts(t *testing.T) {
	got, err := CheckVersions(t.Context(), pins, observing(map[string]string{"commit": "abc123\n", "image": "db@sha256:1"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []score.ObservedVersion{
		{Name: "source.commit", Expected: "abc123", Observed: "abc123", Match: true},
		{Name: "database.image", Expected: "db@sha256:1", Observed: "db@sha256:1", Match: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("observed = %+v", got)
	}
}

func TestADifferentVersionRefusesTheRunAndSaysWhatRuns(t *testing.T) {
	got, err := CheckVersions(t.Context(), pins, observing(map[string]string{"commit": "def456", "image": "db@sha256:1"}, nil))
	if !errors.Is(err, ErrPreflight) || !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want a version mismatch preflight error", err)
	}
	if !strings.Contains(err.Error(), `source.commit: expected "abc123", running "def456"`) || strings.Contains(err.Error(), "database.image") {
		t.Fatalf("message names the wrong pins: %v", err)
	}
	if got[0].Match || !got[1].Match {
		t.Fatalf("observed = %+v", got)
	}
}

func TestAVersionThatCannotBeObservedRefusesTheRun(t *testing.T) {
	got, err := CheckVersions(t.Context(), pins, observing(
		map[string]string{"commit": "abc123", "image": "db@sha256:1"},
		map[string]error{"image": errors.New("no container publishes the port")},
	))
	if !errors.Is(err, ErrVersionMismatch) || !strings.Contains(err.Error(), "could not observe: no container publishes the port") {
		t.Fatalf("err = %v", err)
	}
	if got[1].Match || got[1].Error == "" {
		t.Fatalf("observed = %+v", got[1])
	}
}

func TestACaseThatPinsNothingRefusesTheRun(t *testing.T) {
	_, err := CheckVersions(t.Context(), nil, observing(nil, nil))
	if !errors.Is(err, ErrPreflight) || errors.Is(err, ErrVersionMismatch) || !strings.Contains(err.Error(), "no versions") {
		t.Fatalf("err = %v", err)
	}
}

func TestObserveCommandRunsWithoutAShell(t *testing.T) {
	out, err := ObserveCommand(t.Context(), []string{"echo", "$HOME", "|", "x"})
	if err != nil || out != "$HOME | x\n" {
		t.Fatalf("out = %q, %v", out, err)
	}
}

func TestTheObservedVersionsAreWrittenToMeta(t *testing.T) {
	p := testPlan(t)
	p.Header.Versions = []score.ObservedVersion{{Name: "source.commit", Expected: "abc123", Observed: "abc123", Match: true}}
	var labels []gateway.Labels
	if err := Run(t.Context(), p, testDeps(&scriptedAgent{}, &scriptedJudge{}, &labels)); err != nil {
		t.Fatal(err)
	}
	if m := readMeta(t, p.RunDir); !slices.Equal(m.Versions, p.Header.Versions) {
		t.Fatalf("meta versions = %+v", m.Versions)
	}
}

func writeCase(t *testing.T, versions string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string][]byte{
		"rubric.yaml":           readFile(t, "../../../cases/backoffice/rubric.yaml"),
		"expected/answers.yaml": readFile(t, "../../../cases/backoffice/expected/answers.yaml"),
		"case.yaml":             []byte("benchmark:\n  answers: expected/answers.yaml\n  reset: true\n" + versions),
	}
	if err := os.MkdirAll(filepath.Join(dir, "expected"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBenchRunChecksVersionsBeforeItSignsIn(t *testing.T) {
	cases := map[string]struct {
		versions string
		mismatch bool
		want     string
	}{
		"mismatch": {"versions:\n  - {name: source.commit, expect: pinned, observe: echo running}\n", true, `expected "pinned", running "running"`},
		"no pins":  {"", false, "case.yaml has no versions"},
		// Matching pins pass the check; the run then stops at sign-in, which
		// has no credentials here.
		"match": {"versions:\n  - {name: source.commit, expect: pinned, observe: echo pinned}\n", false, "auth.username_env"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LODOG_UPSTREAM_USER", "")
			runs := t.TempDir()
			_, err := Execute(t.Context(), Options{
				CaseDir: writeCase(t, tc.versions), GatewayConfig: "../../../cases/backoffice/gateway/L0.yaml",
				RunsDir: runs, RunID: "r1", Attempts: 1,
			}, slog.New(slog.DiscardHandler))
			if !errors.Is(err, ErrPreflight) || errors.Is(err, ErrVersionMismatch) != tc.mismatch || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want preflight error with %q (mismatch %v)", err, tc.want, tc.mismatch)
			}
			if _, err := os.Stat(filepath.Join(runs, "r1")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused run left a run directory: %v", err)
			}
		})
	}
}
