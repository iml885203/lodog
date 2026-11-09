package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/policy"

	"github.com/iml885203/lodog/internal/spec"
)

// fakeModel answers each prompt by describing the operations it lists.
type fakeModel struct {
	mu      sync.Mutex
	prompts []string
	sources []string
	answer  func(prompt string) string
}

func (f *fakeModel) Complete(_ context.Context, prompt, sourceDir string) (string, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.sources = append(f.sources, sourceDir)
	f.mu.Unlock()
	return f.answer(prompt), nil
}

var testDoc = &spec.Document{Title: "T", Operations: []spec.Operation{
	{
		Method: "GET", Path: "/api/payment/transactions", Tag: "Payment",
		Params: []spec.Param{{Name: "TransactionType", In: "query"}, {Name: "PageSize", In: "query", Required: true}},
	},
	{Method: "POST", Path: "/api/payment/transaction/withdraw/reject", Tag: "Payment"},
	{
		Method: "GET", Path: "/api/customer/{customerId}", Tag: "Customer",
		Params: []spec.Param{{Name: "customerId", In: "path", Required: true}},
	},
}}

func describeAll(confidence string) func(string) string {
	return func(prompt string) string {
		var ops []Described
		for _, op := range testDoc.Operations {
			if strings.Contains(prompt, op.Method+" "+op.Path+"  [tag") {
				ops = append(ops, Described{
					Method: op.Method, Path: op.Path, Summary: "does " + op.Path, Risk: "read",
					WhenToUse: "when needed", Confidence: confidence,
					Params: map[string]string{"TransactionType": "1 = deposit, 2 = withdrawal", "Ghost": "not a param"},
				})
			}
		}
		b, _ := json.Marshal(map[string]any{"operations": ops})
		return "Here you go:\n```json\n" + string(b) + "\n```"
	}
}

