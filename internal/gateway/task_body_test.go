package gateway

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

const bodyTypesDoc = `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{
 "/flat":{"post":{"requestBody":{"required":true,"content":{"application/x-www-form-urlencoded":{"schema":{"type":"string"}}}}}},
 "/raw":{"post":{"requestBody":{"content":{"application/octet-stream":{"schema":{"type":"string","format":"binary"}}}}}},
 "/form":{"post":{"requestBody":{"required":true,"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"name":{"type":"string"}}}}}}}}}}`

func bodyTypesWorkflow(path string) string {
	return `arazzo: 1.0.1
info: {title: t, version: "1", description: Send things.}
sourceDescriptions: [{name: t, url: openapi.json, type: openapi}]
workflows:
  - workflowId: send
    summary: Send it.
    inputs: {type: object, properties: {name: {type: string}}, required: [name]}
    steps:
      - stepId: post
        operationPath: '{$sourceDescriptions.t.url}#/paths/~1` + path + `/post'
        requestBody: {payload: {name: $inputs.name}}
`
}

func bindBodyTypes(t *testing.T, path string) ([]serving.TaskTool, []serving.Skipped, error) {
	t.Helper()
	doc, err := spec.Parse([]byte(bodyTypesDoc))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Parse([]byte(bodyTypesWorkflow(path)))
	if err != nil {
		t.Fatal(err)
	}
	return serving.TaskTools(doc, wf)
}

// A task step whose body the gateway cannot forward would go out in a form
// the legacy system did not ask for, so the workflow is skipped, not served.
func TestATaskStepWithAnUnforwardableBodyIsSkipped(t *testing.T) {
	tools, skipped, err := bindBodyTypes(t, "flat")
	if err != nil || len(tools) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "body schema is not an object") {
		t.Fatalf("tools %d, skipped %v, err %v", len(tools), skipped, err)
	}
	if tools, _, err := bindBodyTypes(t, "form"); err != nil || len(tools) != 1 {
		t.Fatalf("a form step is forwardable: %d tools, err %v", len(tools), err)
	}
}

// The approval rules of single tools hold for task tools: a call that could
// never run gets no approval, concurrent retries with one approval run the
// task once, and forwarded=true is logged only for the run that was sent.
func TestAnL3TaskApprovalIsSpentOnceAndLoggedTruthfully(t *testing.T) {
	h := newTaskGateway(t, casecfg.L3, bob())
	if res, text := h.call(t, "give_bonus", map[string]any{"id": 7}); !res.IsError || approvalID.MatchString(text) {
		t.Fatalf("a call missing amount: %v %s", res.IsError, text)
	}
	if n := len(h.gw.approvals.Pending()); n != 0 {
		t.Fatalf("%d approvals requested for a call that could never run", n)
	}
	_, text := h.call(t, "give_bonus", map[string]any{"id": 7, "amount": 100})
	id := approvalID.FindString(text)
	if _, err := h.gw.approvals.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	const retries = 8
	var wg sync.WaitGroup
	for range retries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.call(t, "give_bonus", map[string]any{"id": 7, "amount": 100, serving.ApprovalArg: id})
		}()
	}
	wg.Wait()
	if n := len(h.legacy.reached("POST", "/players/7/bonus")); n != 1 {
		t.Fatalf("one approval gave %d bonuses", n)
	}
	if n := len(linesWith(h.logs.String(), "tool call decided", "tool=give_bonus", "forwarded=true")); n != 1 {
		t.Fatalf("%d runs logged as forwarded, 1 was sent:\n%s", n, h.logs.String())
	}
}

