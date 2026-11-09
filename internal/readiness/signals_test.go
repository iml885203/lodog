package readiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/spec"
)

func get(path string) spec.Operation { return spec.Operation{Method: "GET", Path: path} }

func TestTheGenericVocabularyKnowsActionsNotAnyBusiness(t *testing.T) {
	sig := DefaultSignals()
	for _, path := range []string{"/invoices/{id}/send", "/users/{id}/resetPassword", "/orders/{id}/cancel"} {
		if _, ok := sig.SideEffectGET(get(path)); !ok {
			t.Errorf("GET %s is an action and must be seen", path)
		}
	}
	// Whole words only: block, sender and cancelled name things, not actions.
	for _, path := range []string{"/blocks", "/senders/{id}", "/orders/cancelled", "/promotions", "/bonus/issue"} {
		if word, ok := sig.SideEffectGET(get(path)); ok {
			t.Errorf("GET %s is a read but matched %q", path, word)
		}
	}
	for _, path := range []string{"/promotions", "/bonus", "/deposits", "/wallets"} {
		if word, ok := sig.Money(get(path)); ok {
			t.Errorf("%s matched money word %q in the generic vocabulary: that word belongs in a case", path, word)
		}
	}
	if word, ok := sig.Money(get("/billing/invoices")); !ok || word != "billing" {
		t.Errorf("billing is generic money: got %q, %v", word, ok)
	}
}

