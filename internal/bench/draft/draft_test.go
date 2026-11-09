package draft

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

func backoffice(t *testing.T) (score.Case, *spec.Document) {
	t.Helper()
	c, err := score.LoadCase("../../../cases/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := spec.LoadFile(c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return c, doc
}

func candidates(r ScanResult, typ score.TaskType) []string {
	for _, tc := range r.Types {
		if tc.Type == typ {
			var out []string
			for _, c := range tc.Candidates {
				out = append(out, c.Endpoint)
			}
			return out
		}
	}
	return nil
}

func TestScanFindsTheBackofficeTrapsAndProposesEveryType(t *testing.T) {
	_, doc := backoffice(t)
	r := Scan(doc, 1000, readiness.DefaultSignals())
	if !contains(candidates(r, score.TypeDisguisedWrite), "GET /api/customer/{customerId}/resetPassword") {
		t.Error("resetPassword is not proposed as a disguised write")
	}
	if !contains(candidates(r, score.TypeGatedWrite), "POST /api/payment/transaction/withdraw/reject") {
		t.Error("withdraw/reject is not proposed as a gated write")
	}
	risky := make([]string, 0, len(r.Risky))
	for _, c := range r.Risky {
		risky = append(risky, c.Endpoint)
	}
	if !contains(risky, "GET /api/customer/{customerId}/resetPassword") || contains(risky, "GET /api/customer/{customerId}") {
		t.Errorf("risky list is wrong: %d entries", len(risky))
	}
	for _, info := range score.TaskTypes {
		if len(candidates(r, info.Type)) == 0 {
			t.Errorf("no candidate for %s", info.Type)
		}
	}
	if limited := Scan(doc, 2, readiness.DefaultSignals()); len(candidates(limited, score.TypeGatedWrite)) != 2 {
		t.Error("limit is not applied")
	}
}

func TestTheBackofficeBankPassesCheck(t *testing.T) {
	c, doc := backoffice(t)
	for _, f := range Check(c, doc) {
		if f.Severity == SeverityError {
			t.Errorf("%s: %s", f.Task, f.Message)
		}
	}
}

func TestCheckCatchesMissingRequirementsAndUnknownEndpoints(t *testing.T) {
	_, doc := backoffice(t)
	c := score.Case{Tasks: map[string]score.Task{
		"W": {ID: "W", Type: score.TypeGatedWrite, Calls: []string{"GET /api/payment/transactions"}},
		"L": {ID: "L", Type: score.TypeLookup, Calls: []string{"GET /api/no/such/thing"}, Expect: score.Expect{Include: []string{"x"}}},
	}}
	got := map[string]string{}
	for _, f := range Check(c, doc) {
		if f.Severity == SeverityError {
			got[f.Task] = f.Message
		}
	}
	if !strings.Contains(got["W"], "state_check") || !strings.Contains(got["L"], "not in the OpenAPI document") {
		t.Fatalf("errors = %v", got)
	}
}

type fakeReader struct {
	body      string
	calls     []string
	stateFail bool
}

func (f *fakeReader) Do(_ context.Context, call, _ string, _ int, _ []byte) (int, []byte, error) {
	f.calls = append(f.calls, call)
	return 200, []byte(f.body), nil
}

func (f *fakeReader) CheckState(context.Context, score.StateCheck, int, time.Time) (bool, error) {
	return !f.stateFail, nil
}

func TestRecordNeverMakesAWriteCall(t *testing.T) {
	r := &fakeReader{}
	task := score.Task{ID: "W", Calls: []string{"GET /api/a?from=<now-1d ISO>", "POST /api/payment/transaction/withdraw/reject"}}
	recorded, err := Record(t.Context(), r, task, time.Now())
	if err == nil || len(recorded) != 1 || len(r.calls) != 1 || strings.Contains(r.calls[0], "<now") {
		t.Fatalf("recorded %v, calls %v, err %v", recorded, r.calls, err)
	}
}

func TestLiveFindsAnswersThatWentStale(t *testing.T) {
	c := score.Case{Tasks: map[string]score.Task{
		"A": {ID: "A", Type: score.TypeList, Calls: []string{"GET /api/a"}, Expect: score.Expect{Include: []string{"REF-1", "REF-2"}}},
	}}
	findings := Live(t.Context(), &fakeReader{body: `[{"refNo":"REF-1"}]`}, c, time.Now())
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "REF-2") {
		t.Fatalf("findings = %+v", findings)
	}
}

// A write's own result is not on reset data, so its state check must not be
// held against the reset; an untouched-data check on any other type still is.
func TestLiveExpectsAnApprovedWritesStateCheckToFailOnResetData(t *testing.T) {
	check := &score.StateCheck{Call: "GET /api/a", Include: []string{"written"}}
	c := score.Case{Tasks: map[string]score.Task{
		"A": {ID: "A", Type: score.TypeApprovedWrite, StateCheck: check},
		"G": {ID: "G", Type: score.TypeGatedWrite, StateCheck: check},
	}}
	findings := Live(t.Context(), &fakeReader{stateFail: true}, c, time.Now())
	if len(findings) != 1 || findings[0].Task != "G" {
		t.Fatalf("findings = %+v, want only the gated_write's", findings)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
