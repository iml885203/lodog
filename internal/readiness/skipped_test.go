package readiness

import (
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
)

func TestTheReportListsEveryOperationTheAgentIsNotGiven(t *testing.T) {
	v, err := FromSpec("testdata/petstore.json", DefaultSignals())
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	want := Unserved{Method: "POST", Path: "/pet/{petId}/uploadImage", Reason: "application/octet-stream bodies are not forwarded"}
	if len(r.Skipped) != 1 || r.Skipped[0] != want {
		t.Fatalf("skipped = %+v, want only %+v", r.Skipped, want)
	}
	var out strings.Builder
	if err := Render(&out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "POST /pet/{petId}/uploadImage：application/octet-stream bodies are not forwarded") {
		t.Fatalf("the printed report does not name the skipped operation:\n%s", out.String())
	}
}

func TestTheReportListsWhatThePolicyHides(t *testing.T) {
	p := &policy.Policy{
		Risks:   map[string]policy.Risk{"read": {Action: policy.Allow}, "write": {Action: policy.Approval}, "credential": {Action: policy.Hide}},
		Default: "write",
		Rules:   []policy.Rule{{Match: "GET /customer/*/resetPassword", Risk: "credential"}},
	}
	r := Score(taskView(t, p))
	if len(r.Skipped) != 1 || r.Skipped[0].Path != "/customer/{id}/resetPassword" || !strings.Contains(r.Skipped[0].Reason, "hidden by policy") {
		t.Fatalf("skipped = %+v", r.Skipped)
	}
}
