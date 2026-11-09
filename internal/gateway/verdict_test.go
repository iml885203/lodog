package gateway

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content = %v, want one text", res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return tc.Text
}

// A verdict is never ambiguous: ok carries no reason; error carries a reason
// and a next action the agent can follow.
func TestEveryTaskResultIsAnUnambiguousVerdict(t *testing.T) {
	op := spec.Operation{Method: "GET", Path: "/players/{id}"}
	w := workflow.Workflow{Agent: workflow.Agent{Next: []string{"give_bonus"}}}
	results := map[string]workflow.Result{
		"success":         {Data: map[string]any{"id": 1}},
		"success no data": {},
		"huge success":    {Data: map[string]any{"rows": make([]any, 5000)}},
		"missing value":   {Failure: &workflow.Failure{StepID: "read", Op: op, Missing: "$steps.search.outputs.id"}},
		"step never ran":  {Failure: &workflow.Failure{StepID: "read", Op: op}},
	}
	for _, status := range []int{400, 401, 403, 404, 409, 422, 429, 500, 502, 503} {
		results["refused "+strconv.Itoa(status)] = workflow.Result{Failure: &workflow.Failure{StepID: "read", Op: op, Status: status, Body: map[string]any{"msg": "no"}}}
	}
	for name, r := range results {
		res := shape(w, r)
		var env Envelope
		if err := json.Unmarshal([]byte(resultText(t, res)), &env); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkVerdict(t, name, r.Failure != nil, res, env)
	}
}

func checkVerdict(t *testing.T, name string, failed bool, res *mcp.CallToolResult, env Envelope) {
	t.Helper()
	if !failed {
		if env.Status != StatusOK || res.IsError || env.Error != nil {
			t.Errorf("%s: ok result = %+v (isError %v), want ok with no error", name, env, res.IsError)
		}
		return
	}
	e := env.Error
	if env.Status != StatusError || !res.IsError || e == nil {
		t.Errorf("%s: failure = %+v (isError %v), want an error", name, env, res.IsError)
		return
	}
	if strings.TrimSpace(e.Reason) == "" || strings.TrimSpace(e.NextAction) == "" {
		t.Errorf("%s: error %+v lacks a reason or a next action", name, e)
	}
	if e.Step == "" || e.Call != "GET /players/{id}" {
		t.Errorf("%s: error %+v does not name the step and call", name, e)
	}
	if len(env.Next) != 0 {
		t.Errorf("%s: an error must not suggest the next tools of a success: %v", name, env.Next)
	}
}

func TestEveryRefusalNamesWhatToDoNext(t *testing.T) {
	for name, tc := range map[string]struct{ what, next string }{
		"policy":   {"Refused by policy: write risk (default).", "Do not retry."},
		"approval": {"NOT EXECUTED: the user declined this request.", "Do not retry. Tell the user."},
	} {
		res, err := toolError(tc.what, tc.next)
		if err != nil || !res.IsError {
			t.Fatalf("%s: %v, isError %v: a refusal is a tool result marked as an error", name, err, res.IsError)
		}
		text := resultText(t, res)
		if !strings.HasPrefix(text, tc.what+"\nNext: ") || !strings.HasSuffix(text, tc.next) {
			t.Errorf("%s: text = %q, want the reason then \"Next: \" and the action", name, text)
		}
	}
}

func TestAnEnvelopeThatCannotBeEncodedIsStillARefusalWithANextAction(t *testing.T) {
	res := envelopeResult(Envelope{Status: StatusOK, Data: make(chan int)}, false)
	if !res.IsError || !strings.Contains(resultText(t, res), "\nNext: ") {
		t.Fatalf("result = %+v, want an error with a next action", res)
	}
}
