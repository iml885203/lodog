package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/spec"
)

// forwarder turns a tool call into an upstream request.
type forwarder struct {
	upstream *Upstream
	level    casecfg.Level
	identity casecfg.IdentityConfig
	labels   Labels
	enforcer *enforcer // nil below L3: nothing is held or refused
	log      *slog.Logger
}

// Labels file calls under a benchmark run when the client cannot send them in
// _meta itself — a headless Claude Code run, for one. A label in _meta wins.
type Labels struct {
	Run     string
	Task    string
	Attempt string
}

// Meta keys a benchmark runner sets on a tools/call so the recording proxy can
// file the call under its run and task (docs/contracts.md §1).
const (
	metaRun     = "lodog/run"
	metaTask    = "lodog/task"
	metaAttempt = "lodog/attempt"
)

func (f forwarder) handler(t serving.RawTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return toolError("The arguments are not a JSON object.", "Send the arguments as an object matching the tool's input schema.")
			}
		}
		out, err := f.prepare(ctx, t.Tool.Name, t.Op, args, req.Params.Meta)
		var refused *refusal
		if errors.As(err, &refused) {
			return toolError(refused.what, refused.next)
		}
		if err != nil {
			return nil, err
		}
		return f.send(ctx, t, out)
	}
}

// outgoing is a tool call built into the request the legacy system would get.
type outgoing struct {
	req    *http.Request
	path   string // the concrete path, parameters filled in
	callID string
}

// refusal is a call the gateway turns back to the agent before sending it.
type refusal struct{ what, next string }

func (r *refusal) Error() string { return r.what }

// prepare builds the upstream request from the arguments. A call whose
// arguments cannot make a request is a *refusal, with a log line saying why,
// since it never reaches the decision or forwarding lines.
func (f forwarder) prepare(ctx context.Context, tool string, op spec.Operation, args map[string]any, meta mcp.Meta) (*outgoing, error) {
	invalid := func(err error, next string) (*outgoing, error) {
		f.log.Info("tool call refused", "tool", tool, "reason", err.Error(), "forwarded", false,
			"actor", f.identity.Actor, "on_behalf_of", f.identity.OnBehalfOf)
		return nil, &refusal{what: err.Error(), next: next}
	}
	path, query, body, contentType, err := buildCall(op, args)
	if err != nil {
		return invalid(err, "Check the tool's input schema and send every required argument.")
	}
	header, err := buildHeaders(op, args)
	if err != nil {
		return invalid(err, "Check the tool's input schema and send plain values.")
	}
	httpReq, err := f.upstream.NewRequest(ctx, op.Method, path, query, body)
	if err != nil {
		return nil, err
	}
	// The body goes out in the type the document declared, so a server
	// that insists on application/vnd.api+json gets it; a multipart body
	// adds its boundary.
	if body != nil {
		httpReq.Header.Set("Content-Type", contentType)
	}
	// Set before the gateway's own headers, so an argument can never
	// override a trace header or a credential.
	for name, values := range header {
		httpReq.Header[name] = values
	}
	callID, err := newCallID()
	if err != nil {
		return nil, &refusal{what: "The gateway could not record this call, so it was not sent.", next: "Retry once later; if it fails again, tell the operator."}
	}
	f.setTraceHeaders(httpReq.Header.Set, meta)
	httpReq.Header.Set(headerCall, callID)
	return &outgoing{req: httpReq, path: path, callID: callID}, nil
}

// decided is the L3 decision line, written before anything is forwarded so a
// held or refused call leaves a trace too. forwarded is true only for a call
// the gateway goes on to send.
func (f forwarder) decided(s subject, approval string, forwarded bool) {
	if f.enforcer == nil {
		return
	}
	f.log.Info("tool call decided", "tool", s.tool, "action", s.decision.Action, "risk", s.decision.Risk,
		"approval", approval, "forwarded", forwarded, "actor", f.identity.Actor, "on_behalf_of", f.identity.OnBehalfOf)
}

