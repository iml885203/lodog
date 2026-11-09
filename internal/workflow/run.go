package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iml885203/lodog/internal/spec"
)

// Call is one step's request: the operation and its arguments by argument
// name (spec.Param.ArgName), plus the JSON body when the step sends one.
type Call struct {
	StepID string
	Op     spec.Operation
	Args   map[string]any
	// Body is the decoded JSON body; HasBody tells "no body" from a null one.
	Body    any
	HasBody bool
}

// Reply is what the legacy system answered a step.
type Reply struct {
	Status int
	Body   []byte
}

// Caller sends one step to the legacy system. An error means no answer came
// back; the run stops and returns it unchanged.
type Caller func(ctx context.Context, c Call) (Reply, error)

// Result is the outcome of a run: the workflow's data, or the step where it
// stopped and why.
type Result struct {
	Data    any
	Failure *Failure
}

// Failure is a run that stopped before its last step finished.
type Failure struct {
	StepID string
	Op     spec.Operation
	// Status and Body are the legacy system's answer when it refused the
	// step; Status is 0 when the step never ran.
	Status int
	Body   any
	// Missing names the value an earlier step did not return, such as the
	// id of a player a search did not find.
	Missing string
}

// Run executes the steps in order with prepared inputs (Prepare). It stops at
// the first step the legacy system refuses (any non-2xx status) or the first
// step that lacks a required value.
func (t Task) Run(ctx context.Context, inputs map[string]any, call Caller) (Result, error) {
	steps := map[string]map[string]any{}
	var last any
	for i, s := range t.Workflow.Steps {
		op := t.Ops[i]
		c, missing := buildStep(s, op, inputs, steps)
		if missing != "" {
			return Result{Failure: &Failure{StepID: s.StepID, Op: op, Missing: missing}}, nil
		}
		reply, err := call(ctx, c)
		if err != nil {
			return Result{}, err
		}
		body := decode(reply.Body)
		if reply.Status < 200 || reply.Status > 299 {
			return Result{Failure: &Failure{StepID: s.StepID, Op: op, Status: reply.Status, Body: body}}, nil
		}
		outputs := map[string]any{}
		for name, value := range s.Outputs {
			e, _, _ := parseExpr(value)
			if v, ok := evalResponse(e, reply.Status, body); ok {
				outputs[name] = v
			}
		}
		steps[s.StepID] = outputs
		last = body
	}
	if len(t.Workflow.Outputs) == 0 {
		return Result{Data: last}, nil
	}
	data := make(map[string]any, len(t.Workflow.Outputs))
	for name, value := range t.Workflow.Outputs {
		e, _, _ := parseExpr(value)
		data[name] = steps[e.step][e.name]
	}
	return Result{Data: data}, nil
}

func evalResponse(e expr, status int, body any) (any, bool) {
	if e.kind == exprStatus {
		return status, true
	}
	return lookup(body, e.pointer)
}

// buildStep resolves a step's parameters and body. A parameter whose value is
// absent is left out; when the operation requires it, the step cannot run and
// missing names it.
func buildStep(s Step, op spec.Operation, inputs map[string]any, steps map[string]map[string]any) (Call, string) {
	c := Call{StepID: s.StepID, Op: op, Args: map[string]any{}}
	for _, p := range s.Parameters {
		declared, err := matchParam(p, op)
		if err != nil {
			// Bind has refused such a step; a Task never holds one.
			continue
		}
		v, ok := resolve(p.Value, inputs, steps)
		if !ok {
			if declared.Required {
				return Call{}, describeMissing(p.Value, p.Name)
			}
			continue
		}
		c.Args[declared.ArgName()] = v
	}
	if s.RequestBody != nil {
		v, ok := resolve(s.RequestBody.Payload, inputs, steps)
		if !ok {
			return Call{}, describeMissing(s.RequestBody.Payload, "the request body")
		}
		c.Body, c.HasBody = v, true
	}
	return c, ""
}

func describeMissing(v any, what string) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%s (from %s)", what, s)
	}
	return what
}

// resolve replaces every expression in v with its value. Inside an object or
// list an absent value drops its key or item; a whole value that is absent
// reports ok false.
func resolve(v any, inputs map[string]any, steps map[string]map[string]any) (any, bool) {
	switch x := v.(type) {
	case string:
		e, isExpr, err := parseExpr(x)
		if err != nil || !isExpr {
			return x, true
		}
		var got any
		var ok bool
		switch e.kind {
		case exprInput:
			got, ok = inputs[e.name]
		case exprStepOutput:
			got, ok = steps[e.step][e.name]
		case exprBody, exprStatus:
			return nil, false
		}
		return got, ok && got != nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			if r, ok := resolve(child, inputs, steps); ok {
				out[k] = r
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(x))
		for _, child := range x {
			if r, ok := resolve(child, inputs, steps); ok {
				out = append(out, r)
			}
		}
		return out, true
	default:
		return v, true
	}
}

// decode reads a JSON body; a body that is not JSON stays text.
func decode(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	return v
}

// Preview is step i's call as far as the inputs alone settle it, before any
// step has run: what a person approving the run is shown. ok is false when
// the step needs a value only an earlier step returns.
func (t Task) Preview(i int, inputs map[string]any) (Call, bool) {
	c, missing := buildStep(t.Workflow.Steps[i], t.Ops[i], inputs, nil)
	return c, missing == ""
}
