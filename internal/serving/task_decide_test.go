package serving

import (
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

func taskOf(ops ...spec.Operation) TaskTool {
	return TaskTool{Task: workflow.Task{Ops: ops}, Tool: Tool{Name: "t"}}
}

func decidePolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p := &policy.Policy{
		Risks: map[string]policy.Risk{
			"read":  {Action: policy.Allow},
			"write": {Action: policy.Approval},
			"block": {Action: policy.Deny},
			"cred":  {Action: policy.Hide},
		},
		Default: "write",
		Rules: []policy.Rule{
			{Match: "GET /read/**", Risk: "read"},
			{Match: "GET /block/**", Risk: "block"},
			{Match: "GET /cred/**", Risk: "cred"},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestATaskToolIsGovernedByItsStrictestStep(t *testing.T) {
	p := decidePolicy(t)
	read := spec.Operation{Method: "GET", Path: "/read/a"}
	read2 := spec.Operation{Method: "GET", Path: "/read/b"}
	write := spec.Operation{Method: "POST", Path: "/anything"}
	block := spec.Operation{Method: "GET", Path: "/block/x"}
	cred := spec.Operation{Method: "GET", Path: "/cred/x"}
	for name, tc := range map[string]struct {
		ops      []spec.Operation
		action   policy.Action
		decider  int
		wantRisk string
	}{
		"all reads allow, first decides": {[]spec.Operation{read, read2}, policy.Allow, 0, "read"},
		"one write needs approval":       {[]spec.Operation{read, write, read2}, policy.Approval, 1, "write"},
		"write as the only step":         {[]spec.Operation{write}, policy.Approval, 0, "write"},
		"deny beats approval":            {[]spec.Operation{write, block}, policy.Deny, 1, "block"},
		"hide beats everything":          {[]spec.Operation{read, write, block, cred}, policy.Hide, 3, "cred"},
		"two writes: the first is named": {[]spec.Operation{read, write, write}, policy.Approval, 1, "write"},
		"unclassified op fails closed":   {[]spec.Operation{read, {Method: "DELETE", Path: "/unknown"}}, policy.Approval, 1, "write"},
	} {
		d, i := taskOf(tc.ops...).Decide(p)
		if d.Action != tc.action || i != tc.decider || d.Risk != tc.wantRisk {
			t.Errorf("%s: got %s/%s from step %d, want %s/%s from step %d", name, d.Action, d.Risk, i, tc.action, tc.wantRisk, tc.decider)
		}
	}
}

func TestATaskToolWithNoStepsIsHiddenNotAllowed(t *testing.T) {
	p := decidePolicy(t)
	tool := taskOf()
	d, i := tool.Decide(p)
	if d.Action != policy.Hide || i != -1 {
		t.Fatalf("Decide = %+v, step %d, want hide: nothing was judged, so nothing may run", d, i)
	}
	served, hidden := TaskPlan([]TaskTool{tool}, p)
	if len(served) != 0 || len(hidden) != 1 || !strings.Contains(hidden[0].Reason, "hidden by policy") {
		t.Fatalf("served %d, hidden %+v", len(served), hidden)
	}
}

// An undo is a write like any other: calling it harmless in the workflow does
// not let it past the approval the policy asks of the operation.
func TestAnUndoToolDeclaredReadIsStillHeldByThePolicy(t *testing.T) {
	undo := TaskTool{Task: workflow.Task{
		Workflow: workflow.Workflow{WorkflowID: "clear_flag", Agent: workflow.Agent{Risk: "read"}},
		Ops:      []spec.Operation{{Method: "DELETE", Path: "/flags"}},
	}, Tool: Tool{Name: "clear_flag"}}
	served, hidden := TaskPlan([]TaskTool{undo}, decidePolicy(t))
	if len(hidden) != 0 || len(served) != 1 || served[0].Decision.Action != policy.Approval {
		t.Fatalf("served %+v hidden %v: the undo must wait for approval", served, hidden)
	}
}
