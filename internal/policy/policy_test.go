package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/spec"
)

func parse(t *testing.T, yaml string) (*Policy, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadFile(path)
}

const base = `
risks: {read: allow, write: approval, credential: hide}
default: write
rules:
  - {match: "GET /api/customer/*/resetPassword", risk: credential}
  - {match: "GET /api/**", risk: read}
`

func TestTheFirstMatchingRuleDecides(t *testing.T) {
	p, err := parse(t, base)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Decide("GET", "/api/customer/{customerId}/resetPassword")
	if got.Action != Hide || got.Risk != "credential" || !got.Explicit() {
		t.Fatalf("resetPassword = %+v: the GET that resets a password must not fall through to read", got)
	}
	if got := p.Decide("GET", "/api/customer/{customerId}"); got.Action != Allow {
		t.Fatalf("plain GET = %+v", got)
	}
}

func TestAnUnmatchedOperationFailsClosed(t *testing.T) {
	p, err := parse(t, base)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Decide("POST", "/api/payment/transaction/withdraw/reject")
	if got.Action != Approval || got.Rule != DefaultRule || got.Explicit() {
		t.Fatalf("unmatched POST = %+v, want approval by default", got)
	}
}

func TestADefaultThatAllowsIsRefused(t *testing.T) {
	_, err := parse(t, `risks: {read: allow}
default: read`)
	if err == nil || !strings.Contains(err.Error(), "fail closed") {
		t.Fatalf("err = %v, want a fail-closed refusal", err)
	}
}

func TestABrokenPolicyIsRefused(t *testing.T) {
	for name, y := range map[string]string{
		"no default":         `risks: {write: approval}`,
		"unknown action":     "risks: {write: maybe}\ndefault: write",
		"rule without risk":  "risks: {write: approval}\ndefault: write\nrules: [{match: \"GET /x\", risk: nope}]",
		"match without path": "risks: {write: approval}\ndefault: write\nrules: [{match: \"GET\", risk: write}]",
		"allowed yet held":   "risks: {write: approval, read: {action: allow, never_auto_approve: true}}\ndefault: write",
	} {
		if _, err := parse(t, y); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestPathPatterns(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/api/*", "/api/customer", true},
		{"/api/*", "/api/customer/1", false},
		{"/api/**", "/api/customer/1", true},
		{"/api/**", "/api", true},
		{"/api/payment/transaction/**", "/api/payment/transaction/withdraw/reject", true},
		{"/api/payment/transaction/**", "/api/payment/transactions", false},
		{"/api/customer/*/resetPassword", "/api/customer/{customerId}/resetPassword", true},
	}
	for _, c := range cases {
		if got := matchPath(c.pattern, c.path); got != c.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// The Backoffice policy is the case lodog ships; check it against the document.
func TestBackofficePolicy(t *testing.T) {
	p, err := LoadFile("../../cases/backoffice/spec/policy.auto.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := spec.LoadFile("../../cases/backoffice/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range doc.Operations {
		d := p.Decide(op.Method, op.Path)
		if op.Method != "GET" && d.Action == Allow {
			t.Errorf("%s %s is allowed: a write must not pass without approval", op.Method, op.Path)
		}
		if strings.HasPrefix(op.Path, "/mgmt/internal/") && d.Action != Hide {
			t.Errorf("%s %s = %s: anonymous internal endpoints are not offered", op.Method, op.Path, d.Action)
		}
	}
	for _, c := range []struct {
		method, path string
		want         Action
	}{
		{"GET", "/api/customer/{customerId}/resetPassword", Hide},
		{"GET", "/api/images/TestDelete", Hide},
		{"GET", "/api/payment/transactions", Allow},
		{"POST", "/api/payment/transaction/withdraw/reject", Approval},
		{"POST", "/api/promotion/manualBonus/issue", Approval},
	} {
		if got := p.Decide(c.method, c.path); got.Action != c.want {
			t.Errorf("%s %s = %+v, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestARiskIsAnActionOrAnActionWithAttributes(t *testing.T) {
	p, err := parse(t, `
risks:
  read: allow
  destructive: {action: approval, never_auto_approve: true}
default: destructive
rules:
  - {match: "GET /**", risk: read}
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Decide("POST", "/api/x"); got.Action != Approval || !got.NeverAutoApprove {
		t.Fatalf("POST = %+v: the attribute of the matched risk comes with the decision", got)
	}
	if got := p.Decide("GET", "/api/x"); got.Action != Allow || got.NeverAutoApprove {
		t.Fatalf("GET = %+v: a risk given as its action alone has no attributes", got)
	}
	if w := p.Warnings(); len(w) != 0 {
		t.Fatalf("warnings = %v, want none: a risk is never auto-approved", w)
	}
}

func TestAPolicyWithNoNeverAutoApproveRiskIsWarnedAbout(t *testing.T) {
	p, err := parse(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if w := p.Warnings(); len(w) != 1 || !strings.Contains(w[0], "never_auto_approve") {
		t.Fatalf("warnings = %v, want one about never_auto_approve", w)
	}
}

func TestAPolicyWritesBackAsItReads(t *testing.T) {
	in := Policy{Risks: map[string]Risk{"read": {Action: Allow}, "gone": {Action: Approval, NeverAutoApprove: true}}, Default: "gone"}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "read: allow") {
		t.Fatalf("policy = %s: a risk with no attributes is written as its action", data)
	}
	out, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(out.Risks) != fmt.Sprint(in.Risks) {
		t.Fatalf("risks = %v, want %v", out.Risks, in.Risks)
	}
}

// Protection follows the attribute, not the name: the Backoffice policy with its
// never-auto-approved level renamed still holds the same calls for a person.
func TestTheBackofficeProtectionSurvivesRenamingItsRisk(t *testing.T) {
	data, err := os.ReadFile("../../cases/backoffice/spec/policy.auto.yaml")
	if err != nil {
		t.Fatal(err)
	}
	original, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := Parse([]byte(strings.ReplaceAll(string(data), "irreversible", "destructive")))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := spec.LoadFile("../../cases/backoffice/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, op := range doc.Operations {
		a, b := original.Decide(op.Method, op.Path), renamed.Decide(op.Method, op.Path)
		if a.NeverAutoApprove != b.NeverAutoApprove || a.Action != b.Action {
			t.Errorf("%s %s: %+v renamed to %+v", op.Method, op.Path, a, b)
		}
		if b.NeverAutoApprove {
			held++
		}
	}
	if held == 0 {
		t.Fatal("no Backoffice operation is never auto-approved; the fixture lost its protection")
	}
	if got := renamed.Decide("POST", "/api/payment/transaction/withdraw/complete"); got.Risk != "destructive" || !got.NeverAutoApprove {
		t.Fatalf("completing a withdrawal = %+v, want the renamed risk, never auto-approved", got)
	}
}
