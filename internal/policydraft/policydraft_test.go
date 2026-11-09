package policydraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// std is lodog's default risk set, whose risk names equal the classes.
var std = DefaultRiskSet()

func op(method, path string) spec.Operation {
	return spec.Operation{Method: method, Path: path}
}

// backofficeSignals is the Backoffice case's vocabulary; the heuristics tests below
// are written for Backoffice's paths and its money words.
func backofficeSignals(t *testing.T) readiness.Signals {
	t.Helper()
	sig, err := readiness.LoadSignals("../../cases/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestHeuristicsSettleWhatTheDocumentMakesClear(t *testing.T) {
	declaredRead := op("POST", "/api/banners")
	declaredRead.AgentRisk = ClassRead
	declaredMoney := op("POST", "/api/banners")
	declaredMoney.AgentRisk = ClassMoney
	declaredIrreversible := op("POST", "/api/banners")
	declaredIrreversible.AgentRisk = ClassIrreversible
	cases := []struct {
		op   spec.Operation
		want string // "" = heuristics cannot settle it
	}{
		{op("POST", "/api/payment/transaction/withdraw/complete"), ClassIrreversible},
		{op("POST", "/api/payment/transaction/deposit/rollback"), ClassIrreversible},
		{op("POST", "/api/promotion/manualBonus/issue"), ClassIrreversible},
		{op("GET", "/api/promotion/manualBonus/issue"), ClassIrreversible}, // probably a report, but it names money and an action: held, not read
		{op("POST", "/api/promotion/rebate/batch/approve"), ClassIrreversible},
		{op("POST", "/api/transfer/manual"), ClassIrreversible},
		{op("POST", "/api/wallet/payout"), ClassIrreversible},
		{op("POST", "/api/affiliate/payout/{id}"), ClassIrreversible},
		{op("DELETE", "/api/role/{id}"), ClassIrreversible},
		{op("DELETE", "/api/promotion/rebate/{id}"), ClassIrreversible},
		{op("POST", "/api/customer/{customerId}/purgeHistory"), ClassIrreversible},
		{op("POST", "/api/account/{id}/close"), ClassIrreversible},
		{op("PUT", "/api/customer/{customerId}/removeTag"), ClassIrreversible},
		{op("DELETE", "/api/customer/{customerId}/password"), ClassCredential},
		{op("DELETE", "/api/internal/cache"), ClassInternal},
		{op("POST", "/api/payment/transaction/remark/save"), ClassMoney},
		{op("POST", "/api/promotion/depositBonus"), ClassMoney},
		{op("PATCH", "/api/payment/payment-method"), ClassMoney},
		{op("POST", "/api/referrals/batch/approve"), ""},
		{op("POST", "/api/report/closedAccounts"), ""},
		{op("POST", "/api/payment/transaction/deleted/export"), ClassMoney},
		{declaredIrreversible, ClassIrreversible},
		{op("GET", "/api/customer/{customerId}/resetPassword"), ClassCredential},
		{op("POST", "/api/customer/{customerId}/resetOtp"), ClassCredential},
		{op("POST", "/api/operator/unlock"), ClassCredential},
		{op("GET", "/api/images/TestDelete"), ClassTest},
		{op("GET", "/api/report/test-voucher"), ClassTest},
		{op("GET", "/api/internalSystem/{customerId}"), ClassInternal},
		{op("POST", "/mgmt/internal/report/merchant/send"), ClassInternal},
		{op("GET", "/api/customer/{customerId}"), ClassRead},
		{op("GET", "/api/payment/transactions"), ""}, // names money: left to the model
		{op("GET", "/api/testimonials"), ClassRead},
		{declaredMoney, ClassMoney},
		{declaredRead, ""},
		{op("POST", "/api/banners"), ""},
	}
	for _, c := range cases {
		got, ok := classify(backofficeSignals(t), std, c.op)
		if !ok {
			got.Risk = ""
		}
		if got.Risk != c.want {
			t.Errorf("%s %s = %q (%s), want %q", c.op.Method, c.op.Path, got.Risk, got.Why, c.want)
		}
		if ok && (got.Source != ByHeuristic || !strings.HasPrefix(got.Why, "heuristic: ")) {
			t.Errorf("%s %s: source %s, why %q", c.op.Method, c.op.Path, got.Source, got.Why)
		}
	}
}

// fakeModel answers every operation of a batch with risk, reading the ids
// back from the prompt the way the model would.
type fakeModel struct {
	risk      func(question) (string, bool)
	batches   [][]question
	malformed string
}

func (f *fakeModel) Ask(_ context.Context, prompt string) (string, error) {
	if f.malformed != "" {
		return f.malformed, nil
	}
	var qs []question
	if err := json.Unmarshal([]byte(prompt[strings.LastIndex(prompt, "Operations:\n")+len("Operations:\n"):]), &qs); err != nil {
		return "", err
	}
	f.batches = append(f.batches, qs)
	answers := make([]map[string]any, len(qs))
	for i, q := range qs {
		risk, confident := f.risk(q)
		answers[i] = map[string]any{"id": q.ID, "risk": risk, "reason": "because " + q.Path, "confident": confident}
	}
	data, err := json.Marshal(answers)
	return "```json\n" + string(data) + "\n```", err
}

func writes(question) (string, bool) { return ClassWrite, true }

func TestUndecidedOperationsGoToTheModelInBatches(t *testing.T) {
	doc := &spec.Document{}
	for i := range 65 {
		doc.Operations = append(doc.Operations, op("POST", fmt.Sprintf("/api/banners/%d", i)))
	}
	doc.Operations = append(doc.Operations, op("GET", "/api/banners"))
	model := &fakeModel{risk: writes}
	cs, err := Classify(t.Context(), doc, model, readiness.DefaultSignals(), std)
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{}
	for _, b := range model.batches {
		sizes = append(sizes, len(b))
	}
	if fmt.Sprint(sizes) != "[30 30 5]" {
		t.Fatalf("batch sizes = %v, want [30 30 5]", sizes)
	}
	if len(cs) != 66 {
		t.Fatalf("%d classifications, want 66", len(cs))
	}
	s := Draft{Risks: std, Classifications: cs}.Summary()
	if s.ByModel != 65 || s.ByHeuristic != 1 || s.PerRisk[ClassRead] != 1 || s.NeedsReview != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if q := model.batches[0][0]; q.Signal != "POST changes state" || q.ID != 1 {
		t.Fatalf("question = %+v", q)
	}
}

func TestTheModelIsNotAskedWhenHeuristicsSettleEverything(t *testing.T) {
	doc := &spec.Document{Operations: []spec.Operation{op("GET", "/api/customer"), op("POST", "/api/transfer/manual")}}
	if _, err := Classify(t.Context(), doc, nil, readiness.DefaultSignals(), std); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnsureAnswerIsMarkedForReview(t *testing.T) {
	doc := &spec.Document{Operations: []spec.Operation{op("POST", "/api/report/performance/casino"), op("POST", "/api/role")}}
	model := &fakeModel{risk: func(q question) (string, bool) { return ClassWrite, q.Path == "/api/role" }}
	cs, err := Classify(t.Context(), doc, model, readiness.DefaultSignals(), std)
	if err != nil {
		t.Fatal(err)
	}
	d := Draft{Risks: std, Classifications: cs}
	for _, c := range cs {
		unsure := c.Path == "/api/report/performance/casino"
		if c.NeedsReview != unsure || strings.Contains(c.Why, "needs review") != unsure || c.Source != ByModel {
			t.Errorf("%+v", c)
		}
	}
	if d.Summary().NeedsReview != 1 {
		t.Fatalf("summary = %+v", d.Summary())
	}
	if first := d.Policy().Rules[0]; first.Match != "POST /api/report/performance/casino" {
		t.Fatalf("a rule that needs review must head its group, got %s first", first.Match)
	}
}

func TestTheModelMayChooseIrreversible(t *testing.T) {
	doc := &spec.Document{Operations: []spec.Operation{op("POST", "/api/voucher/campaign/{id}/expire"), op("POST", "/api/banners")}}
	model := &fakeModel{risk: func(q question) (string, bool) {
		if q.Path == "/api/banners" {
			return ClassWrite, true
		}
		return ClassIrreversible, true
	}}
	cs, err := Classify(t.Context(), doc, model, readiness.DefaultSignals(), std)
	if err != nil {
		t.Fatal(err)
	}
	rules := Draft{Risks: std, Classifications: cs}.Policy().Rules
	if rules[0].Match != "POST /api/voucher/campaign/{id}/expire" || rules[0].Risk != ClassIrreversible || !strings.HasPrefix(rules[0].Why, "model: ") {
		t.Fatalf("rules = %+v: an irreversible answer is kept and heads the draft", rules)
	}
}

func TestThePromptOffersIrreversibleAndPrefersItOverMoney(t *testing.T) {
	p, err := prompt(readiness.DefaultSignals(), std, []spec.Operation{op("POST", "/api/a")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- irreversible: cannot be undone", "- money: ", "choose the one listed first", "still choose write"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Index(p, "- irreversible: ") > strings.Index(p, "- money: ") {
		t.Error("irreversible must be listed before money: the model chooses the earlier of two that fit")
	}
	if strings.Contains(p, "- read: ") {
		t.Error("the prompt must not offer read")
	}
}

func TestAMalformedAnswerStopsTheDraft(t *testing.T) {
	two := []spec.Operation{op("POST", "/api/a"), op("POST", "/api/b")}
	for name, text := range map[string]string{
		"not json":        "I think these are writes.",
		"broken json":     `[{"id": 1, "risk": "write"`,
		"missing id":      `[{"id": 1, "risk": "write", "reason": "r", "confident": true}]`,
		"unknown id":      `[{"id": 1, "risk": "write", "reason": "r", "confident": true}, {"id": 3, "risk": "write", "reason": "r", "confident": true}]`,
		"duplicate id":    `[{"id": 1, "risk": "write", "reason": "r", "confident": true}, {"id": 1, "risk": "write", "reason": "r", "confident": true}]`,
		"read":            `[{"id": 1, "risk": "read", "reason": "r", "confident": true}, {"id": 2, "risk": "write", "reason": "r", "confident": true}]`,
		"unknown risk":    `[{"id": 1, "risk": "danger", "reason": "r", "confident": true}, {"id": 2, "risk": "write", "reason": "r", "confident": true}]`,
		"no reason":       `[{"id": 1, "risk": "write", "reason": " ", "confident": true}, {"id": 2, "risk": "write", "reason": "r", "confident": true}]`,
		"confident unset": `[{"id": 1, "risk": "write", "reason": "r"}, {"id": 2, "risk": "write", "reason": "r", "confident": true}]`,
	} {
		_, err := Classify(t.Context(), &spec.Document{Operations: two}, &fakeModel{malformed: text}, readiness.DefaultSignals(), std)
		if !errors.Is(err, ErrMalformedAnswer) {
			t.Errorf("%s: err = %v, want ErrMalformedAnswer", name, err)
		}
	}
}

func TestAModelErrorStopsTheDraft(t *testing.T) {
	_, err := Classify(t.Context(), &spec.Document{Operations: []spec.Operation{op("POST", "/api/a")}}, failingModel{}, readiness.DefaultSignals(), std)
	if !errors.Is(err, errModelDown) {
		t.Fatalf("err = %v", err)
	}
}

var errModelDown = errors.New("model down")

type failingModel struct{}

func (failingModel) Ask(context.Context, string) (string, error) { return "", errModelDown }

func TestTheBackofficeDraftLoadsAndRulesEveryMutatingOperation(t *testing.T) {
	doc, err := spec.LoadFile("../../cases/backoffice/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{risk: writes}
	cs, err := Classify(t.Context(), doc, model, readiness.DefaultSignals(), std)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Draft{Source: "openapi.json", Model: "m", Risks: std, Classifications: cs}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	checkDraftedPolicy(t, doc, p)
	for _, q := range model.batches {
		for _, x := range q {
			if x.Method == "DELETE" {
				t.Errorf("DELETE %s went to the model; a DELETE is irreversible by heuristic", x.Path)
			}
		}
	}
	if d := p.Decide("POST", "/api/payment/transaction/withdraw/complete"); d.Risk != ClassIrreversible {
		t.Errorf("completing a withdrawal = %+v, want irreversible", d)
	}
	if !strings.Contains(string(data), "# irreversible: ") || !strings.Contains(string(data), "# money: ") || !strings.Contains(string(data), "model m") {
		t.Fatalf("the draft must head each risk group and name its model:\n%s", data[:min(len(data), 1500)])
	}
}

func TestTheCommittedBackofficeDraftRulesEveryMutatingOperation(t *testing.T) {
	doc, err := spec.LoadFile("../../cases/backoffice/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.LoadFile("../../cases/backoffice/spec/policy.auto.yaml")
	if err != nil {
		t.Fatal(err)
	}
	checkDraftedPolicy(t, doc, p)
}

func checkDraftedPolicy(t *testing.T, doc *spec.Document, p *policy.Policy) {
	t.Helper()
	if len(p.Rules) != len(doc.Operations) {
		t.Errorf("%d rules for %d operations: a draft has one rule per operation", len(p.Rules), len(doc.Operations))
	}
	for _, o := range doc.Operations {
		d := p.Decide(o.Method, o.Path)
		if backofficeSignals(t).Mutating(o) && !d.Explicit() {
			t.Errorf("%s %s may change state but falls to the default", o.Method, o.Path)
		}
	}
	for _, trap := range []string{"/api/customer/{customerId}/resetPassword", "/api/images/TestDelete"} {
		d := p.Decide("GET", trap)
		if d.Risk == ClassRead || d.Action == policy.Allow || !d.Explicit() {
			t.Errorf("GET %s = %+v: a GET that changes state must not be allowed", trap, d)
		}
	}
	for _, r := range p.Rules {
		if strings.Contains(r.Match, "*") || r.Why == "" {
			t.Errorf("rule %+v: a drafted rule names one operation and says why", r)
		}
	}
}

func TestTheDraftDefaultsFailClosed(t *testing.T) {
	p := Draft{Risks: std}.Policy()
	if a := p.Risks[p.Default].Action; a != policy.Approval {
		t.Fatalf("default %s maps to %s, want approval", p.Default, a)
	}
	want := map[string]policy.Risk{
		"read": {Action: policy.Allow}, "write": {Action: policy.Approval}, "money": {Action: policy.Approval},
		"irreversible": {Action: policy.Approval, NeverAutoApprove: true},
		"credential":   {Action: policy.Hide}, "internal": {Action: policy.Hide}, "test": {Action: policy.Hide},
	}
	if fmt.Sprint(p.Risks) != fmt.Sprint(want) {
		t.Fatalf("risks = %v, want %v", p.Risks, want)
	}
}

// The policy and a benchmark's answers must be produced independently, or the
// benchmark would grade the policy against itself. Nothing the draft builds on
// may reach the benchmark's packages, which read the answers.
func TestTheDraftCannotReadBenchmarkAnswers(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.Contains(dep, "/lodog/internal/bench") {
			t.Errorf("policydraft depends on %s", dep)
		}
	}
}

// A system that is not a casino: the generic vocabulary alone settles it.
func TestGenericHeuristicsSettleAnInvoicingAPIWithoutAnyCaseWords(t *testing.T) {
	cases := []struct {
		op   spec.Operation
		want string
	}{
		{op("GET", "/invoices"), ""},                   // money in the path: the model decides, never a heuristic read
		{op("GET", "/invoices/{id}/send"), ClassMoney}, // an action GET on money is never read
		{op("POST", "/invoices/{id}/refund"), ClassIrreversible},
		{op("POST", "/billing/charge"), ClassMoney},
		{op("DELETE", "/customers/{id}"), ClassIrreversible},
		{op("GET", "/blocks"), ClassRead},
		{op("GET", "/promotions"), ClassRead},
		{op("POST", "/promotions"), ""},
	}
	for _, c := range cases {
		got, ok := classify(readiness.DefaultSignals(), std, c.op)
		if c.want == "" && ok || c.want != "" && (!ok || got.Risk != c.want) {
			t.Errorf("%s %s = %+v (settled %v), want %q", c.op.Method, c.op.Path, got, ok, c.want)
		}
	}
}

// The score leaves refund, transfer, lock… out of its action words (a GET with
// one is often harmless); a policy must not: a GET it wrongly drafts as read
// is called without approval.
func TestAGetThatMayMoveMoneyIsNeverDraftedAsReadByHeuristics(t *testing.T) {
	for _, path := range []string{"/payments/{id}/refund", "/wallet/transfer", "/accounts/{id}/lock", "/jobs/trigger", "/clients/{id}/generate-token", "/payouts/{id}", "/orders/{id}/sync"} {
		got, ok := classify(readiness.DefaultSignals(), std, op("GET", path))
		if ok && got.Risk == ClassRead {
			t.Errorf("GET %s drafted as read by %s", path, got.Why)
		}
	}
	// A GET with none of those signals is still read.
	if got, ok := classify(readiness.DefaultSignals(), std, op("GET", "/clients/{id}/sessions")); !ok || got.Risk != ClassRead {
		t.Errorf("a plain GET = %+v (%v), want read", got, ok)
	}
}

func getWith(path string, f func(*spec.Operation)) spec.Operation {
	o := op("GET", path)
	f(&o)
	return o
}

// Each way a GET can hide an action must keep it from a heuristic read.
func TestAGetThatHidesItsActionIsNotDraftedAsReadByHeuristics(t *testing.T) {
	cases := map[string]spec.Operation{
		"action in operationId":        getWith("/orders/{id}", func(o *spec.Operation) { o.OperationID = "approveOrder" }),
		"action in summary":            getWith("/orders/{id}", func(o *spec.Operation) { o.Summary = "Approve the order" }),
		"suspect word in summary":      getWith("/orders/{id}", func(o *spec.Operation) { o.Summary = "Refund the order" }),
		"action query parameter":       getWith("/orders/{id}", func(o *spec.Operation) { o.Params = []spec.Param{{Name: "action", In: "query"}} }),
		"command parameter":            getWith("/orders/{id}", func(o *spec.Operation) { o.Params = []spec.Param{{Name: "command", In: "query"}} }),
		"op parameter":                 getWith("/orders/{id}", func(o *spec.Operation) { o.Params = []spec.Param{{Name: "op", In: "query"}} }),
		"unreviewed overlay read":      getWith("/payments/{id}/refund", func(o *spec.Operation) { o.AgentRisk, o.NeedsReview = ClassRead, true }),
		"unreviewed read, path action": getWith("/users/{id}/resetPassword", func(o *spec.Operation) { o.AgentRisk, o.NeedsReview = ClassRead, true }),
	}
	for name, o := range cases {
		if got, ok := classify(readiness.DefaultSignals(), std, o); ok && got.Risk == ClassRead {
			t.Errorf("%s: drafted as read by %s", name, got.Why)
		}
	}
	reviewed := getWith("/payments/{id}/history", func(o *spec.Operation) { o.AgentRisk = ClassRead })
	if got, ok := classify(readiness.DefaultSignals(), std, reviewed); !ok || got.Risk != ClassRead {
		t.Errorf("a reviewed read = %+v (%v), want read", got, ok)
	}
	declaredHigher := getWith("/orders", func(o *spec.Operation) { o.AgentRisk, o.NeedsReview = ClassMoney, true })
	if got, ok := classify(readiness.DefaultSignals(), std, declaredHigher); !ok || got.Risk != ClassMoney {
		t.Errorf("an unreviewed higher risk = %+v (%v), want it kept", got, ok)
	}
}

func TestTheModelMayAnswerReadForAGetWithAReasonAndOnlyForAGet(t *testing.T) {
	get := getWith("/orders/{id}/history", func(o *spec.Operation) { o.Summary = "List refund history" })
	post := op("POST", "/api/report/performance")
	doc := &spec.Document{Operations: []spec.Operation{get, post}}
	read := func(conf bool) *fakeModel {
		return &fakeModel{risk: func(question) (string, bool) { return ClassRead, conf }}
	}
	if _, err := Classify(t.Context(), doc, read(true), readiness.DefaultSignals(), std); !errors.Is(err, ErrMalformedAnswer) {
		t.Fatalf("read for a POST must be refused, got %v", err)
	}
	doc.Operations = doc.Operations[:1]
	cs, err := Classify(t.Context(), doc, read(true), readiness.DefaultSignals(), std)
	if err != nil || cs[0].Risk != ClassRead || !strings.HasPrefix(cs[0].Why, "model: ") {
		t.Fatalf("a confident read for a GET = %+v, %v", cs, err)
	}
	// Not sure: held for approval and flagged, never read.
	cs, err = Classify(t.Context(), doc, read(false), readiness.DefaultSignals(), std)
	if err != nil || cs[0].Risk != ClassWrite || !cs[0].NeedsReview {
		t.Fatalf("an unsure read for a GET = %+v, %v", cs, err)
	}
}

// The score leaves out a preview of an action (#35); policy draft must not:
// the same Keycloak GETs are never drafted as read by heuristics, with or
// without a case that calls generate an action.
func TestKeycloakGenerateExampleGETsAreNotDraftedAsReadByHeuristics(t *testing.T) {
	withGenerate, err := readiness.NewSignals(readiness.Config{ActionWords: []string{"generate"}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/admin/realms/{realm}/clients/{client-uuid}/evaluate-scopes/generate-example-"
	for _, sig := range []readiness.Signals{readiness.DefaultSignals(), withGenerate} {
		for _, suffix := range []string{"access-token", "id-token", "saml-response", "userinfo"} {
			o := op("GET", base+suffix)
			if got, ok := classify(sig, std, o); ok && got.Risk == ClassRead {
				t.Errorf("GET %s drafted as read by %s", o.Path, got.Why)
			}
		}
	}
	if got := signal(withGenerate, op("GET", base+"userinfo")); !strings.Contains(got, "(generate)") {
		t.Errorf("the model is told %q: a case's action word must still be named", got)
	}
}
