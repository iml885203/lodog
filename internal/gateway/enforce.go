package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/iml885203/lodog/internal/approval"
	"github.com/iml885203/lodog/internal/redact"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/policy"
)

// maxReason bounds the agent's reason: it is shown to a person.
const maxReason = 300

// withApprovalArg is the tool as a held call needs it: it also takes the
// approvalId and the agent's reason.
func withApprovalArg(tool serving.Tool, risk string) serving.Tool {
	props, _ := tool.InputSchema["properties"].(map[string]any)
	props = maps.Clone(props)
	if props == nil {
		props = map[string]any{}
	}
	props[serving.ReasonArg] = map[string]any{
		"type": "string",
		"description": "Optional. One sentence for the human who approves: why this call is needed. " +
			"It is shown to them as your own words and does not change what runs.",
	}
	props[serving.ApprovalArg] = map[string]any{
		"type": "string",
		"description": "This call is " + risk + " risk and runs only after a human approves it. " +
			"Call once without approvalId to request approval; once the operator has approved, " +
			"call again with the same arguments and the approval id.",
	}
	schema := maps.Clone(tool.InputSchema)
	schema["properties"] = props
	tool.InputSchema = schema
	return tool
}

// mcpTool is a served tool as the MCP SDK registers it.
func mcpTool(t serving.Tool) *mcp.Tool {
	return &mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
}

// subject is what the L3 gate and the call log know about one tool call: the
// tool, the policy's decision for it, and the legacy call a person approving
// it is shown.
type subject struct {
	tool     string
	decision policy.Decision
	method   string
	summary  func(args map[string]any) string
	// what is the operation or task in plain words, for the person approving.
	what string
}

// enforcer is the L3 gate in front of the forwarder.
type enforcer struct {
	approvals *approval.Store
	identity  casecfg.IdentityConfig
	hub       *approval.Hub
	// asker, when set, puts each held call to the user at the MCP client.
	asker *approval.Asker
	log   *slog.Logger
}

// withoutApproval splits the approval id and the agent's reason off the
// arguments, so neither is forwarded or part of the arguments an approval is
// bound to.
func withoutApproval(args map[string]any) (rest map[string]any, id, reason string) {
	id, _ = args[serving.ApprovalArg].(string)
	reason, _ = args[serving.ReasonArg].(string)
	rest = maps.Clone(args)
	delete(rest, serving.ApprovalArg)
	delete(rest, serving.ReasonArg)
	return rest, id, clipReason(reason)
}

func clipReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxReason {
		return string(r[:maxReason]) + "…"
	}
	return s
}

// refuse answers a call the policy never lets through, before its arguments
// are read: nil means the call may go on to admit.
func (e enforcer) refuse(s subject) *mcp.CallToolResult {
	switch s.decision.Action {
	case policy.Allow, policy.Approval:
		return nil
	case policy.Deny:
		res, _ := toolError(fmt.Sprintf("Refused by policy: %s risk (%s).", s.decision.Risk, s.decision.Rule),
			"Do not retry. Tell the operator this action is not allowed through the agent.")
		return res
	case policy.Hide:
		// A hidden tool is never served; reaching here means it was.
	}
	res, _ := toolError("The gateway has no decision for this tool.", "Do not retry. Tell the operator.")
	return res
}

// admit decides a call whose arguments already built a valid request, so an
// approval is only ever requested for a call that could be sent. It returns a
// result to send back instead of forwarding, the approval id involved, and
// whether that approval is now reserved for this call: the caller then
// commits it once the request is sent, or releases it if nothing was.
func (e enforcer) admit(ctx context.Context, sess approval.Elicitor, s subject, args map[string]any, id, path, reason string) (*mcp.CallToolResult, string, bool) {
	if s.decision.Action != policy.Approval {
		return nil, "", false
	}
	if id == "" {
		return e.hold(ctx, sess, s, args, path, reason)
	}
	if err := e.approvals.Reserve(id, s.tool, args); err != nil {
		return refuseApproval(id, err), id, false
	}
	return nil, id, true
}

