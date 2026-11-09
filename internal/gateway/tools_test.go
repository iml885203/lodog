package gateway

import (
	"testing"

	"github.com/iml885203/lodog/internal/spec"
)

func TestBuildCallMapsArgumentsOntoTheOperation(t *testing.T) {
	op := spec.Operation{
		Method: "GET", Path: "/api/customer/{customerId}",
		Params: []spec.Param{
			{Name: "customerId", In: "path", Required: true},
			{Name: "PageSize", In: "query", Required: true},
			{Name: "Status", In: "query"},
		},
	}
	path, query, body, _, err := buildCall(op, map[string]any{
		"customerId": "a/b", "PageSize": float64(5), "Status": []any{"1", "4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/customer/a%2Fb" {
		t.Errorf("path = %q: a path argument must be escaped, not split the path", path)
	}
	if query.Get("PageSize") != "5" {
		t.Errorf("PageSize = %q: a whole number must not become 5.0", query.Get("PageSize"))
	}
	if got := query["Status"]; len(got) != 2 {
		t.Errorf("Status = %v: a list becomes repeated values", got)
	}
	if body != nil {
		t.Errorf("body = %s, want none", body)
	}
}

func TestBuildCallRefusesAMissingRequiredArgument(t *testing.T) {
	op := spec.Operation{
		Method: "GET", Path: "/api/payment/transactions",
		Params: []spec.Param{{Name: "PageSize", In: "query", Required: true}},
	}
	if _, _, _, _, err := buildCall(op, map[string]any{}); err == nil {
		t.Fatal("want an error: a half-formed call must not reach the legacy system")
	}
	op = spec.Operation{Method: "POST", Path: "/x", Body: map[string]any{"type": "object"}, BodyRequired: true}
	if _, _, _, _, err := buildCall(op, map[string]any{}); err == nil {
		t.Fatal("want an error for a missing required body")
	}
}
