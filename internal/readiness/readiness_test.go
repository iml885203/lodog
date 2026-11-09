package readiness

import (
	"math"
	"strconv"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// ready is a small API that does everything the static score asks for.
const ready = `{
  "openapi": "3.0.1",
  "info": {"title": "ready"},
  "security": [{"bearer": []}],
  "components": {"securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}}},
  "paths": {
    "/orders": {
      "get": {
        "summary": "List orders, newest first",
        "parameters": [
          {"name": "pageNumber", "in": "query", "description": "1-based page", "schema": {"type": "integer"}},
          {"name": "pageSize", "in": "query", "description": "rows per page", "schema": {"type": "integer", "maximum": 100}}
        ],
        "responses": {"200": {"description": "ok"}, "400": {"description": "bad paging"}}
      },
      "post": {
        "summary": "Create an order",
        "x-agent": {"risk": "money"},
        "responses": {"201": {"description": "created"}, "422": {"description": "invalid"}}
      }
    },
    "/orders/{id}/cancel": {
      "get": {
        "summary": "Cancel an order",
        "x-agent": {"risk": "write"},
        "parameters": [{"name": "id", "in": "path", "required": true, "description": "order id", "schema": {"type": "string"}}],
        "responses": {"200": {"description": "ok"}, "404": {"description": "no such order"}}
      }
    }
  }
}`

func parse(t *testing.T, doc string) *spec.Document {
	t.Helper()
	d, err := spec.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func check(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestTheWeightsAddUpToTheStaticShare(t *testing.T) {
	r := Score(View{Doc: parse(t, `{"openapi":"3.0.1","paths":{}}`)})
	var total float64
	for _, c := range r.Checks {
		total += c.Max
	}
	if total != Max {
		t.Fatalf("weights sum to %v, want %v", total, float64(Max))
	}
}

func TestAnAgentReadyAPIScoresFullMarksExceptIdentity(t *testing.T) {
	r := Score(View{Doc: parse(t, ready)})
	if r.Points != Max-2 || check(r, "agent_identity").Points != 0 {
		t.Fatalf("points = %v, checks = %+v", r.Points, r.Checks)
	}
	r = Score(View{Doc: parse(t, ready), Identity: true})
	if r.Points != Max {
		t.Fatalf("with identity: points = %v", r.Points)
	}
}

const bare = `{
  "openapi": "3.0.1",
  "paths": {
    "/customer/{id}/resetPassword": {"get": {"parameters": [{"name": "id", "in": "path", "schema": {"type": "string"}}], "responses": {"200": {}}}},
    "/customer/{id}": {"get": {"parameters": [{"name": "id", "in": "path", "schema": {"type": "string"}}], "responses": {"200": {}}}},
    "/payment/withdraw/reject": {"post": {"responses": {"200": {}}}}
  }
}`

func TestABareAPIScoresOnlyForItsSize(t *testing.T) {
	r := Score(View{Doc: parse(t, bare)})
	want := map[string]float64{
		"descriptions": 0, "tool_count": 4, "risk_declared": 0, "side_effect_gets": 0,
		"auth_declared": 0, "agent_identity": 0, "error_responses": 0, "paging": 3,
	}
	for id, points := range want {
		if got := check(r, id).Points; got != points {
			t.Errorf("%s = %v, want %v", id, got, points)
		}
	}
	if ex := check(r, "side_effect_gets").Examples; len(ex) != 1 {
		t.Errorf("side-effect examples = %v", ex)
	}
}

func TestAPolicyDeclaresRiskNeutralizesTrapsAndHidesTools(t *testing.T) {
	p := &policy.Policy{
		Risks:   map[string]policy.Risk{"read": {Action: policy.Allow}, "write": {Action: policy.Approval}, "credential": {Action: policy.Hide}},
		Default: "write",
		Rules: []policy.Rule{
			{Match: "GET /customer/*/resetPassword", Risk: "credential"},
			{Match: "GET /customer/*", Risk: "read"},
			{Match: "POST /payment/**", Risk: "write"},
		},
	}
	r := Score(taskView(t, p))
	if r.Tools != 2 || check(r, "risk_declared").Points != 5 || check(r, "side_effect_gets").Points != 3 {
		t.Fatalf("tools = %d, checks = %+v", r.Tools, r.Checks)
	}
}

func TestAPolicyDefaultNeutralizesTrapsButDeclaresNoRisk(t *testing.T) {
	p := &policy.Policy{
		Risks:   map[string]policy.Risk{"read": {Action: policy.Allow}, "write": {Action: policy.Approval}},
		Default: "write",
	}
	r := Score(taskView(t, p))
	risk := check(r, "risk_declared")
	if risk.Points != 0 || len(risk.Examples) != 2 || check(r, "side_effect_gets").Points != 3 {
		t.Fatalf("checks = %+v", r.Checks)
	}
}

func TestTheToolCountScalesDownPastTwenty(t *testing.T) {
	c := toolCount(View{}, make([]spec.Operation, 80))
	if c.Points != 1 {
		t.Fatalf("80 tools = %v points, want 1", c.Points)
	}
}

func TestBackofficeScoresHigherAtL3ThanRaw(t *testing.T) {
	t.Chdir("../..") // gateway configs name their case relative to the repository root
	raw, err := FromSpec("cases/backoffice/spec/openapi.json", backoffice(t))
	if err != nil {
		t.Fatal(err)
	}
	l0 := Score(raw)
	l3view, notes, err := FromGateway("cases/backoffice/gateway/L3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l3 := Score(l3view)
	if l0.Points >= l3.Points || len(notes) == 0 {
		t.Fatalf("L0 = %v, L3 = %v, notes = %v", l0.Points, l3.Points, notes)
	}
	// L3 includes L1's overlay, whose x-agent.risk declares every operation,
	// so its risk marks can reach full points.
	if check(l0, "risk_declared").Points != 0 || check(l3, "risk_declared").Points <= 0 || check(l3, "agent_identity").Points != 2 {
		t.Fatalf("L0 = %+v\nL3 = %+v", l0.Checks, l3.Checks)
	}
	l1view, _, err := FromGateway("cases/backoffice/gateway/L1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l1 := Score(l1view)
	if check(l1, "descriptions").Points <= check(l0, "descriptions").Points || l1.Points <= l0.Points {
		t.Fatalf("L1 = %v (descriptions %v) must beat L0 = %v: the overlay describes every operation",
			l1.Points, check(l1, "descriptions").Points, l0.Points)
	}
	l0view, _, err := FromGateway("cases/backoffice/gateway/L0.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := Score(l0view); math.Abs(got.Points-l0.Points) > 0.001 {
		t.Fatalf("the L0 gateway (%v) must score as the raw document (%v): identity and policy count only at L3", got.Points, l0.Points)
	}
}

func TestBackofficeScoresHigherAtL2ThanL1(t *testing.T) {
	t.Chdir("../..")
	l1view, _, err := FromGateway("cases/backoffice/gateway/L1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l2view, _, err := FromGateway("cases/backoffice/gateway/L2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l1, l2 := Score(l1view), Score(l2view)
	if l2.Points <= l1.Points || check(l2, "tool_count").Points <= check(l1, "tool_count").Points || l2.Tools > 25 {
		t.Fatalf("L2 = %v with %d tools must beat L1 = %v: the task tools are about twenty", l2.Points, l2.Tools, l1.Points)
	}
}

// taskView is the view an L3 gateway gives the agent: one single-step task
// tool per operation, with the tools the policy hides left out and listed as
// skipped, the way lodog serve and lodog score plan them.
func taskView(t *testing.T, p *policy.Policy) View {
	t.Helper()
	doc := parse(t, bare)
	tools := make([]serving.TaskTool, len(doc.Operations))
	for i, op := range doc.Operations {
		tools[i] = serving.TaskTool{
			Tool: serving.Tool{Name: "tool_" + strconv.Itoa(i), InputSchema: map[string]any{"type": "object"}},
			Task: workflow.Task{Workflow: workflow.Workflow{WorkflowID: "tool_" + strconv.Itoa(i)}, Ops: []spec.Operation{op}},
		}
	}
	served, hidden := serving.TaskPlan(tools, p)
	kept := make([]serving.TaskTool, len(served))
	for i, sv := range served {
		kept[i] = sv.TaskTool
	}
	return View{Doc: doc, Policy: p, Tasks: kept, TasksSkipped: hidden}
}

func TestAViewSaysWhichLevelOffersACallAndWhichHidesItByPolicy(t *testing.T) {
	t.Chdir("../..")
	const reset, reject = "/api/customer/910004/resetPassword", "/api/payment/transaction/withdraw/reject"
	cases := []struct {
		level           string
		method, path    string
		reached, hidden bool
	}{
		{"L0", "GET", reset, true, false},   // one tool per operation
		{"L2", "GET", reset, false, false},  // no task tool has the step, and L2 hides nothing
		{"L3", "GET", reset, false, true},   // the policy hides it
		{"L3", "POST", reject, true, false}, // a task tool holds it for approval
		{"L3", "GET", "/not/in/the/document", false, false},
	}
	for _, tc := range cases {
		v, _, err := FromGateway("cases/backoffice/gateway/" + tc.level + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if reached, hidden := v.Reach(tc.method, tc.path); reached != tc.reached || hidden != tc.hidden {
			t.Errorf("%s %s %s: reached = %v, hidden = %v, want %v, %v", tc.level, tc.method, tc.path, reached, hidden, tc.reached, tc.hidden)
		}
	}
}
