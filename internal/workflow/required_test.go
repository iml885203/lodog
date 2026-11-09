package workflow

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/spec"
)

// An invoicing API (not Backoffice). The body requires customer, currency and memo;
// "customer" is an object that itself requires id. Path "id" and query "tenant"
// are required parameters, "dryRun" is optional.
const invoiceDoc = `{"openapi":"3.0.4","info":{"title":"invoicing","version":"1"},"paths":{
 "/tenants/{id}/invoices":{"post":{"parameters":[
   {"name":"id","in":"path","required":true,"schema":{"type":"integer"}},
   {"name":"tenant","in":"query","required":true,"schema":{"type":"string"}},
   {"name":"dryRun","in":"query","schema":{"type":"boolean"}}],
  "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","required":["customer","currency","memo","channel"],"properties":{
    "customer":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"},"nickname":{"type":"string"}}},
    "currency":{"type":"string"},"memo":{"type":"string"},"channel":{"type":"string"},"note":{"type":"string"}}}}}}}}}}`

func invoiceWorkflow(inputs, params, payload string) string {
	return `arazzo: 1.0.1
info: {title: invoicing tools, version: "1", description: Invoices.}
sourceDescriptions: [{name: inv, url: openapi.json, type: openapi}]
workflows:
  - workflowId: create_invoice
    summary: Create an invoice.
    inputs: {type: object, properties: {` + inputs + `}}
    steps:
      - stepId: create
        operationPath: '{$sourceDescriptions.inv.url}#/paths/~1tenants~1{id}~1invoices/post'
        parameters: [` + params + `]
        requestBody: {payload: {` + payload + `}}
`
}

func invoiceRequired(t *testing.T, inputs, params, payload string) []string {
	t.Helper()
	doc, err := spec.Parse([]byte(invoiceDoc))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse([]byte(invoiceWorkflow(inputs, params, payload)))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := Bind(d, doc)
	if err != nil {
		t.Fatal(err)
	}
	got := requiredInputs(tasks[0].Workflow.Inputs)
	slices.Sort(got)
	return got
}

func TestAnInputThatFillsARequiredSwaggerFieldBecomesRequired(t *testing.T) {
	// The drafted file marks nothing required; the document does.
	got := invoiceRequired(t,
		`tenantId: {type: integer}, tenant: {type: string}, dryRun: {type: boolean}, custId: {type: integer},
         nick: {type: string}, cur: {type: string}, memo: {type: string}, chan: {type: string}, note: {type: string}`,
		`{name: id, in: path, value: $inputs.tenantId}, {name: tenant, in: query, value: $inputs.tenant}, {name: dryRun, in: query, value: $inputs.dryRun}`,
		`customer: {id: $inputs.custId, nickname: $inputs.nick}, currency: $inputs.cur, memo: $inputs.memo, channel: $inputs.chan, note: $inputs.note`)
	want := []string{"chan", "cur", "custId", "memo", "tenant", "tenantId"}
	if !slices.Equal(got, want) {
		t.Fatalf("required = %v, want %v (optional parameters and body fields stay optional)", got, want)
	}
}

func TestARequiredFieldTheStepFixesOrDefaultsNeedsNoArgument(t *testing.T) {
	// currency and memo come from the agent but have a default; channel is a
	// literal in the step, so no input exists for it; the path id is literal.
	got := invoiceRequired(t,
		`tenant: {type: string}, custId: {type: integer}, cur: {type: string, default: USD}, memo: {type: string, x-default: now}`,
		`{name: id, in: path, value: 7}, {name: tenant, in: query, value: $inputs.tenant}`,
		`customer: {id: $inputs.custId}, currency: $inputs.cur, memo: $inputs.memo, channel: web`)
	want := []string{"custId", "tenant"}
	if !slices.Equal(got, want) {
		t.Fatalf("required = %v, want %v", got, want)
	}
}

func TestAnInputTheWorkflowAlreadyRequiresIsKept(t *testing.T) {
	doc, _ := spec.Parse([]byte(invoiceDoc))
	d, _ := Parse([]byte(strings.Replace(invoiceWorkflow(
		`tenant: {type: string}, note: {type: string}`,
		`{name: id, in: path, value: 1}, {name: tenant, in: query, value: $inputs.tenant}`,
		`customer: {id: 1}, currency: EUR, memo: m, channel: web, note: $inputs.note`),
		"inputs: {type: object,", "inputs: {type: object, required: [note],", 1)))
	tasks, err := Bind(d, doc)
	if err != nil {
		t.Fatal(err)
	}
	got := requiredInputs(tasks[0].Workflow.Inputs)
	slices.Sort(got)
	if !slices.Equal(got, []string{"note", "tenant"}) {
		t.Fatalf("required = %v", got)
	}
}

func TestACallMissingASwaggerRequiredFieldIsRefusedWithEveryMissingName(t *testing.T) {
	doc, _ := spec.Parse([]byte(invoiceDoc))
	d, _ := Parse([]byte(invoiceWorkflow(
		`custId: {type: integer}, cur: {type: string}, memo: {type: string}, chan: {type: string}`,
		`{name: id, in: path, value: 1}, {name: tenant, in: query, value: t}`,
		`customer: {id: $inputs.custId}, currency: $inputs.cur, memo: $inputs.memo, channel: $inputs.chan`)))
	tasks, err := Bind(d, doc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks[0].Prepare(map[string]any{"custId": 1.0, "cur": "USD"}, time.Now())
	var in *InputError
	if err == nil || !strings.Contains(err.Error(), `"chan"`) || !strings.Contains(err.Error(), `"memo"`) {
		t.Fatalf("want an error naming chan and memo, got %v", err)
	}
	if !asInput(err, &in) {
		t.Fatalf("not an InputError: %T", err)
	}
}

func asInput(err error, target **InputError) bool {
	e, ok := err.(*InputError) //nolint:errorlint // Prepare returns it directly
	*target = e
	return ok
}
