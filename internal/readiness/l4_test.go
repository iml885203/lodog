package readiness

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
)

func item(l4 *L4, id string) (L4Item, bool) {
	for _, it := range l4.Items {
		if it.ID == id {
			return it, true
		}
	}
	return L4Item{}, false
}

func TestEachScoredItemGainsWhatItsCheckStillLacks(t *testing.T) {
	r := Score(View{Doc: parse(t, bare)})
	var sum float64
	for _, it := range r.L4.Items {
		if it.Check == "" {
			if it.Gain != 0 {
				t.Errorf("%s is outside the static score but gains %v", it.ID, it.Gain)
			}
			continue
		}
		c := check(r, it.Check)
		if want := c.Max - c.Points; it.Gain != want {
			t.Errorf("%s gains %v, its check lacks %v", it.ID, it.Gain, want)
		}
		sum += it.Gain
	}
	if r.L4.Gain != round2(sum) || r.L4.Gain <= 0 {
		t.Fatalf("total gain = %v, items add up to %v", r.L4.Gain, sum)
	}
}

func TestAnItemNamesTheEndpointsAndCarriesAnInstructionToPaste(t *testing.T) {
	r := Score(View{Doc: parse(t, bare)})
	for _, it := range r.L4.Items {
		if strings.TrimSpace(it.Instruction.String()) == "" || it.Measured.String() == "" {
			t.Errorf("%s has no instruction or no measure: %+v", it.ID, it)
		}
	}
	get, ok := item(r.L4, "side_effect_gets")
	if !ok || get.Gain != 3 || !strings.Contains(get.Instruction.String(), "GET /customer/{id}/resetPassword") {
		t.Fatalf("side-effect GET item = %+v", get)
	}
}

func TestAPolicyHidingATrapStillLeavesItOnTheL4List(t *testing.T) {
	p := &policy.Policy{
		Risks:   map[string]policy.Risk{"read": {Action: policy.Allow}, "write": {Action: policy.Approval}, "credential": {Action: policy.Hide}},
		Default: "write",
		Rules:   []policy.Rule{{Match: "GET /customer/*/resetPassword", Risk: "credential"}},
	}
	r := Score(taskView(t, p))
	get, ok := item(r.L4, "side_effect_gets")
	if !ok || get.Gain != 0 || len(get.Examples) != 1 {
		t.Fatalf("hidden trap: item = %+v", get)
	}
}

func TestAReadyAPILeavesOnlyWhatTheScoreDoesNotSee(t *testing.T) {
	r := Score(View{Doc: parse(t, ready), Identity: true})
	for _, id := range []string{"error_responses", "auth_declared", "paging"} {
		if _, ok := item(r.L4, id); ok {
			t.Errorf("%s is already satisfied but is listed", id)
		}
	}
	// the GET that cancels an order is marked write, so it earns nothing more but still needs the code change
	if get, ok := item(r.L4, "side_effect_gets"); !ok || get.Gain != 0 {
		t.Errorf("a marked side-effect GET: item = %+v", get)
	}
	if r.L4.Gain != 0 {
		t.Fatalf("gain = %v on a ready API with identity", r.L4.Gain)
	}
	if _, ok := item(r.L4, "dry_run"); !ok {
		t.Fatal("no dry-run parameter anywhere, but dry_run is not listed")
	}
}

func TestADryRunAndAnIdempotencyKeyRemoveTheirItems(t *testing.T) {
	doc := `{"openapi":"3.0.1","paths":{
	  "/pay": {"post": {"requestBody": {"content": {"application/json": {"schema": {"type":"object","properties": {"idempotencyKey": {"type":"string"}}}}}},
	    "parameters": [{"name":"dryRun","in":"query","schema":{"type":"boolean"}}], "responses": {"200": {}}}}}}`
	r := Score(View{Doc: parse(t, doc)})
	for _, id := range []string{"dry_run", "idempotency_key"} {
		if _, ok := item(r.L4, id); ok {
			t.Errorf("%s is declared but listed", id)
		}
	}
}