func TestACaseAddsItsOwnWordsToTheGenericOnes(t *testing.T) {
	sig, err := NewSignals(Config{MoneyWords: []string{"Bonus"}, ActionWords: []string{"publish"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sig.Money(get("/api/bonuses")); !ok {
		t.Error("the case's money word, in its plural, must match")
	}
	if _, ok := sig.Money(get("/api/payments")); !ok {
		t.Error("the generic words stay")
	}
	if _, ok := sig.SideEffectGET(get("/posts/{id}/publish")); !ok {
		t.Error("the case's action word must match")
	}
	if _, ok := DefaultSignals().SideEffectGET(get("/posts/{id}/publish")); ok {
		t.Error("another system's vocabulary must not leak into the default")
	}
}

func TestADeclaredRiskOverridesTheWords(t *testing.T) {
	sig := DefaultSignals()
	read := get("/customers/{id}/reset")
	read.AgentRisk = "read"
	if sig.Mutating(read) {
		t.Error("an overlay that says read beats the word reset")
	}
	if _, ok := sig.SideEffectGET(read); ok {
		t.Error("a declared GET is not a guessed side-effect GET")
	}
	write := get("/customers/{id}")
	write.AgentRisk = "write"
	if !sig.Mutating(write) {
		t.Error("an overlay that says write beats a plain path")
	}
	post := spec.Operation{Method: "POST", Path: "/search", AgentRisk: "read"}
	if !sig.Mutating(post) {
		t.Error("a POST stays mutating whatever it declares: the gateway fails closed")
	}
}

func TestSignalsLoadFromACaseAndDefaultWithoutOne(t *testing.T) {
	dir := t.TempDir()
	if sig, err := LoadSignals(dir); err != nil || sig.Mutating(get("/x")) {
		t.Fatalf("no case.yaml: %v", err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "case.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("name: x\nsignals:\n  money_words: [loyalty]\n")
	sig, err := LoadSignals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sig.Money(get("/loyalty/points")); !ok {
		t.Error("case.yaml money word not loaded")
	}
	write("signals:\n  money_words: [two words]\n")
	if _, err := LoadSignals(dir); err == nil {
		t.Error("a signal that is not one word must be rejected, not silently never match")
	}
}

func TestBackofficeKeepsItsMoneyWordsInItsCaseNotInCode(t *testing.T) {
	t.Chdir("../..")
	sig := backoffice(t)
	if _, ok := sig.Money(get("/api/promotion/rebate")); !ok {
		t.Error("promotion is a money word of the Backoffice case")
	}
	if _, ok := DefaultSignals().Money(get("/api/promotion/rebate")); ok {
		t.Error("promotion must not be in the generic vocabulary")
	}
}

func TestAWordThatIsAlsoANounOrAPreviewIsNotAnActionOnGET(t *testing.T) {
	sig := DefaultSignals()
	for _, path := range []string{"/locks", "/triggers", "/transfers", "/refunds", "/payouts", "/sync", "/clients/{id}/generate-example-token"} {
		if word, ok := sig.SideEffectGET(get(path)); ok {
			t.Errorf("GET %s is a read but matched %q", path, word)
		}
	}
	// The same words on a non-GET still mutate: only GET is guessed from words.
	if !sig.Mutating(spec.Operation{Method: "POST", Path: "/clients/{id}/generate-secret"}) {
		t.Error("a POST always mutates")
	}
}

func TestKeycloakAdminAPIHasNoSideEffectGETAndPagesWithFirstMax(t *testing.T) {
	v, err := FromSpec("testdata/keycloak-26.5.7-admin-rest.yaml", DefaultSignals())
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if c := check(r, "side_effect_gets"); c.Points != c.Max {
		t.Errorf("side-effect GET check = %+v: Keycloak's generate-example-* GETs are read-only", c)
	}
	if _, ok := item(r.L4, "side_effect_gets"); ok {
		t.Error("L4 lists side-effect GETs for Keycloak, which has none")
	}
	c := check(r, "paging")
	if !strings.Contains(c.Detail, "first+max") {
		t.Errorf("paging = %q: Keycloak pages with first+max and it must be seen", c.Detail)
	}
}

func TestPagingIsRecognisedByPairsNotBySingleWords(t *testing.T) {
	op := func(names ...string) spec.Operation {
		o := spec.Operation{Method: "GET", Path: "/things"}
		for _, n := range names {
			o.Params = append(o.Params, spec.Param{Name: n, In: "query"})
		}
		return o
	}
	paged := [][]string{
		{"page", "size"},
		{"page", "per_page"},
		{"page", "pageSize"},
		{"offset", "limit"},
		{"first", "max"},
		{"skip", "take"},
		{"$skip", "$top"},
		{"cursor"},
		{"after", "limit"},
		{"cursor", "count"},
		{"limit"},
	}
	for _, names := range paged {
		if got := PagingParams(op(names...)); len(got) != len(names) {
			t.Errorf("%v: recognised %d of %d paging params", names, len(got), len(names))
		}
	}
	// A weak name alone is a filter, not paging: max price, a "before" date, a size filter.
	for _, names := range [][]string{{"max"}, {"first"}, {"before"}, {"size"}, {"count"}, {"max", "min"}, {"before", "after"}} {
		if got := PagingParams(op(names...)); len(got) != 0 {
			t.Errorf("%v: is not paging but %d params were taken", names, len(got))
		}
	}
}

func TestTheScoreLeavesOutAPreviewOfAnActionButTheSharedGuessKeepsIt(t *testing.T) {
	sig, err := NewSignals(Config{ActionWords: []string{"generate"}, PreviewWords: []string{"dryrun"}})
	if err != nil {
		t.Fatal(err)
	}
	previews := []string{
		"/admin/realms/{realm}/clients/{client-uuid}/evaluate-scopes/generate-example-access-token",
		"/admin/realms/{realm}/clients/{client-uuid}/evaluate-scopes/generate-example-saml-response",
		"/invoices/{id}/send/preview",
		"/invoices/{id}/sendPreview",
		"/newsletters/{id}/send-samples",
		"/reports/generateTemplate",
		"/orders/{id}/cancelDryrun",
	}
	for _, path := range previews {
		if word, ok := sig.ScoreSideEffectGET(get(path)); ok || sig.ScoreMutating(get(path)) {
			t.Errorf("GET %s returns a preview but the score counted it (%q)", path, word)
		}
		if _, ok := sig.SideEffectGET(get(path)); !ok || !sig.Mutating(get(path)) || !sig.MaybeMutating(get(path)) {
			t.Errorf("GET %s: policy draft and bench draft must still treat it as an action", path)
		}
	}
	actions := map[string]string{
		"/reports/generate":                   "generate",
		"/templates/send":                     "send",
		"/samples/{id}/delete":                "delete",
		"/preview/users/{id}/resetPassword":   "reset",
		"/api/customer/{id}/resetPassword":    "reset",
		"/examples/{id}/send/{channel}":       "send",
		"/previews/{id}/generate-and-publish": "generate",
	}
	for path, want := range actions {
		if word, ok := sig.ScoreSideEffectGET(get(path)); !ok || word != want || !sig.ScoreMutating(get(path)) {
			t.Errorf("GET %s = %q, %v: an action with no preview beside it must still count", path, word, ok)
		}
	}
	declared := get("/invoices/{id}/send/preview")
	declared.AgentRisk = "write"
	if !sig.ScoreMutating(declared) {
		t.Error("a declared write beats the preview word")
	}
	if !sig.ScoreMutating(spec.Operation{Method: "POST", Path: "/invoices/{id}/send/preview"}) {
		t.Error("a POST is never excused as a preview")
	}
	if _, err := NewSignals(Config{PreviewWords: []string{"dry run"}}); err == nil {
		t.Error("a preview word that is not one word must be rejected")
	}
}

func TestKeycloakGenerateExampleGETsStayUncountedWhenACaseCallsGenerateAnAction(t *testing.T) {
	sig, err := NewSignals(Config{ActionWords: []string{"generate"}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := FromSpec("testdata/keycloak-26.5.7-admin-rest.yaml", sig)
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if c := check(r, "side_effect_gets"); c.Points != c.Max {
		t.Errorf("side-effect GET check = %+v: the generate-example-* GETs only return an example", c)
	}
	if _, ok := item(r.L4, "side_effect_gets"); ok {
		t.Error("L4 asks to turn Keycloak's generate-example-* GETs into POST")
	}
	previews := 0
	for _, op := range v.Doc.Operations {
		if !strings.Contains(op.Path, "/generate-example-") {
			continue
		}
		previews++
		if sig.ScoreMutating(op) {
			t.Errorf("%s %s counts as changing state: risk, dry-run and idempotency would ask for it", op.Method, op.Path)
		}
	}
	if previews != 3 {
		t.Errorf("found %d generate-example-* operations in the fixture, want 3", previews)
	}
}

// MaybeMutating is the fail-closed judgement behind what an agent may call
// without approval. Each row is one way an operation is held or let through.
func TestMaybeMutatingHoldsEveryDoubtfulOperationAndOnlyReleasesAReviewedRead(t *testing.T) {
	declared := func(risk string, reviewed bool, o spec.Operation) spec.Operation {
		o.AgentRisk, o.NeedsReview = risk, !reviewed
		return o
	}
	withParam := func(o spec.Operation, in, name string) spec.Operation {
		o.Params = append(o.Params, spec.Param{In: in, Name: name})
		return o
	}
	summary := func(o spec.Operation, text string) spec.Operation { o.Summary = text; return o }
	opID := func(o spec.Operation, id string) spec.Operation { o.OperationID = id; return o }
	tests := []struct {
		name string
		op   spec.Operation
		want bool
	}{
		{"POST is always a change", spec.Operation{Method: "POST", Path: "/customers"}, true},
		{"DELETE", spec.Operation{Method: "DELETE", Path: "/customers/{id}"}, true},
		{"POST declared read is still a change", declared("read", true, spec.Operation{Method: "POST", Path: "/search"}), true},
		{"plain GET list", get("/customers"), false},
		{"plain GET by id", get("/customers/{id}"), false},
		{"GET with an action word", get("/customers/{id}/resetPassword"), true},
		{"GET with an action word only before the last two segments is not seen", get("/send/a/b/history"), false},
		{"GET with a money word", get("/billing/invoices"), true},
		{"GET with a money word in the summary", summary(get("/reports/daily"), "daily payment totals"), true},
		{"GET with a suspect word in the path", get("/jobs/{id}/trigger"), true},
		{"GET with a suspect word in the operationId", opID(get("/jobs/{id}"), "triggerJob"), true},
		{"GET with a suspect word in the summary", summary(get("/jobs/{id}"), "Refund the order"), true},
		{"GET with an action word in the operationId", opID(get("/accounts/{id}"), "resetAccount"), true},
		{"GET with an action word in the summary", summary(get("/accounts/{id}"), "Cancel the booking"), true},
		{"GET with ?action=", withParam(get("/devices"), "query", "action"), true},
		{"GET with ?command=", withParam(get("/devices"), "query", "command"), true},
		{"GET with a query parameter that merely contains action", withParam(get("/devices"), "query", "actionDate"), false},
		{"GET with an action header", withParam(get("/devices"), "header", "action"), false},
		{"GET with an ordinary query parameter", withParam(get("/devices"), "query", "name"), false},
		{"GET declared read and reviewed releases the words", declared("read", true, get("/customers/{id}/resetPassword")), false},
		{"GET declared read and reviewed releases a money word", declared("read", true, get("/billing/invoices")), false},
		{"GET declared read and reviewed releases ?action=", declared("read", true, withParam(get("/devices"), "query", "action")), false},
		{"GET declared read but unreviewed stays judged", declared("read", false, get("/customers/{id}/resetPassword")), true},
		{"GET declared read, unreviewed, nothing suspicious", declared("read", false, get("/customers")), false},
		{"GET declared write", declared("write", true, get("/customers")), true},
		{"GET declared money, unreviewed", declared("money", false, get("/customers")), true},
	}
	sig := DefaultSignals()
	for _, tt := range tests {
		if got := sig.MaybeMutating(tt.op); got != tt.want {
			t.Errorf("%s: MaybeMutating = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The three versions only ever loosen in this order.
func TestTheThreeVersionsNeverContradictEachOther(t *testing.T) {
	sig := DefaultSignals()
	for _, op := range []spec.Operation{
		get("/customers"), get("/customers/{id}/resetPassword"), get("/invoices/{id}/send/preview"),
		get("/jobs/{id}/trigger"), get("/billing/invoices"),
		{Method: "POST", Path: "/x"},
		{Method: "HEAD", Path: "/x"},
	} {
		if sig.ScoreMutating(op) && !sig.Mutating(op) {
			t.Errorf("%s %s: counted by the score but not by Mutating", op.Method, op.Path)
		}
		if sig.Mutating(op) && !sig.MaybeMutating(op) {
			t.Errorf("%s %s: Mutating but let through by MaybeMutating", op.Method, op.Path)
		}
	}
}

func TestOnlyGETIsNotMutatedByMethod(t *testing.T) {
	if MethodMutates("GET") {
		t.Error("GET reads")
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if !MethodMutates(m) {
			t.Errorf("%s must count as a change until a person says otherwise", m)
		}
	}
}

func TestACallIsJudgedByTheOperationItHit(t *testing.T) {
	v := View{
		Doc:     &spec.Document{Operations: []spec.Operation{get("/customers/{id}/resetPassword"), get("/invoices/{id}/send/preview"), get("/customers/{id}")}},
		Signals: DefaultSignals(),
	}
	for call, want := range map[string]bool{
		"/customers/7/resetPassword": true,
		"/invoices/9/send/preview":   false,
		"/customers/7":               false,
		"/unknown/resetPassword":     true, // no operation: its path alone is judged
		"/unknown/list":              false,
	} {
		if got := v.CallChangesState("GET", call); got != want {
			t.Errorf("GET %s = %v, want %v", call, got, want)
		}
	}
	if !v.CallChangesState("POST", "/customers/7") {
		t.Error("a POST is a change")
	}
	if (View{Signals: DefaultSignals()}).CallChangesState("POST", "/x") != true {
		t.Error("a view with no document still judges by method")
	}
}
