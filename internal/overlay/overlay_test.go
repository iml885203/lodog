package overlay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doc(t *testing.T) map[string]any {
	t.Helper()
	var d map[string]any
	err := json.Unmarshal([]byte(`{"paths":{"/api/payment/transactions":{"get":{
		"parameters":[{"name":"TransactionType","in":"query","schema":{"type":"integer"}}],
		"x-agent":{"risk":"read"}}}}}`), &d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func op(d map[string]any) map[string]any {
	return d["paths"].(map[string]any)["/api/payment/transactions"].(map[string]any)["get"].(map[string]any)
}

func TestAnOperationAndAParameterAreUpdated(t *testing.T) {
	d := doc(t)
	o := &Overlay{Overlay: "1.0.0", Actions: []Action{
		{Target: OperationTarget("GET", "/api/payment/transactions"), Update: map[string]any{
			"summary": "List deposits and withdrawals", "x-agent": map[string]any{"whenToUse": "find a player's transactions"},
		}},
		{Target: ParamTarget("GET", "/api/payment/transactions", "TransactionType"), Update: map[string]any{
			"description": "1 = deposit, 2 = withdrawal",
		}},
	}}
	if err := o.Apply(d); err != nil {
		t.Fatal(err)
	}
	got := op(d)
	if got["summary"] != "List deposits and withdrawals" {
		t.Errorf("summary = %v", got["summary"])
	}
	agent := got["x-agent"].(map[string]any)
	if agent["risk"] != "read" || agent["whenToUse"] != "find a player's transactions" {
		t.Errorf("x-agent = %v: the update must merge into it, not replace it", agent)
	}
	param := got["parameters"].([]any)[0].(map[string]any)
	if param["description"] != "1 = deposit, 2 = withdrawal" || param["in"] != "query" {
		t.Errorf("parameter = %v", param)
	}
}

func TestATargetThatMatchesNothingIsAnError(t *testing.T) {
	for _, target := range []string{
		OperationTarget("GET", "/api/gone"),
		OperationTarget("POST", "/api/payment/transactions"),
		ParamTarget("GET", "/api/payment/transactions", "Nope"),
		"$.info",
	} {
		o := &Overlay{Overlay: "1.0.0", Actions: []Action{{Target: target, Update: map[string]any{"summary": "x"}}}}
		if err := o.Apply(doc(t)); err == nil {
			t.Errorf("%s: want an error", target)
		}
	}
}

func TestAnOverlayRoundTripsThroughAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	o := &Overlay{
		Overlay: "1.0.0", Info: Info{Title: "t", Version: "1"},
		Actions: []Action{{Target: OperationTarget("GET", "/a"), Update: map[string]any{"summary": "s"}}},
	}
	if err := o.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Actions) != 1 || back.Actions[0].Update["summary"] != "s" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestANonOneVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(path, []byte("overlay: 2.0.0\nactions: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "1.x") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPlaceQualifiedTargetPicksTheRightSameNamedParameter(t *testing.T) {
	d := map[string]any{"paths": map[string]any{"/x": map[string]any{"get": map[string]any{"parameters": []any{
		map[string]any{"name": "id", "in": "path"},
		map[string]any{"name": "id", "in": "header"},
	}}}}}
	o := &Overlay{Overlay: "1.0.0", Actions: []Action{
		{Target: ParamTargetIn("GET", "/x", "id", "header"), Update: map[string]any{"description": "tenant"}},
	}}
	if err := o.Apply(d); err != nil {
		t.Fatal(err)
	}
	params := d["paths"].(map[string]any)["/x"].(map[string]any)["get"].(map[string]any)["parameters"].([]any)
	if params[0].(map[string]any)["description"] != nil || params[1].(map[string]any)["description"] != "tenant" {
		t.Fatalf("parameters = %v", params)
	}
}

// A parameter shared by the whole path, or declared once under components and
// referenced, is still a parameter of the operation.
func TestAPathLevelOrReferencedParameterCanBeDescribed(t *testing.T) {
	d := map[string]any{
		"paths": map[string]any{"/x": map[string]any{
			"parameters": []any{map[string]any{"name": "tenant", "in": "header"}},
			"get": map[string]any{"parameters": []any{
				map[string]any{"$ref": "#/components/parameters/Limit"},
			}},
		}},
		"components": map[string]any{"parameters": map[string]any{
			"Limit": map[string]any{"name": "limit", "in": "query"},
		}},
	}
	o := &Overlay{Overlay: "1.0.0", Actions: []Action{
		{Target: ParamTarget("GET", "/x", "tenant"), Update: map[string]any{"description": "which customer"}},
		{Target: ParamTarget("GET", "/x", "limit"), Update: map[string]any{"description": "page size"}},
	}}
	if err := o.Apply(d); err != nil {
		t.Fatal(err)
	}
	if d["paths"].(map[string]any)["/x"].(map[string]any)["parameters"].([]any)[0].(map[string]any)["description"] != "which customer" {
		t.Error("path-level parameter not described")
	}
	if d["components"].(map[string]any)["parameters"].(map[string]any)["Limit"].(map[string]any)["description"] != "page size" {
		t.Error("referenced parameter not described")
	}
}

func TestAnOperationThatAdmitsAGuessMustBeFlaggedForReview(t *testing.T) {
	o := &Overlay{Actions: []Action{
		{Target: OperationTarget("GET", "/a"), Update: map[string]any{"description": "Lists a."}},
		{Target: ParamTarget("GET", "/a", "q"), Update: map[string]any{"description": "Match type not confirmed."}},
		{Target: OperationTarget("GET", "/b"), Update: map[string]any{"x-agent": map[string]any{"needsReview": true}}},
		{Target: ParamTarget("GET", "/b", "q"), Update: map[string]any{"description": "[猜] maybe a prefix"}},
	}}
	if got := o.UnflaggedUncertain(); len(got) != 1 || got[0] != OperationTarget("GET", "/a") {
		t.Fatalf("UnflaggedUncertain = %v, want only GET /a", got)
	}
}

func TestTheBackofficeOverlayHasNoUnflaggedGuess(t *testing.T) {
	o, err := LoadFile("../../cases/backoffice/spec/overlay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.UnflaggedUncertain(); len(got) != 0 {
		t.Fatalf("operations that admit a guess without needsReview: %v", got)
	}
}