func TestStandardsAreUncheckedWithoutAnOriginAndFoundOnlyAsJSON(t *testing.T) {
	r := Score(View{Doc: parse(t, bare)})
	for _, s := range r.L4.Standards {
		if s.Status != StandardUnchecked {
			t.Fatalf("%s = %s without an origin", s.ID, s.Status)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/mcp/server-card.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/.well-known/oauth-protected-resource":
			_, _ = w.Write([]byte(`<html>catch-all</html>`)) // 200 but not the document
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	if err := CheckStandards(context.Background(), srv.Client(), srv.URL, &r); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range r.L4.Standards {
		got[s.ID] = s.Status
	}
	want := map[string]string{"mcp_server_card": StandardFound, "oauth_protected_resource": StandardMissing, "api_catalog": StandardMissing}
	for id, status := range want {
		if got[id] != status {
			t.Errorf("%s = %s, want %s", id, got[id], status)
		}
	}
	if err := CheckStandards(context.Background(), srv.Client(), "not-a-url", &r); err == nil {
		t.Fatal("a relative origin must be rejected")
	}
}

func TestBackofficeL4ListsTheTrapsAndWhatEachGains(t *testing.T) {
	t.Chdir("../..")
	raw, err := FromSpec("cases/backoffice/spec/openapi.json", backoffice(t))
	if err != nil {
		t.Fatal(err)
	}
	r := Score(raw)
	get, ok := item(r.L4, "side_effect_gets")
	if !ok || get.Gain != 3 || len(get.Examples) != 2 {
		t.Fatalf("Backoffice traps = %+v", get)
	}
	var out bytes.Buffer
	if err := Render(&out, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"L4：", "+3.00 分", "貼給 coding agent", "MCP Server Card"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// petstore is the public Swagger Petstore: a system with none of Backoffice's
// paths or words, with header parameters and a different auth model.
func TestAPublicPetstoreGetsASensibleL4List(t *testing.T) {
	v, err := FromSpec("testdata/petstore.json", DefaultSignals())
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if _, ok := item(r.L4, "side_effect_gets"); ok {
		t.Error("petstore has no side-effect GET")
	}
	if _, ok := item(r.L4, "error_responses"); ok {
		t.Error("petstore documents its errors")
	}
	auth, ok := item(r.L4, "auth_declared")
	if !ok || auth.Gain <= 0 || auth.Gain >= 2 {
		t.Errorf("auth item = %+v: some operations are secured, some are not", auth)
	}
	if _, ok := item(r.L4, "dry_run"); !ok {
		t.Error("no petstore operation has a dry-run parameter")
	}
	if r.L4.Gain != round2(auth.Gain+2) {
		t.Errorf("gain = %v, want the auth gain plus the 2 identity points", r.L4.Gain)
	}
}

func TestAnIdempotencyKeyHeaderCountsAsDeclared(t *testing.T) {
	doc := `{"openapi":"3.0.1","paths":{"/pay":{"post":{
	  "parameters":[{"name":"Idempotency-Key","in":"header","schema":{"type":"string"}}],"responses":{"200":{}}}}}}`
	r := Score(View{Doc: parse(t, doc)})
	if _, ok := item(r.L4, "idempotency_key"); ok {
		t.Fatal("a header idempotency key is declared but the item is listed")
	}
	other := strings.Replace(doc, "Idempotency-Key", "X-Tenant", 1)
	if _, ok := item(Score(View{Doc: parse(t, other)}).L4, "idempotency_key"); !ok {
		t.Fatal("no idempotency key anywhere, but the item is not listed")
	}
}

// backoffice is the vocabulary of the Backoffice case: the generic words plus the
// money words its case.yaml adds. The working directory is the repository root.
func backoffice(t *testing.T) Signals {
	t.Helper()
	sig, err := LoadSignals("cases/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func renderOf(t *testing.T, v View) string {
	t.Helper()
	var out strings.Builder
	if err := Render(&out, Score(v)); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// An item whose check is already full earns nothing, so it is not offered as
// "+0.00": it sits with the items outside the static score.
func TestAnItemThatEarnsNothingMoreIsNotPrintedAsAGain(t *testing.T) {
	out := renderOf(t, View{Doc: parse(t, ready), Identity: true})
	if strings.Contains(out, "+0.00") {
		t.Errorf("the report prints a +0.00 gain:\n%s", out)
	}
	_, after, found := strings.Cut(out, "靜態分不計、原系統仍建議改")
	if !found || !strings.Contains(after, "把會改資料的 GET 改成 POST") {
		t.Errorf("the handled GET item is not under the not-counted heading:\n%s", out)
	}
	if !strings.Contains(after, "給 agent 真正的機器身份") {
		t.Errorf("the identity item, already earned through the gateway, is not there either:\n%s", out)
	}
	// An item that still earns points stays above it.
	bare := renderOf(t, View{Doc: parse(t, bare)})
	if head, _, _ := strings.Cut(bare, "靜態分不計、原系統仍建議改"); !strings.Contains(head, "+3.00 分  把會改資料的 GET 改成 POST") {
		t.Errorf("an item with a real gain must be listed with its points:\n%s", bare)
	}
}

func TestTheGETCountSaysWhereEachCameFrom(t *testing.T) {
	byName, ok := item(Score(View{Doc: parse(t, bare)}).L4, "side_effect_gets")
	if !ok || !strings.Contains(byName.Detail.String(), "名字是動作") || strings.Contains(byName.Detail.String(), "overlay") {
		t.Fatalf("a GET caught by its name: %q", byName.Detail.String())
	}
	declared, ok := item(Score(View{Doc: parse(t, ready), Identity: true}).L4, "side_effect_gets")
	if !ok || !strings.Contains(declared.Detail.String(), "被 overlay 宣告成會改資料") || strings.Contains(declared.Detail.String(), "名字是動作") {
		t.Fatalf("a GET declared by an overlay: %q", declared.Detail.String())
	}
}
