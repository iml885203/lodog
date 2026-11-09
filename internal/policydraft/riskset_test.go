package policydraft

import (
	"slices"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// A system that calls its strictest level "destructive" and has no separate
// money level: classes map onto its names, and protection follows the
// attribute.
const ownRisks = `
default: change
risks:
  - {name: destructive, action: approval, never_auto_approve: true, classes: [irreversible], means: gone once it runs}
  - {name: change, action: approval, classes: [write, money], means: edits records}
  - {name: secret, action: hide, classes: [credential, internal, test], means: not for agents}
  - {name: lookup, action: allow, classes: [read], means: looks things up}
`

func TestADraftUsesTheSystemsOwnRiskNames(t *testing.T) {
	rs, err := ParseRiskSet([]byte(ownRisks))
	if err != nil {
		t.Fatal(err)
	}
	doc := &spec.Document{Operations: []spec.Operation{
		op("POST", "/api/payment/transaction/withdraw/complete"),
		op("POST", "/api/promotion/depositBonus"),
		op("GET", "/api/customer"),
		op("POST", "/api/banners"),
	}}
	model := &fakeModel{risk: func(question) (string, bool) { return "change", true }}
	cs, err := Classify(t.Context(), doc, model, readiness.DefaultSignals(), rs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Draft{Source: "d", Model: "m", Risks: rs, Classifications: cs}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p := Draft{Risks: rs, Classifications: cs}.Policy()
	for path, want := range map[string]string{
		"/api/payment/transaction/withdraw/complete": "destructive",
		"/api/promotion/depositBonus":                "change",
		"/api/banners":                               "change",
	} {
		if d := p.Decide("POST", path); d.Risk != want {
			t.Errorf("POST %s = %+v, want %s", path, d, want)
		}
	}
	if d := p.Decide("POST", "/api/payment/transaction/withdraw/complete"); !d.NeverAutoApprove {
		t.Errorf("completing a withdrawal = %+v: the set's never_auto_approve reaches the policy", d)
	}
	if d := p.Decide("GET", "/api/customer"); d.Risk != "lookup" {
		t.Errorf("GET /api/customer = %+v, want lookup", d)
	}
	if !strings.Contains(string(data), "never_auto_approve: true") || strings.Contains(string(data), "irreversible:") {
		t.Fatalf("the draft writes the set's names and attributes:\n%s", data)
	}
	asked, err := prompt(readiness.DefaultSignals(), rs, doc.Operations[3:])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "- destructive: gone once it runs") || strings.Contains(asked, "- lookup:") || !strings.Contains(asked, "still choose change") {
		t.Fatalf("the prompt offers the set's names, never its read risk:\n%s", asked)
	}
}

func TestADeclaredRiskMapsThroughItsClass(t *testing.T) {
	rs, err := ParseRiskSet([]byte(ownRisks))
	if err != nil {
		t.Fatal(err)
	}
	declared := op("POST", "/api/banners")
	declared.AgentRisk = ClassIrreversible
	if c, ok := classify(readiness.DefaultSignals(), rs, declared); !ok || c.Risk != "destructive" {
		t.Fatalf("x-agent.risk irreversible = %+v, want destructive", c)
	}
}

func TestTheDefaultRiskSetNeverAutoApprovesWhatCannotBeUndone(t *testing.T) {
	p := Draft{Risks: std}.Policy()
	if w := p.Warnings(); len(w) != 0 {
		t.Fatalf("warnings = %v", w)
	}
	if !p.Risks[std.riskOf(ClassIrreversible)].NeverAutoApprove {
		t.Fatal("the risk that takes the irreversible class must be never_auto_approve")
	}
}

func TestABrokenRiskSetIsRefused(t *testing.T) {
	base := func(replace, with string) string { return strings.Replace(ownRisks, replace, with, 1) }
	for name, c := range map[string]struct{ yaml, want string }{
		"class missing":     {base("classes: [write, money]", "classes: [write]"), `class "money" goes to no risk`},
		"class twice":       {base("classes: [write, money]", "classes: [write, money, read]"), `class "read" goes to both`},
		"unknown class":     {base("classes: [write, money]", "classes: [write, money, payout]"), `class "payout" is not one of`},
		"read with another": {strings.Replace(base("classes: [read]", "classes: [read, test]"), "internal, test]", "internal]", 1), "takes no other class"},
		"no means":          {base("means: edits records", "means: \"\""), "means is required"},
		"duplicate name":    {base("name: secret", "name: change"), `risk "change" is listed twice`},
		"allowed default":   {base("default: change", "default: lookup"), "fail closed"},
		"allowed yet held":  {base("name: lookup, action: allow", "name: lookup, action: allow, never_auto_approve: true"), "never_auto_approve with allow"},
		"unknown field":     {base("default: change", "default: change\nwarn: true"), "field warn not found"},
	} {
		_, err := ParseRiskSet([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestOverlayRisksAreTheSetsNamesForTheDeclarableClasses(t *testing.T) {
	got := DefaultRiskSet().OverlayRisks()
	if want := []string{"read", "write", "money", "irreversible", "credential"}; !slices.Equal(got, want) {
		t.Errorf("default overlay risks = %v, want %v", got, want)
	}
	rs, err := ParseRiskSet([]byte(ownRisks))
	if err != nil {
		t.Fatal(err)
	}
	// The system's own names win; a class two risks share maps to one name.
	if got, want := rs.OverlayRisks(), []string{"lookup", "change", "change", "destructive", "secret"}; !slices.Equal(got, want) {
		t.Errorf("own overlay risks = %v, want %v", got, want)
	}
}