// A workflow that calls an operation the gateway does not serve is skipped
// with a reason naming the workflow, the step and the way out; the other
// workflows are served and stop suggesting it as a next tool.
func TestAWorkflowUsingAnUnservedOperationIsSkippedAndNoLongerNamed(t *testing.T) {
	doc, err := spec.Parse([]byte(bodyTypesDoc))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Parse([]byte(bodyTypesWorkflow("form") + `    x-agent: {next: [upload_raw]}
  - workflowId: upload_raw
    summary: Upload raw bytes.
    steps:
      - stepId: push
        operationPath: '{$sourceDescriptions.t.url}#/paths/~1raw/post'
`))
	if err != nil {
		t.Fatal(err)
	}
	tools, skipped, err := serving.TaskTools(doc, wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Tool.Name != "send" || len(skipped) != 1 {
		t.Fatalf("tools %d, skipped %v", len(tools), skipped)
	}
	for _, want := range []string{"upload_raw", "step push", "POST /raw", "octet-stream bodies are not forwarded", "remove"} {
		if !strings.Contains(skipped[0].Reason, want) {
			t.Errorf("reason lacks %q: %s", want, skipped[0].Reason)
		}
	}
	if next := tools[0].Task.Workflow.Agent.Next; len(next) != 0 || strings.Contains(tools[0].Tool.Description, "upload_raw") {
		t.Fatalf("next = %v, description %q: a skipped workflow is still suggested", next, tools[0].Tool.Description)
	}
}

// lodog serve starts without the workflow it cannot serve, and the WARN names
// the workflow, the step and the operation.
func TestServeStartsWithoutAWorkflowItCannotServeAndWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	wf := bodyTypesWorkflow("form") + `  - workflowId: upload_raw
    summary: Upload raw bytes.
    steps:
      - stepId: push
        operationPath: '{$sourceDescriptions.t.url}#/paths/~1raw/post'
`
	for name, body := range map[string]string{
		"openapi.json":          bodyTypesDoc,
		"overlay.yaml":          "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n",
		"workflows.arazzo.yaml": wf,
	} {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L2, Listen: "127.0.0.1:0", Upstream: "http://127.0.0.1:1",
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"},
		Identity: casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
	}
	var logs bytes.Buffer
	gw, err := New(cfg, func(string) string { return "k" }, Labels{}, "test", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, skipped := gw.Tools(); len(gw.Tasks()) != 1 || len(skipped) != 1 {
		t.Fatalf("tasks %d, skipped %v", len(gw.Tasks()), skipped)
	}
	for _, want := range []string{"operation not served", "workflow upload_raw skipped", "step push", "path=/raw"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// A booking API that is not Backoffice: the body requires guest and nights, the
// workflow the model drafted marks only guest.
const bookingDoc = `{"openapi":"3.0.4","info":{"title":"booking","version":"1"},"paths":{
 "/bookings":{"post":{"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","required":["guest","nights"],"properties":{
   "guest":{"type":"string"},"nights":{"type":"integer"},"remark":{"type":"string"}}}}}}}}}}`

const bookingWorkflows = `arazzo: 1.0.1
info: {title: booking tools, version: "1", description: Bookings.}
sourceDescriptions: [{name: api, url: openapi.json, type: openapi}]
workflows:
  - workflowId: create_booking
    summary: Book a room.
    inputs: {type: object, required: [guest], properties: {guest: {type: string}, nights: {type: integer}, remark: {type: string}}}
    steps:
      - stepId: book
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1bookings/post'
        requestBody: {payload: {guest: $inputs.guest, nights: $inputs.nights, remark: $inputs.remark}}
`

const bookingPolicy = `
risks: {write: approval}
default: write
rules:
  - {match: "POST /bookings", risk: write}
`

func TestACallThatLeavesOutARequiredBodyFieldIsRefusedBeforeAnyApproval(t *testing.T) {
	h := newL3Case(t, bookingDoc, bookingWorkflows, bookingPolicy, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"}, nil, nil, nil)
	isErr, text := h.call(t, "create_booking", map[string]any{"guest": "Ana", "remark": "late arrival"})
	if !isErr || !strings.Contains(text, `argument "nights" is required`) || !strings.Contains(text, "Next:") {
		t.Fatalf("want an error naming nights with a next action, got %v: %s", isErr, text)
	}
	if approvalID.MatchString(text) || len(h.gw.approvals.Pending()) != 0 {
		t.Fatalf("a call the legacy system would refuse asked a person to approve it: %s", text)
	}
	if got := len(h.forwarded("/bookings")); got != 0 {
		t.Fatalf("%d calls were forwarded", got)
	}
	// With the field it is held for approval as usual.
	isErr, text = h.call(t, "create_booking", map[string]any{"guest": "Ana", "nights": 2})
	if isErr || !approvalID.MatchString(text) {
		t.Fatalf("a complete call should be held for approval, got %v: %s", isErr, text)
	}
}