// send forwards a built call of a one-to-one tool and shows the agent the
// legacy system's answer.
func (f forwarder) send(ctx context.Context, t serving.RawTool, out *outgoing) (*mcp.CallToolResult, error) {
	resp, refused, err := f.transmit(ctx, subject{tool: t.Tool.Name}, t.Op, out, "", false, true)
	if refused != nil || err != nil {
		return refused, err
	}
	text := fmt.Sprintf("HTTP %d\n%s", resp.Status, resp.Body)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: resp.Status >= 400,
	}, nil
}

// transmit sends a built call and returns the legacy system's answer with
// held secrets masked, or the result the agent gets instead when no answer
// came. A reserved approval is spent once the request may have reached the
// legacy system, and returned if it surely did not. logDecision writes the
// decision line; a task tool writes it for its first step only.
func (f forwarder) transmit(ctx context.Context, s subject, op spec.Operation, out *outgoing, approval string, reserved, logDecision bool) (*Response, *mcp.CallToolResult, error) {
	settle := func(wasSent bool) {
		switch {
		case !reserved:
		case wasSent:
			f.enforcer.approvals.Commit(approval)
		default:
			f.enforcer.approvals.Release(approval)
		}
	}
	// Signed in before the decision line, so forwarded=true is never logged
	// for a call that a refused or unreachable sign-in kept from being sent.
	if err := f.upstream.EnsureSignedIn(ctx); err != nil {
		settle(false)
		if logDecision {
			f.decided(s, approval, false)
		}
		res, ferr := f.failed(s.tool, out.callID, 0, false, err)
		return nil, res, ferr
	}
	if logDecision {
		f.decided(s, approval, true)
	}
	if approval != "" {
		out.req.Header.Set("X-Lodog-Approval", approval)
	}
	// Recorded before anything reaches the legacy system, at every level,
	// so a call that times out or kills the process still left a trace.
	// The closing line below carries the same call_id.
	f.log.Info("tool call forwarding", "call_id", out.callID, "tool", s.tool, "method", op.Method, "path", op.Path,
		"level", f.level, "actor", f.identity.Actor, "on_behalf_of", f.identity.OnBehalfOf, "approval", approval)
	started := time.Now()
	resp, err := f.upstream.Do(ctx, out.req)
	elapsed := time.Since(started).Milliseconds()
	settle(err == nil || sent(err))
	if err != nil {
		res, ferr := f.failed(s.tool, out.callID, elapsed, sent(err), err)
		return nil, res, ferr
	}
	f.log.Info("tool call", "call_id", out.callID, "tool", s.tool, "method", op.Method, "path", op.Path,
		"status", resp.Status, "duration_ms", elapsed)
	// Held secret values are masked whatever the status; names only on an
	// error page, which often echoes the request back and where a
	// look-alike word may be masked by mistake.
	hide := f.upstream.Redaction()
	shown := hide.Value(string(resp.Body))
	if resp.Status >= 400 {
		shown = hide.Text(shown)
	}
	masked := *resp
	masked.Body = []byte(shown)
	return &masked, nil, nil
}

// failed reports a call the legacy system did not answer. wasSent says
// whether the request may have reached it.
func (f forwarder) failed(tool, callID string, elapsed int64, wasSent bool, err error) (*mcp.CallToolResult, error) {
	if errors.Is(err, ErrLoginFailed) {
		f.log.Error("tool call failed: upstream login refused; the gateway will not sign in again",
			"call_id", callID, "tool", tool, "duration_ms", elapsed, "sent", wasSent)
		return toolError("The gateway could not sign in to the legacy system.",
			"Stop and tell the operator: the gateway's upstream credentials were refused, and it will not retry.")
	}
	// A Go URL error carries the whole URL, query credentials included: the
	// log gets it masked, the agent gets the reason only.
	hide := f.upstream.Redaction()
	f.log.Error("tool call failed", "call_id", callID, "tool", tool, "duration_ms", elapsed, "sent", wasSent, "err", hide.Text(err.Error()))
	return toolError("The legacy system could not be reached: "+hide.Text(reason(err)), "Retry once later; if it fails again, tell the operator.")
}

