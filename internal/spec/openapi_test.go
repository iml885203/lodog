package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

// The recorded Backoffice document is the vendor format this package reads.
const backofficeDoc = "../../cases/backoffice/spec/openapi.json"

func loadBackoffice(t *testing.T) *Document {
	t.Helper()
	doc, err := LoadFile(backofficeDoc)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestEveryBackofficeOperationIsRead(t *testing.T) {
	doc := loadBackoffice(t)
	if got := len(doc.Operations); got != 232 {
		t.Fatalf("operations = %d, want 232 (anonymized fixture)", got)
	}
}

func TestNoReferenceSurvivesInlining(t *testing.T) {
	for _, op := range loadBackoffice(t).Operations {
		encoded, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"$ref"`) {
			t.Fatalf("%s %s still holds a $ref", op.Method, op.Path)
		}
	}
}

func TestPathParametersAreAlwaysRequired(t *testing.T) {
	for _, op := range loadBackoffice(t).Operations {
		for _, p := range op.Params {
			if p.In == "path" && !p.Required {
				t.Fatalf("%s %s: path parameter %q not required", op.Method, op.Path, p.Name)
			}
		}
	}
}

func TestATransactionQueryKeepsItsRequiredPaging(t *testing.T) {
	op := find(t, loadBackoffice(t), "GET", "/api/payment/transactions")
	required := map[string]bool{}
	for _, p := range op.Params {
		if p.Required {
			required[p.Name] = true
		}
	}
	if !required["PageSize"] || !required["PageNumber"] {
		t.Fatalf("required = %v, want PageSize and PageNumber", required)
	}
}

func TestARequestBodyIsInlinedFromComponents(t *testing.T) {
	op := find(t, loadBackoffice(t), "POST", "/api/promotion/manualBonus/issue")
	if op.BodyContentType != "application/json" || !op.BodyRequired {
		t.Fatalf("body = %q required=%v", op.BodyContentType, op.BodyRequired)
	}
	if _, ok := op.Body["properties"]; !ok {
		t.Fatalf("body schema has no properties: %v", op.Body)
	}
}

func TestARecursiveSchemaIsCutInsteadOfLooping(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi": "3.0.1",
		"paths": {"/node": {"post": {"requestBody": {"content": {"application/json": {
			"schema": {"$ref": "#/components/schemas/Node"}}}}}}},
		"components": {"schemas": {"Node": {"type": "object", "properties": {
			"child": {"$ref": "#/components/schemas/Node"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	// Body is Node; its child refers back to Node, so the child is the cut.
	child := doc.Operations[0].Body["properties"].(map[string]any)["child"].(map[string]any)
	if desc, _ := child["description"].(string); !strings.Contains(desc, "cut") {
		t.Fatalf("recursion not cut: %v", child)
	}
}

func TestADanglingReferenceIsAnError(t *testing.T) {
	_, err := Parse([]byte(`{"openapi": "3.0.1", "paths": {"/x": {"get": {"parameters": [
		{"name": "a", "in": "query", "schema": {"$ref": "#/components/schemas/Missing"}}]}}}}`))
	if err == nil {
		t.Fatal("want an error for a reference to a missing schema")
	}
}

func find(t *testing.T, doc *Document, method, path string) Operation {
	t.Helper()
	for _, op := range doc.Operations {
		if op.Method == method && op.Path == path {
			return op
		}
	}
	t.Fatalf("%s %s not in document", method, path)
	return Operation{}
}

func TestResponsesSecurityAndAgentRiskAreRead(t *testing.T) {
	doc, err := Parse([]byte(`{
	  "openapi": "3.0.1",
	  "security": [{"key": []}],
	  "components": {"securitySchemes": {"key": {"type": "apiKey", "in": "header", "name": "X-Key"}}},
	  "paths": {
	    "/a": {"get": {"responses": {"404": {}, "200": {}}, "x-agent": {"risk": "read"}}},
	    "/b": {"get": {"security": [], "responses": {"200": {}}}}
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := doc.Operations[0], doc.Operations[1]
	if len(doc.SecuritySchemes) != 1 || !a.Secured || b.Secured {
		t.Fatalf("schemes = %v, a secured = %v, b secured = %v", doc.SecuritySchemes, a.Secured, b.Secured)
	}
	if len(a.Responses) != 2 || a.Responses[0] != "200" || a.AgentRisk != "read" {
		t.Fatalf("a = %+v", a)
	}
}

func TestNeedsReviewOfTheAgentRiskIsParsed(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.1","paths":{
	  "/a": {"get": {"responses": {"200": {}}, "x-agent": {"risk": "read", "needsReview": true}}},
	  "/b": {"get": {"responses": {"200": {}}, "x-agent": {"risk": "read"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Operations[0].NeedsReview || doc.Operations[1].NeedsReview {
		t.Fatalf("needsReview = %v, %v; want true, false", doc.Operations[0].NeedsReview, doc.Operations[1].NeedsReview)
	}
}

func TestTheResultIsTheLowestSuccessfulJSONResponse(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{"/x":{"get":{"responses":{
	  "404":{"description":"no","content":{"application/json":{"schema":{"type":"string"}}}},
	  "202":{"description":"later","content":{"application/json":{"schema":{"$ref":"#/components/schemas/B"}}}},
	  "200":{"description":"text","content":{"text/plain":{"schema":{"type":"string"}}}}}}},
	  "/y":{"get":{"responses":{"200":{"description":"none"}}}}},
	  "components":{"schemas":{"B":{"type":"object","properties":{"id":{"type":"integer"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	x, y := doc.Operations[0], doc.Operations[1]
	props, _ := x.Result["properties"].(map[string]any)
	if _, ok := props["id"]; !ok {
		t.Fatalf("result = %v: want the 202 JSON schema, inlined", x.Result)
	}
	if y.Result != nil {
		t.Fatalf("result = %v: no JSON response means no result schema", y.Result)
	}
}

func TestTheBackofficeTransactionsListHasAResultSchema(t *testing.T) {
	for _, op := range loadBackoffice(t).Operations {
		if op.Path == "/api/payment/transactions" && op.Result == nil {
			t.Fatal("the transactions list has no result schema")
		}
	}
}
