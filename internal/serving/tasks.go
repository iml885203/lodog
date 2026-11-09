package serving

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// ApprovalArg is the tool argument that carries a granted approval back.
// Workflow inputs may not use it or ReasonArg.
const ApprovalArg = "approvalId"

// ReasonArg carries the agent's reason for a held call to the person approving.
// It is read when a call is held and never forwarded or bound to the approval.
const ReasonArg = "approvalReason"

// TaskTool is one workflow served as a tool: the L2 and L3 shape.
type TaskTool struct {
	Tool Tool
	Task workflow.Task
}

// Served is a task tool with the decision the L3 policy made for it. Below L3
// the decision is Allow. Decider is the step whose operation decided.
type Served struct {
	TaskTool
	Decision policy.Decision
	Decider  int
}

// Tasks is what a task level (L2, L3) serves: the tools, every workflow it
// does not serve with the reason, and the workflows document whose
// description is the server's instructions.
type Tasks struct {
	Served   []Served
	Skipped  []Skipped
	Workflow *workflow.Document
}

// LoadPolicy reads the L3 policy of cfg, pointing at lodog policy draft when
// the case has none yet. Below L3 nothing is enforced and it returns nil.
func LoadPolicy(cfg *casecfg.Config) (*policy.Policy, error) {
	if cfg.Level != casecfg.L3 {
		return nil, nil //nolint:nilnil // no policy below L3 is not an error
	}
	if _, statErr := os.Stat(cfg.PolicyPath()); errors.Is(statErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("level L3 needs the policy %s, which does not exist: draft it with `lodog policy draft %s --case %s --out %s` and review it first (docs/policy.md)",
			cfg.PolicyPath(), cfg.OpenAPIPath(), cfg.Case, cfg.PolicyPath())
	}
	return policy.LoadFile(cfg.PolicyPath())
}

// LoadTasks reads the case's workflows, binds them to doc (the document as
// this level serves it) and applies p (nil below L3). lodog serve and lodog
// score both read this answer, so the tools scored are the tools served.
func LoadTasks(cfg *casecfg.Config, doc *spec.Document, p *policy.Policy) (*Tasks, error) {
	if _, statErr := os.Stat(cfg.WorkflowsPath()); errors.Is(statErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("level %s needs the task tools %s, which do not exist: draft them with `lodog workflows draft --case %s` and review them first (docs/levels.md)",
			cfg.Level, cfg.WorkflowsPath(), cfg.Case)
	}
	wf, err := workflow.LoadFile(cfg.WorkflowsPath())
	if err != nil {
		return nil, err
	}
	tools, unsendable, err := TaskTools(doc, wf)
	if err != nil {
		return nil, fmt.Errorf("workflows %s: %w", cfg.WorkflowsPath(), err)
	}
	served, hidden := TaskPlan(tools, p)
	return &Tasks{Served: served, Skipped: append(unsendable, hidden...), Workflow: wf}, nil
}

// TaskTools turns every workflow into a tool. A workflow that does not bind
// to the document is an error, never a tool that fails when called. A workflow
// with a step the gateway cannot forward is skipped and reported, as RawTools
// does for an operation: one such workflow does not stop the others, and the
// tools it was said to be followed by no longer name it.
func TaskTools(doc *spec.Document, wf *workflow.Document) ([]TaskTool, []Skipped, error) {
	tasks, err := workflow.Bind(wf, doc)
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	var skipped []Skipped
	gone := map[string]bool{}
	kept := make([]workflow.Task, 0, len(tasks))
	for _, t := range tasks {
		if s, ok := unsendable(t); ok {
			skipped = append(skipped, s)
			gone[t.Workflow.WorkflowID] = true
			continue
		}
		kept = append(kept, t)
	}
	tools := make([]TaskTool, 0, len(kept))
	for _, t := range kept {
		t.Workflow.Agent.Next = without(t.Workflow.Agent.Next, gone)
		schema := t.InputSchema()
		if props, _ := schema["properties"].(map[string]any); props[ApprovalArg] != nil || props[ReasonArg] != nil {
			errs = append(errs, fmt.Errorf("workflow %q: inputs %s are reserved for approvals", t.Workflow.WorkflowID, ApprovalArg+" and "+ReasonArg))
			continue
		}
		tools = append(tools, TaskTool{Task: t, Tool: Tool{
			Name:        t.Workflow.WorkflowID,
			Description: taskDescription(t),
			InputSchema: schema,
		}})
	}
	return tools, skipped, errors.Join(errs...)
}

