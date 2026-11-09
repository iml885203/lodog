package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/redact"
	"github.com/iml885203/lodog/internal/serving"
	"github.com/iml885203/lodog/internal/spec"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/workflow"
)

// governedTask is a task tool as L2 or L3 serves it.
type governedTask struct{ serving.Served }

func (t governedTask) subject() subject {
	op := t.Task.Ops[t.Decider]
	return subject{tool: t.Tool.Name, decision: t.Decision, method: op.Method, what: t.Task.Workflow.Summary, summary: func(args map[string]any) string {
		return strings.TrimSpace(fmt.Sprintf("%s %s: %s %s %s", t.Tool.Name, compactArgs(args), op.Method, op.Path, redact.FieldList(t.previewBody(args))))
	}}
}

// shownPath is the concrete path of op with the value of every path parameter
// whose name is a credential left as "{name}", so a person approving is not
// shown a token that sits in the URL. The path actually sent is buildCall's.
func shownPath(op spec.Operation, args map[string]any) string {
	path := op.Path
	for _, p := range op.Params {
		v, ok := args[p.ArgName()]
		if p.In != "path" || !ok || v == nil {
			continue
		}
		shown := url.PathEscape(scalar(v))
		if redact.IsCredentialName(p.Name) {
			shown = "{" + p.Name + "}"
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", shown)
	}
	return path
}

// heldPath is the path of the deciding step a person approving is shown:
// concrete when the inputs alone settle it, the template otherwise.
func (t governedTask) heldPath(inputs map[string]any) string {
	op := t.Task.Ops[t.Decider]
	if c, ok := t.Task.Preview(t.Decider, inputs); ok {
		args := callArgs(c)
		if _, _, _, _, err := buildCall(op, args); err == nil {
			return shownPath(op, args)
		}
	}
	return op.Path
}

// checkSteps builds every step the inputs alone settle, so a call that could
// never be sent is refused before it costs a human verdict.
func (t governedTask) checkSteps(inputs map[string]any) error {
	for i, op := range t.Task.Ops {
		c, ok := t.Task.Preview(i, inputs)
		if !ok {
			continue
		}
		if _, _, _, _, err := buildCall(op, callArgs(c)); err != nil {
			return fmt.Errorf("step %s: %w", c.StepID, err)
		}
		if _, err := buildHeaders(op, callArgs(c)); err != nil {
			return fmt.Errorf("step %s: %w", c.StepID, err)
		}
	}
	return nil
}

// previewBody is the body the deciding step would send, when the inputs alone
// settle it: what the person approving is asked to allow.
func (t governedTask) previewBody(args map[string]any) map[string]any {
	c, ok := t.Task.Preview(t.Decider, args)
	if !ok || !c.HasBody {
		return nil
	}
	body, _ := c.Body.(map[string]any)
	return body
}

// callArgs is a step's call in the shape buildCall reads.
func callArgs(c workflow.Call) map[string]any {
	args := make(map[string]any, len(c.Args)+1)
	for k, v := range c.Args {
		args[k] = v
	}
	if c.HasBody {
		args[serving.BodyArg] = c.Body
	}
	return args
}

// stepRefused stops a run when a step could not be sent or got no answer;
// the result it carries goes to the agent as is.
type stepRefused struct{ res *mcp.CallToolResult }

func (stepRefused) Error() string { return "step not sent" }

func (f forwarder) taskHandler(t governedTask, now func() time.Time) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return toolError("The arguments are not a JSON object.", "Send the arguments as an object matching the tool's input schema.")
			}
		}
		s := t.subject()
		approval, reason := "", ""
		if f.enforcer != nil {
			if res := badApprovalID(args); res != nil {
				return res, nil
			}
			args, approval, reason = withoutApproval(args)
			if refused := f.enforcer.refuse(s); refused != nil {
				f.decided(s, approval, false)
				return refused, nil
			}
		}
		// Checked before the gate, so nobody is asked to approve a call that
		// could never run.
		inputs, err := t.Task.Prepare(args, now())
		if err == nil {
			err = t.checkSteps(inputs)
		}
		if err != nil {
			f.log.Info("tool call refused", "tool", s.tool, "reason", err.Error(), "forwarded", false,
				"actor", f.identity.Actor, "on_behalf_of", f.identity.OnBehalfOf)
			return toolError(err.Error(), "Check the tool's input schema and call again.")
		}
		reserved := false
		if f.enforcer != nil {
			var held *mcp.CallToolResult
			held, approval, reserved = f.enforcer.admit(ctx, req.Session, s, args, approval, t.heldPath(inputs), reason)
			if held != nil {
				f.decided(s, approval, false)
				return held, nil
			}
		}
		return f.runTask(ctx, t, s, inputs, approval, reserved, req.Params.Meta)
	}
}

// runTask runs the steps. The approval, if any, permits the whole run: it is
// spent with the first step that may reach the legacy system, and returned
// when no step was sent.
func (f forwarder) runTask(ctx context.Context, t governedTask, s subject, inputs map[string]any, approval string, reserved bool, meta mcp.Meta) (*mcp.CallToolResult, error) {
	first := true
	defer func() {
		if reserved && first {
			f.enforcer.approvals.Release(approval)
		}
	}()
	result, err := t.Task.Run(ctx, inputs, func(ctx context.Context, c workflow.Call) (workflow.Reply, error) {
		out, err := f.prepare(ctx, s.tool, c.Op, callArgs(c), meta)
		var refused *refusal
		if errors.As(err, &refused) {
			res, _ := toolError(refused.what, refused.next)
			return workflow.Reply{}, stepRefused{res}
		}
		if err != nil {
			return workflow.Reply{}, err
		}
		settles := reserved && first
		resp, res, err := f.transmit(ctx, s, c.Op, out, approval, settles, first)
		first = false
		if err != nil {
			return workflow.Reply{}, err
		}
		if res != nil {
			return workflow.Reply{}, stepRefused{res}
		}
		return workflow.Reply{Status: resp.Status, Body: resp.Body}, nil
	})
	var stopped stepRefused
	if errors.As(err, &stopped) {
		return stopped.res, nil
	}
	if err != nil {
		return nil, err
	}
	return shape(t.Task.Workflow, result), nil
}