func (e enforcer) hold(ctx context.Context, sess approval.Elicitor, s subject, args map[string]any, path, reason string) (*mcp.CallToolResult, string, bool) {
	summary := s.summary(args)
	req, err := e.approvals.Hold(s.tool, s.method, path, summary, s.decision, args, e.identity)
	if err != nil {
		res, _ := toolError("The approval request could not be recorded: "+err.Error(), "Do not retry. Tell the operator.")
		return res, "", false
	}
	ask := fmt.Sprintf("ask the operator to approve %s", req.ID)
	afterApproval := true
	switch {
	case e.asker != nil:
		verdict, by, why := e.asker.Ask(ctx, sess, approval.NoticeFor(req, s.what, reason))
		if res, id, reserved, done := e.afterAsking(verdict, by, req, s, args); done {
			return res, id, reserved
		}
		// Nobody can approve this request any more; a fresh call asks again.
		ask = fmt.Sprintf("the user could not be asked just now (%s); tell the user this needs their approval, then call this tool again without approvalId to ask them again", why)
		afterApproval = false
	case e.hub.Configured():
		if e.hub.Announce(ctx, req, s.what, reason) {
			ask = fmt.Sprintf("the approvers were sent a message to approve or deny %s; tell the operator you are waiting", req.ID)
		} else {
			// Nobody was told, and nothing else can approve it: retrying
			// without an id is the only way forward.
			ask = "the approval notification was not sent, so nobody has been asked; tell the operator, then call this tool again without approvalId to send it again"
			afterApproval = false
		}
	}
	text := fmt.Sprintf("NOT EXECUTED: needs approval.\nApproval id: %s\nRisk: %s\nCall: %s\nNext: %s.", req.ID, req.Risk, summary, ask)
	if afterApproval {
		text += fmt.Sprintf(" After they approve, call this tool again with the same arguments plus approvalId=%q. Do not tell anyone it is done until then.", req.ID)
	} else {
		text += " Do not tell anyone it is done."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, req.ID, false
}

// afterAsking turns the user's answer into the call's fate. done is false
// when the user gave none: the request stays pending.
func (e enforcer) afterAsking(verdict approval.AskVerdict, by string, req *approval.Request, s subject, args map[string]any) (res *mcp.CallToolResult, id string, reserved, done bool) {
	switch verdict {
	case approval.Accepted:
		if _, err := e.approvals.Decide(req.ID, true, by); err != nil {
			r, _ := toolError("NOT EXECUTED: the approval could not be recorded: "+err.Error(), "Call again without approvalId to ask again.")
			return r, req.ID, false, true
		}
		e.log.Info("approval decided", "id", req.ID, "status", approval.Approved, "by", by, "tool", s.tool)
		// Spent by this very call: the same arguments, reserved now, sent next.
		if err := e.approvals.Reserve(req.ID, s.tool, args); err != nil {
			return refuseApproval(req.ID, err), req.ID, false, true
		}
		return nil, req.ID, true, true
	case approval.Declined:
		if _, err := e.approvals.Decide(req.ID, false, by); err == nil {
			e.log.Info("approval decided", "id", req.ID, "status", approval.Denied, "by", by, "tool", s.tool)
		}
		r, _ := toolError("NOT EXECUTED: the user declined this request.", "Do not retry. Tell the user you did not do it.")
		return r, req.ID, false, true
	case approval.Unanswered:
	}
	return nil, "", false, false
}

func refuseApproval(id string, err error) *mcp.CallToolResult {
	next := map[error]string{
		approval.ErrPending:  "Wait for the operator to approve it, then call again.",
		approval.ErrDenied:   "Do not retry. Tell the operator the request was denied.",
		approval.ErrUsed:     "That approval permitted one call and it has run. Request a new approval for another call.",
		approval.ErrInFlight: "Another call is using this approval now. Wait for its result; call again only if it failed before reaching the system.",
		approval.ErrExpired:  "Request a new approval by calling again without approvalId.",
		approval.ErrMismatch: "Call again with exactly the arguments that were approved, or request a new approval.",
		approval.ErrUnknown:  "Call without approvalId to request an approval.",
	}
	for known, action := range next {
		if errors.Is(err, known) {
			res, _ := toolError(fmt.Sprintf("NOT EXECUTED: approval %s cannot be used: %v.", id, err), action)
			return res
		}
	}
	res, _ := toolError(fmt.Sprintf("NOT EXECUTED: approval %s cannot be used: %v.", id, err), "Tell the operator.")
	return res
}

// compactArgs is the arguments as a one-line summary shows them: masked and
// clipped the way a person approving sees them.
func compactArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	return redact.PreviewJSON(args, false)
}