func TestEveryOperationGetsAnActionAndKnownParamsTheirOwn(t *testing.T) {
	m := &fakeModel{answer: describeAll("high")}
	res, err := Run(context.Background(), testDoc, m, Options{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 0 || len(res.Review) != 0 {
		t.Fatalf("missing %v review %v", res.Missing, res.Review)
	}
	var targets []string
	for _, a := range res.Overlay.Actions {
		targets = append(targets, a.Target)
	}
	joined := strings.Join(targets, "\n")
	if !strings.Contains(joined, "$.paths['/api/payment/transactions'].get.parameters[?(@.name=='TransactionType')]") {
		t.Errorf("no action for TransactionType in\n%s", joined)
	}
	if strings.Contains(joined, "Ghost") {
		t.Error("a parameter the operation does not have got an action")
	}
	if len(m.prompts) != 2 {
		t.Errorf("prompts = %d, want one per tag", len(m.prompts))
	}
}

func TestLowConfidenceIsMarkedForReview(t *testing.T) {
	res, err := Run(context.Background(), testDoc, &fakeModel{answer: describeAll("low")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Review) != 3 {
		t.Fatalf("review = %v", res.Review)
	}
	agent := res.Overlay.Actions[0].Update["x-agent"].(map[string]any)
	if agent["needsReview"] != true {
		t.Fatalf("x-agent = %v", agent)
	}
}

func TestAnOperationTheModelSkippedIsReportedMissing(t *testing.T) {
	m := &fakeModel{answer: func(string) string { return `{"operations":[]}` }}
	res, err := Run(context.Background(), testDoc, m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 3 || len(res.Overlay.Actions) != 0 {
		t.Fatalf("missing %v actions %d", res.Missing, len(res.Overlay.Actions))
	}
}

func TestTheSourceIsOfferedOnlyWhenGiven(t *testing.T) {
	m := &fakeModel{answer: describeAll("high")}
	if _, err := Run(context.Background(), testDoc, m, Options{}); err != nil {
		t.Fatal(err)
	}
	if m.sources[0] != "" || strings.Contains(m.prompts[0], "source code is available") {
		t.Fatal("swagger-only run told the model it has source")
	}
	m = &fakeModel{answer: describeAll("high")}
	if _, err := Run(context.Background(), testDoc, m, Options{SourceDir: "/src"}); err != nil {
		t.Fatal(err)
	}
	if m.sources[0] != "/src" || !strings.Contains(m.prompts[0], "source code is available") {
		t.Fatal("source run did not pass the source")
	}
}

func TestAnUnknownRiskIsDropped(t *testing.T) {
	m := &fakeModel{answer: func(string) string {
		return `{"operations":[{"method":"GET","path":"/api/customer/{customerId}","risk":"spicy","confidence":"high"}]}`
	}}
	res, err := Run(context.Background(), &spec.Document{Operations: testDoc.Operations[2:]}, m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	agent := res.Overlay.Actions[0].Update["x-agent"].(map[string]any)
	if _, ok := agent["risk"]; ok {
		t.Fatalf("x-agent = %v: a risk outside the vocabulary must not be written", agent)
	}
}

func TestABatchThatFailsFailsTheRun(t *testing.T) {
	m := &failing{}
	if _, err := Run(context.Background(), testDoc, m, Options{}); err == nil {
		t.Fatal("want an error: a half-drafted overlay must not look complete")
	}
}

type failing struct{}

func (failing) Complete(context.Context, string, string) (string, error) {
	return "", errors.New("rate limited")
}

func TestBatchesKeepATagTogetherAndSplitLargeOnes(t *testing.T) {
	var ops []spec.Operation
	for range 5 {
		ops = append(ops, spec.Operation{Tag: "A"})
	}
	ops = append(ops, spec.Operation{Tag: "B"})
	b := Batches(ops, 2)
	if len(b) != 4 || len(b[3]) != 1 || b[3][0].Tag != "B" {
		t.Fatalf("batches = %v", b)
	}
}

func TestThePromptShowsEnumsAndRequiredParams(t *testing.T) {
	p := Prompt([]spec.Operation{{Method: "GET", Path: "/x", Params: []spec.Param{
		{Name: "Status", In: "query", Required: true, Schema: map[string]any{"type": "integer", "enum": []any{0, 1}, "x-enumNames": []any{"Active", "Inactive"}}},
	}}}, false, nil)
	for _, want := range []string{"param Status (query, required)", "enum {0 = Active, 1 = Inactive}"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestErrorStatusesBecomeResponsesAndOthersAreDropped(t *testing.T) {
	m := &fakeModel{answer: func(string) string {
		return `{"operations":[{"method":"GET","path":"/api/customer/{customerId}","confidence":"high",
			"errors":{"404":"no such customer; search first","200":"fine","4xx":"vague","500":""}}]}`
	}}
	res, err := Run(context.Background(), &spec.Document{Operations: testDoc.Operations[2:]}, m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	responses, _ := res.Overlay.Actions[0].Update["responses"].(map[string]any)
	if len(responses) != 1 || responses["404"] == nil {
		t.Fatalf("responses = %v: keep only a described 4xx/5xx status", responses)
	}
}

func TestAMalformedAnswerIsAskedOnceMore(t *testing.T) {
	calls := 0
	m := &fakeModel{answer: func(p string) string {
		calls++
		if calls == 1 {
			return `{"operations":[{"method":"GET",:}]}`
		}
		return describeAll("high")(p)
	}}
	res, err := Run(context.Background(), &spec.Document{Operations: testDoc.Operations[2:]}, m, Options{Concurrency: 1})
	if err != nil || len(res.Missing) != 0 || calls != 2 {
		t.Fatalf("err %v missing %v calls %d", err, res.Missing, calls)
	}
}

// A name used in two places is described and targeted by its place, so the
// description lands on the parameter the model meant.
func TestAClashingParameterIsTargetedByItsPlace(t *testing.T) {
	doc, err := spec.Parse([]byte(`{"openapi":"3.0.3","paths":{"/x/{id}":{"get":{"parameters":[
		{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
		{"name":"id","in":"header","schema":{"type":"string"}},
		{"name":"limit","in":"query","schema":{"type":"integer"}}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res := build(doc, []Described{{Method: "GET", Path: "/x/{id}", Params: map[string]string{
		"header.id": "tenant id", "path.id": "ticket id", "limit": "page size",
	}}}, "t", DefaultRisks())
	targets := map[string]string{}
	for _, a := range res.Overlay.Actions {
		if d, ok := a.Update["description"].(string); ok {
			targets[d] = a.Target
		}
	}
	want := map[string]string{
		"tenant id": "$.paths['/x/{id}'].get.parameters[?(@.name=='id' && @.in=='header')]",
		"ticket id": "$.paths['/x/{id}'].get.parameters[?(@.name=='id' && @.in=='path')]",
		"page size": "$.paths['/x/{id}'].get.parameters[?(@.name=='limit')]",
	}
	for d, target := range want {
		if targets[d] != target {
			t.Errorf("%q target = %q, want %q", d, targets[d], target)
		}
	}
}

func TestTheRiskVocabularyComesFromThePolicy(t *testing.T) {
	if got := RiskVocabulary(nil); !slices.Equal(got, DefaultRisks()) {
		t.Errorf("no policy: %v", got)
	}
	p := &policy.Policy{Risks: map[string]policy.Risk{"read": {Action: policy.Allow}, "write": {Action: policy.Approval}, "zzz": {Action: policy.Hide}, "bulk": {Action: policy.Approval}}}
	if got, want := RiskVocabulary(p), []string{"read", "write", "bulk", "zzz"}; !slices.Equal(got, want) {
		t.Errorf("policy: got %v want %v", got, want)
	}
}

func TestThePromptOffersOnlyTheGivenRisks(t *testing.T) {
	p := Prompt([]spec.Operation{{Method: "GET", Path: "/x"}}, false, []string{"read", "bulk"})
	for _, want := range []string{"one of read, bulk", "read = changes nothing"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "credential") {
		t.Error("prompt offers a risk outside the vocabulary")
	}
}

func TestThePromptCarriesNoDomainWords(t *testing.T) {
	p := strings.ToLower(Prompt(nil, true, nil))
	for _, w := range []string{"deposit", "withdraw", "customer", "player", "bonus"} {
		if strings.Contains(p, w) {
			t.Errorf("prompt names domain word %q", w)
		}
	}
}

func TestAnOperationLeftOutOfAnAnswerIsAskedForAgain(t *testing.T) {
	const dropped = "POST /api/payment/transaction/withdraw/reject"
	var mu sync.Mutex
	asked := 0
	m := &fakeModel{answer: func(p string) string {
		out := describeAll("high")(p)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(p, dropped) {
			asked++
			if asked == 1 { // first time: answer for its batch without it
				return strings.NewReplacer(`"path":"/api/payment/transaction/withdraw/reject"`, `"path":"/api/other"`).Replace(out)
			}
		}
		return out
	}}
	res, err := Run(context.Background(), testDoc, m, Options{Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 0 || asked != 2 {
		t.Fatalf("missing %v, asked for the dropped operation %d times", res.Missing, asked)
	}
}

func TestAHighConfidenceAnswerThatAdmitsAGuessIsFlaggedForReview(t *testing.T) {
	doc := &spec.Document{Operations: []spec.Operation{{Method: "GET", Path: "/x", Tag: "T"}}}
	res := build(doc, []Described{{
		Method: "GET", Path: "/x", Summary: "s", Confidence: "high",
		Errors: map[string]string{"403": "[猜] no permission"},
	}}, "t", DefaultRisks())
	if len(res.Review) != 1 {
		t.Fatalf("review = %v, want the guess flagged", res.Review)
	}
}

func TestTheNamesOfABodyFieldsCodesReachTheModel(t *testing.T) {
	body := map[string]any{"type": "object", "properties": map[string]any{
		"channel": map[string]any{"type": "integer", "enum": []any{1.0, 2.0}, "x-enumNames": []any{"Email", "LiveChat"}},
	}}
	p := Prompt([]spec.Operation{{Method: "POST", Path: "/x", BodyContentType: "application/json", Body: body}}, false, nil)
	if !strings.Contains(p, "channel:integer enum {1 = Email, 2 = LiveChat}") {
		t.Errorf("prompt does not give the model the code names:\n%s", p)
	}
}
