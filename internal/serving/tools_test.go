package serving

import (
	"regexp"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/spec"
)

func backofficeTools(t *testing.T) ([]RawTool, []Skipped) {
	t.Helper()
	doc, err := spec.LoadFile("../../cases/backoffice/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	return RawTools(doc)
}

func TestEveryOperationIsServedOrReportedSkipped(t *testing.T) {
	tools, skipped := backofficeTools(t)
	if len(tools)+len(skipped) != 232 {
		t.Fatalf("served %d + skipped %d != 232", len(tools), len(skipped))
	}
	for _, s := range skipped {
		t.Errorf("%s %s skipped for %q; every Backoffice body (JSON or multipart) is forwarded", s.Op.Method, s.Op.Path, s.Reason)
	}
}

var clientToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestToolNamesAreUniqueAndAcceptedByClients(t *testing.T) {
	tools, _ := backofficeTools(t)
	seen := map[string]bool{}
	for _, tool := range tools {
		name := tool.Tool.Name
		if !clientToolName.MatchString(name) {
			t.Errorf("%q is not a name clients accept", name)
		}
		if seen[name] {
			t.Errorf("%q appears twice", name)
		}
		seen[name] = true
	}
}

func TestToolNameIsBuiltFromMethodAndPath(t *testing.T) {
	op := spec.Operation{Method: "GET", Path: "/api/customer/promotion/{customerId}/bonus/history"}
	if got := toolName(op); got != "get_api_customer_promotion_by_customerId_bonus_history" {
		t.Fatalf("toolName = %q", got)
	}
}

func TestALongToolNameIsShortenedWithAStableSuffix(t *testing.T) {
	op := spec.Operation{Method: "GET", Path: "/" + strings.Repeat("segment/", 12)}
	a, b := toolName(op), toolName(op)
	if len(a) > MaxToolName || a != b {
		t.Fatalf("name %q (len %d) not shortened stably", a, len(a))
	}
}

func TestL0DescribesOnlyWhatTheDocumentSays(t *testing.T) {
	tools, _ := backofficeTools(t)
	for _, tool := range tools {
		want := tool.Op.Method + " " + tool.Op.Path
		if tool.Tool.Description != want {
			t.Fatalf("%s: description %q, want exactly %q (Backoffice documents nothing; L0 adds nothing)",
				tool.Tool.Name, tool.Tool.Description, want)
		}
	}
}

func TestInputSchemaCarriesRequiredParametersAndBody(t *testing.T) {
	op := spec.Operation{
		Method: "POST", Path: "/api/x/{id}",
		Params:       []spec.Param{{Name: "id", In: "path", Required: true, Schema: map[string]any{"type": "integer"}}},
		Body:         map[string]any{"type": "object"},
		BodyRequired: true,
	}
	schema := inputSchema(op)
	if schema["type"] != "object" {
		t.Fatalf("type = %v", schema["type"])
	}
	required, _ := schema["required"].([]string)
	if strings.Join(required, ",") != "id,"+BodyArg {
		t.Fatalf("required = %v", required)
	}
}

func TestL1DescribesWhatTheOverlaySays(t *testing.T) {
	op := spec.Operation{
		Method: "GET", Path: "/api/payment/transactions", Summary: "List deposits and withdrawals",
		WhenToUse: "a player's deposit or withdrawal history", WhenNotToUse: "changing a transaction",
	}
	got := rawDescription(op)
	for _, want := range []string{
		"GET /api/payment/transactions", "List deposits and withdrawals",
		"Use when: a player's deposit or withdrawal history", "Do not use when: changing a transaction",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q lacks %q", got, want)
		}
	}
}
