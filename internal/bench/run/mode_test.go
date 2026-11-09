package run

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

type discardSink struct{}

func (discardSink) Append(record.Call) error { return nil }

func serveProxy(t *testing.T, readOnly bool) string {
	t.Helper()
	system := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the mode question reached the system")
	}))
	t.Cleanup(system.Close)
	target, err := url.Parse(system.URL)
	if err != nil {
		t.Fatal(err)
	}
	px := proxy.New(target, nil, discardSink{}, slog.New(slog.DiscardHandler))
	if readOnly {
		doc := &spec.Document{Operations: []spec.Operation{{Method: http.MethodGet, Path: "/api/x"}}}
		pol, err := policy.Parse([]byte("risks: {read: allow, write: approval}\ndefault: write\nrules: [{match: \"GET /**\", risk: read}]\n"))
		if err != nil {
			t.Fatal(err)
		}
		g, err := proxy.NewGuard(doc, pol, nil)
		if err != nil {
			t.Fatal(err)
		}
		px = px.ReadOnly(g)
	}
	srv := httptest.NewServer(px)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAReadOnlyRunNeedsAReadOnlyProxy(t *testing.T) {
	if _, err := runMode(t.Context(), true, serveProxy(t, false), false, "r1"); !errors.Is(err, ErrPreflight) {
		t.Fatalf("read-only run on a full proxy: err = %v", err)
	}
	mode, err := runMode(t.Context(), true, serveProxy(t, true), false, "r1")
	if err != nil || mode != record.ModeReadOnly {
		t.Fatalf("mode = %q, err = %v", mode, err)
	}
}

func TestAFullRunRefusesAProxyThatSendsNoWrites(t *testing.T) {
	if _, err := runMode(t.Context(), false, serveProxy(t, true), false, "r1"); !errors.Is(err, ErrPreflight) {
		t.Fatalf("full run on a read-only proxy: err = %v", err)
	}
	mode, err := runMode(t.Context(), false, serveProxy(t, false), false, "r1")
	if err != nil || mode != record.ModeFull {
		t.Fatalf("mode = %q, err = %v", mode, err)
	}
}

func TestAFullRunStillStartsInFrontOfSomethingThatIsNotALodogProxy(t *testing.T) {
	system := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(system.Close)
	mode, err := runMode(t.Context(), false, system.URL, false, "r1")
	if err != nil || mode != record.ModeFull {
		t.Fatalf("mode = %q, err = %v", mode, err)
	}
	if _, err := runMode(t.Context(), true, system.URL, false, "r1"); !errors.Is(err, ErrPreflight) {
		t.Fatalf("read-only run without a lodog proxy: err = %v", err)
	}
}

func TestAReadOnlyRunSkipsResetAndStateChecksAndRecordsItsMode(t *testing.T) {
	p := testPlan(t)
	p.Header.Mode = record.ModeReadOnly
	task := p.Case.Tasks["T07"]
	task.StateCheck = &score.StateCheck{Call: "GET /api/x", Include: []string{"BOB-01"}}
	p.Case.Tasks["T07"] = task
	var labels []gateway.Labels
	d := testDeps(&scriptedAgent{}, &scriptedJudge{}, &labels)
	d.CheckState = func(context.Context, score.StateCheck, int) (bool, error) {
		t.Error("a read-only run ran a state check")
		return false, nil
	}
	if err := Run(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	m := readMeta(t, p.RunDir)
	if m.Mode != record.ModeReadOnly || m.Attempts[0].Status != StatusDone || m.Attempts[0].StateCheck != "" {
		t.Fatalf("meta = %+v", m)
	}
	ev, err := score.LoadEvidence(p.RunDir)
	if err != nil || ev.Mode != record.ModeReadOnly {
		t.Fatalf("evidence mode = %q, err = %v", ev.Mode, err)
	}
	reset := resetter(Options{ReadOnly: true}, []string{"false"})
	if err := reset(t.Context()); err != nil {
		t.Fatalf("a read-only run ran the reset: %v", err)
	}
}