// unsendable reports a workflow with a step whose body the gateway cannot
// forward: the step would be sent in a form the legacy system did not ask for.
// The reason names the workflow, the step and what to do about it.
func unsendable(t workflow.Task) (Skipped, bool) {
	for i, op := range t.Ops {
		if reason := unservable(op); reason != "" {
			id := t.Workflow.WorkflowID
			return Skipped{Op: op, Reason: fmt.Sprintf("workflow %s skipped: step %s (%s %s): %s; remove that step or the workflow from the workflows file",
				id, t.Workflow.Steps[i].StepID, op.Method, op.Path, reason)}, true
		}
	}
	return Skipped{}, false
}

func without(names []string, gone map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !gone[n] {
			out = append(out, n)
		}
	}
	return out
}

// taskDescription is what the agent reads to choose the tool: what it does,
// when to use it and when not, its risk, the legacy calls it makes and the
// tools that usually follow.
func taskDescription(t workflow.Task) string {
	w := t.Workflow
	parts := []string{w.Summary}
	if w.Description != "" {
		parts = append(parts, w.Description)
	}
	if w.Agent.WhenToUse != "" {
		parts = append(parts, "Use when: "+w.Agent.WhenToUse)
	}
	if w.Agent.WhenNotToUse != "" {
		parts = append(parts, "Do not use when: "+w.Agent.WhenNotToUse)
	}
	if w.Agent.Risk != "" {
		parts = append(parts, "Risk: "+w.Agent.Risk)
	}
	calls := make([]string, len(t.Ops))
	for i, op := range t.Ops {
		calls[i] = op.Method + " " + op.Path
	}
	parts = append(parts, "Calls: "+strings.Join(calls, ", "))
	if len(w.Agent.Next) > 0 {
		parts = append(parts, "Usually followed by: "+strings.Join(w.Agent.Next, ", "))
	}
	return strings.Join(parts, "\n")
}

// Decide is the policy's decision for the whole tool: the strictest decision
// among its steps' operations, so a read step can never carry a write past
// the gate. It also returns which step decided. A tool with no steps has no
// operation to judge: it is hidden (step -1), never allowed.
func (t TaskTool) Decide(p *policy.Policy) (policy.Decision, int) {
	if len(t.Task.Ops) == 0 {
		return policy.Decision{Action: policy.Hide, Risk: "no steps", Rule: policy.DefaultRule}, -1
	}
	ds := make([]policy.Decision, len(t.Task.Ops))
	for i, op := range t.Task.Ops {
		ds[i] = p.Decide(op.Method, op.Path)
	}
	i := policy.Strictest(ds)
	return ds[i], i
}

// TaskPlan applies p (nil: allow everything) to tools: a tool with a hidden
// step is not served and is reported with the step that hid it. The tools that
// stay carry the decision, which the gateway enforces at L3.
func TaskPlan(tools []TaskTool, p *policy.Policy) ([]Served, []Skipped) {
	out := make([]Served, 0, len(tools))
	var hidden []Skipped
	for _, t := range tools {
		if p == nil {
			out = append(out, Served{TaskTool: t, Decision: policy.Decision{Action: policy.Allow}})
			continue
		}
		d, i := t.Decide(p)
		if d.Action == policy.Hide {
			hidden = append(hidden, hiddenTask(t, d, i))
			continue
		}
		out = append(out, Served{TaskTool: t, Decision: d, Decider: i})
	}
	return out, hidden
}

func hiddenTask(t TaskTool, d policy.Decision, step int) Skipped {
	var op spec.Operation
	if step >= 0 {
		op = t.Task.Ops[step]
	}
	return Skipped{Op: op, Reason: "workflow " + t.Tool.Name + " hidden by policy (" + d.Risk + ", " + d.Rule + ")"}
}