// reason is why a request failed, without the URL a *url.Error wraps it in.
func reason(err error) string {
	var ue *url.Error
	for errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// headerCall carries the gateway's call id to the recording proxy, which files
// its own record under the same id (docs/contracts.md §1).
const headerCall = "X-Lodog-Call"

// newCallID names one forwarded call. The opening and closing log lines, and
// the proxy's records, share it so a call is never counted twice.
func newCallID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("call id: %w", err)
	}
	return "call_" + hex.EncodeToString(b), nil
}

func (f forwarder) setTraceHeaders(set func(string, string), meta mcp.Meta) {
	set("X-Lodog-Level", string(f.level))
	set("X-Lodog-Actor", f.identity.Actor)
	if f.identity.OnBehalfOf != "" {
		set("X-Lodog-On-Behalf-Of", f.identity.OnBehalfOf)
	}
	defaults := map[string]string{metaRun: f.labels.Run, metaTask: f.labels.Task, metaAttempt: f.labels.Attempt}
	for key, header := range map[string]string{metaRun: "X-Lodog-Run", metaTask: "X-Lodog-Task", metaAttempt: "X-Lodog-Attempt"} {
		if v, ok := meta[key]; ok {
			set(header, fmt.Sprint(v))
		} else if d := defaults[key]; d != "" {
			set(header, d)
		}
	}
}

// buildCall maps tool arguments onto the operation: path parameters into the
// path, query parameters into the query string, requestBody into the body in
// its declared type, returned with the Content-Type to send (header and
// cookie parameters: buildHeaders).
// A missing required argument is refused here rather than sent half-formed.
func buildCall(op spec.Operation, args map[string]any) (string, url.Values, []byte, string, error) {
	path := op.Path
	query := url.Values{}
	for _, p := range op.Params {
		v, ok := args[p.ArgName()]
		if !ok || v == nil {
			if p.Required {
				return "", nil, nil, "", fmt.Errorf("argument %q is required", p.ArgName())
			}
			continue
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(scalar(v)))
		case "query":
			if list, isList := v.([]any); isList {
				for _, item := range list {
					query.Add(p.Name, scalar(item))
				}
			} else {
				query.Set(p.Name, scalar(v))
			}
		}
	}
	v, ok := args[serving.BodyArg]
	if !ok || v == nil {
		if op.BodyRequired {
			return "", nil, nil, "", fmt.Errorf("argument %q is required", serving.BodyArg)
		}
		return path, query, nil, "", nil
	}
	body, contentType, err := encodeBody(op, v)
	if err != nil {
		return "", nil, nil, "", err
	}
	return path, query, body, contentType, nil
}

// scalar renders a JSON value as a URL value. Whole numbers print without a
// decimal point, because JSON decoding makes every number a float64.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprint(x)
	default:
		return fmt.Sprint(x)
	}
}

// badApprovalID refuses an approvalId that is not a string. Reading it as
// absent would hold the call again and leave the agent unaware its argument was
// wrong.
func badApprovalID(args map[string]any) *mcp.CallToolResult {
	v, given := args[serving.ApprovalArg]
	if _, isString := v.(string); !given || v == nil || isString {
		return nil
	}
	res, _ := toolError(fmt.Sprintf("Argument %q must be the approval id as a string, got %v.", serving.ApprovalArg, v),
		"Send approvalId exactly as the gateway gave it, or leave it out to request a new approval.")
	return res
}

// toolError is a refusal the agent can act on: what went wrong, and what to
// do next. A verdict without a next action is not allowed (CLAUDE.md).
//
// It is a tool result, never a Go error: MCP reports a failed tool execution
// inside the result (isError) so the model can read it and correct itself,
// and keeps protocol errors for a broken conversation.
func toolError(what, next string) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: what + "\nNext: " + next}},
		IsError: true,
	}, nil
}
