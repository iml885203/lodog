package readiness

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

// taskOperation is a task tool as the checks read it: one tool with the
// workflow's description and inputs, standing on the step that matters most
// for risk (the first that may change state, else the last). It documents
// errors and is secured only when every step's operation is, since a failure
// in any step reaches the agent.
func taskOperation(t serving.TaskTool, sig Signals) spec.Operation {
	w := t.Task.Workflow
	primary := t.Task.Ops[len(t.Task.Ops)-1]
	for _, op := range t.Task.Ops {
		if sig.Mutating(op) {
			primary = op
			break
		}
	}
	op := spec.Operation{
		Method:       primary.Method,
		Path:         primary.Path,
		OperationID:  w.WorkflowID,
		Summary:      w.Summary,
		Description:  w.Description,
		AgentRisk:    w.Agent.Risk,
		WhenToUse:    w.Agent.WhenToUse,
		WhenNotToUse: w.Agent.WhenNotToUse,
		Responses:    stepResponses(t.Task.Ops),
		Secured:      true,
	}
	for _, s := range t.Task.Ops {
		op.Secured = op.Secured && s.Secured
	}
	props, _ := t.Tool.InputSchema["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := t.Tool.InputSchema["required"].([]string); ok {
		for _, r := range req {
			required[r] = true
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schema, _ := props[name].(map[string]any)
		desc, _ := schema["description"].(string)
		op.Params = append(op.Params, spec.Param{Name: name, In: "query", Required: required[name], Description: desc, Schema: schema})
	}
	return op
}

// stepResponses keeps the error codes only when every step documents one.
func stepResponses(ops []spec.Operation) []string {
	codes := map[string]bool{}
	all := true
	for _, op := range ops {
		all = all && hasErrorResponse(op)
		for _, c := range op.Responses {
			codes[c] = true
		}
	}
	var out []string
	for c := range codes {
		if !all && (strings.HasPrefix(c, "4") || strings.HasPrefix(c, "5") || c == "default") {
			continue
		}
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// endpoints are the legacy operations behind what the agent sees: what L4
// asks the legacy code to change.
func endpoints(v View, tools []spec.Operation) []spec.Operation {
	if v.Tasks == nil {
		return tools
	}
	seen := map[string]bool{}
	var out []spec.Operation
	for _, t := range v.Tasks {
		for _, op := range t.Task.Ops {
			key := op.Method + " " + op.Path
			if !seen[key] {
				seen[key] = true
				out = append(out, op)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return name(out[i]) < name(out[j]) })
	return out
}

func taskNote(v View) string {
	return fmt.Sprintf("%s 的 tool 是 %d 個任務 tool（workflows），每個依它的 workflow 說明、輸入和步驟呼叫的 API 評分", v.Level, len(v.Tasks))
}
