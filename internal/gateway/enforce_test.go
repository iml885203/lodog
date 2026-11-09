package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/approval"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const l3Doc = `{"openapi":"3.0.4","paths":{
	"/api/payment/transactions":{"get":{"parameters":[{"name":"PageSize","in":"query","schema":{"type":"integer"}}]}},
	"/api/payment/transaction/withdraw/reject":{"post":{"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"refNo":{"type":"string"}}}}}}}},
	"/api/customer/{customerId}/resetPassword":{"get":{"parameters":[{"name":"customerId","in":"path","required":true,"schema":{"type":"integer"}}]}}}}`

const l3Policy = `
risks: {read: allow, money: approval, credential: hide, write: approval}
default: write
rules:
  - {match: "GET /api/customer/*/resetPassword", risk: credential}
  - {match: "POST /api/payment/transaction/**", risk: money}
  - {match: "GET /**", risk: read}
`

// l3Workflows serves each operation as a task tool of the same name, so the
// L3 tests read as calls to the legacy operations they guard.
const l3Workflows = `arazzo: 1.0.1
info: {title: t, version: "1", description: Payments.}
sourceDescriptions: [{name: api, url: openapi.json, type: openapi}]
workflows:
  - workflowId: get_api_payment_transactions
    summary: List transactions.
    inputs: {type: object, properties: {PageSize: {type: integer, maximum: 50}}}
    steps:
      - stepId: list
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1payment~1transactions/get'
        parameters: [{name: PageSize, in: query, value: $inputs.PageSize}]
  - workflowId: post_api_payment_transaction_withdraw_reject
    summary: Reject a withdrawal.
    inputs: {type: object, properties: {requestBody: {type: object}}, required: [requestBody]}
    steps:
      - stepId: reject
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1payment~1transaction~1withdraw~1reject/post'
        requestBody: {payload: $inputs.requestBody}
  - workflowId: get_api_customer_by_customerId_resetPassword
    summary: Reset a password.
    inputs: {type: object, properties: {customerId: {type: integer}}, required: [customerId]}
    steps:
      - stepId: reset
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1customer~1{customerId}~1resetPassword/get'
        parameters: [{name: customerId, in: path, value: $inputs.customerId}]
`

const (
	reject = "post_api_payment_transaction_withdraw_reject"
	list   = "get_api_payment_transactions"
)

type l3 struct {
	gw      *Gateway
	legacy  *recorder
	session *mcp.ClientSession
	mcpURL  string
	admin   string
	logs    *lockedBuffer
}

func newL3(t *testing.T) *l3 {
	t.Helper()
	return newL3With(t, l3Policy)
}

func newL3With(t *testing.T, policyYAML string) *l3 {
	t.Helper()
	return newL3Upstream(t, policyYAML, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"}, nil)
}

// newL3Upstream serves the L3 gateway against a recorder, signing in with
// auth; wrap, when set, stands between the gateway and the recorder.
func newL3Upstream(t *testing.T, policyYAML string, auth casecfg.AuthConfig, wrap func(http.Handler) http.Handler) *l3 {
	t.Helper()
	return newL3Full(t, policyYAML, auth, wrap, nil, nil)
}

// newL3Full is newL3Upstream with the approval channel: tune edits the config
// before the gateway reads it, and channel, when set, is installed.
func newL3Full(t *testing.T, policyYAML string, auth casecfg.AuthConfig, wrap func(http.Handler) http.Handler, tune func(*casecfg.Config), channel approval.Notifier, clientOpts ...*mcp.ClientOptions) *l3 {
	t.Helper()
	return newL3Case(t, l3Doc, l3Workflows, policyYAML, auth, wrap, tune, channel, clientOpts...)
}

// newL3Case is newL3Full for another OpenAPI document and workflows file.
func newL3Case(t *testing.T, doc, workflows, policyYAML string, auth casecfg.AuthConfig, wrap func(http.Handler) http.Handler, tune func(*casecfg.Config), channel approval.Notifier, clientOpts ...*mcp.ClientOptions) *l3 {
	t.Helper()
	legacy := &recorder{token: "tok"}
	var handler http.Handler = legacy
	if wrap != nil {
		handler = wrap(legacy)
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	overlay := "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n"
	for name, body := range map[string]string{"openapi.json": doc, "policy.yaml": policyYAML, "overlay.yaml": overlay, "workflows.arazzo.yaml": workflows} {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L3, Listen: "127.0.0.1:0", Upstream: upstream.URL,
		Auth:      auth,
		Identity:  casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
		Approvals: casecfg.ApprovalsConfig{Listen: "127.0.0.1:1"},
	}
	cfg.Approvals.InProcess = true // these tests decide in-process, as the benchmark runner does
	if tune != nil {
		tune(cfg)
	}
	logs := &lockedBuffer{}
	gw, err := New(cfg, func(string) string { return "k" }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if channel != nil {
		gw.SetNotifier(channel)
	}
	mcpSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(mcpSrv.Close)
	adminSrv := httptest.NewServer(gw.AdminHandler())
	t.Cleanup(adminSrv.Close)
	var copts *mcp.ClientOptions
	if len(clientOpts) > 0 {
		copts = clientOpts[0]
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, copts).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: mcpSrv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &l3{gw: gw, legacy: legacy, session: session, mcpURL: mcpSrv.URL, admin: adminSrv.URL, logs: logs}
}

func (h *l3) call(t *testing.T, tool string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.IsError, res.Content[0].(*mcp.TextContent).Text
}

// decide gives a verdict as the operator would through any channel; it answers
// 200, or 409 when the approval can no longer be decided.
func (h *l3) decide(t *testing.T, id, verb string) int {
	t.Helper()
	if _, err := h.gw.approvals.Decide(id, verb == "approve", "user:lead"); err != nil {
		return http.StatusConflict
	}
	return http.StatusOK
}

func (h *l3) forwarded(path string) []*http.Request {
	h.legacy.mu.Lock()
	defer h.legacy.mu.Unlock()
	var out []*http.Request
	for _, r := range h.legacy.seen {
		if r.URL.Path == path {
			out = append(out, r)
		}
	}
	return out
}

var approvalID = regexp.MustCompile(`apr_[0-9a-f]{16}`)

func holdReject(t *testing.T, h *l3) string {
	t.Helper()
	isErr, text := h.call(t, reject, map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}})
	if isErr || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want the call held, got %v: %s", isErr, text)
	}
	id := approvalID.FindString(text)
	if id == "" {
		t.Fatalf("no approval id in %s", text)
	}
	return id
}

func TestAHiddenOperationIsNotOffered(t *testing.T) {
	h := newL3(t)
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tool.Name, "resetPassword") {
			t.Fatalf("%s is offered; the policy hides it", tool.Name)
		}
		if strings.Contains(strings.ToLower(tool.Name), "approv") {
			t.Fatalf("%s is offered: the agent must have no way to approve its own calls", tool.Name)
		}
	}
}

func TestAReadIsForwardedWithoutApproval(t *testing.T) {
	h := newL3(t)
	if isErr, text := h.call(t, list, map[string]any{"PageSize": 5}); isErr {
		t.Fatalf("read refused: %s", text)
	}
	if len(h.forwarded("/api/payment/transactions")) != 1 {
		t.Fatal("the read did not reach the legacy system")
	}
}

func TestAMoneyCallIsHeldUntilApprovedAndRunsOnce(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	if n := len(h.forwarded("/api/payment/transaction/withdraw/reject")); n != 0 {
		t.Fatalf("a held call reached the legacy system %d times", n)
	}
	if status := h.decide(t, id, "approve"); status != http.StatusOK {
		t.Fatalf("approve: %d", status)
	}
	args := map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}, "approvalId": id}
	if isErr, text := h.call(t, reject, args); isErr {
		t.Fatalf("approved call refused: %s", text)
	}
	sent := h.forwarded("/api/payment/transaction/withdraw/reject")
	if len(sent) != 1 || sent[0].Header.Get("X-Lodog-Approval") != id {
		t.Fatalf("forwarded %d times, approval header %q", len(sent), sent[0].Header.Get("X-Lodog-Approval"))
	}
	if isErr, text := h.call(t, reject, args); !isErr || !strings.Contains(text, "already used") {
		t.Fatalf("replay: %v %s", isErr, text)
	}
	if n := len(h.forwarded("/api/payment/transaction/withdraw/reject")); n != 1 {
		t.Fatalf("an approval was spent twice: %d calls", n)
	}
}

func TestAnApprovalDoesNotCoverDifferentArguments(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	h.decide(t, id, "approve")
	isErr, text := h.call(t, reject, map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-CAROL-01"}, "approvalId": id})
	if !isErr || len(h.forwarded("/api/payment/transaction/withdraw/reject")) != 0 {
		t.Fatalf("approval for bob's withdrawal used on carol's: %v %s", isErr, text)
	}
}

func TestADeniedCallNeverRuns(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	h.decide(t, id, "deny")
	isErr, text := h.call(t, reject, map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}, "approvalId": id})
	if !isErr || !strings.Contains(text, "denied") || len(h.forwarded("/api/payment/transaction/withdraw/reject")) != 0 {
		t.Fatalf("denied call: %v %s", isErr, text)
	}
}

func TestThePendingListShowsWhoAsked(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.admin+"/approvals", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var pending []approval.Request
	if err := json.NewDecoder(resp.Body).Decode(&pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].OnBehalfOf != "user:alice" || pending[0].Risk != "money" {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestTheMCPEndpointDoesNotServeApprovals(t *testing.T) {
	h := newL3(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, h.mcpURL+"/approvals", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /approvals on the MCP listener = %d, want 404", resp.StatusCode)
	}
}

func approveAll(approval.Request) bool { return true }

// The policy below names its strictest risk "settled": the gateway reads the
// attribute, so no name is reserved.
const neverAutoPolicy = `
risks:
  read: allow
  write: approval
  credential: hide
  settled: {action: approval, never_auto_approve: true}
default: write
rules:
  - {match: "GET /api/customer/*/resetPassword", risk: credential}
  - {match: "POST /api/payment/transaction/**", risk: settled}
  - {match: "GET /**", risk: read}
`

func TestAnAutomatedApproverNeverApprovesANeverAutoApproveCall(t *testing.T) {
	h := newL3With(t, neverAutoPolicy)
	id := holdReject(t, h)
	decided, err := h.gw.DecidePending(approveAll, "agent:bench-operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 1 || decided[0].ID != id || decided[0].Status != approval.Denied || !decided[0].NeverAutoApprove {
		t.Fatalf("decided = %+v: a never_auto_approve call is denied whatever the automated approver says", decided)
	}
	second := holdReject(t, h)
	if code := h.decide(t, second, "approve"); code != http.StatusOK {
		t.Fatalf("a person approving through the admin endpoint got %d, want 200", code)
	}
}

func TestAnAutomatedApproverMayApproveACallWithoutTheAttribute(t *testing.T) {
	h := newL3(t)
	holdReject(t, h)
	decided, err := h.gw.DecidePending(approveAll, "agent:bench-operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 1 || decided[0].Status != approval.Approved {
		t.Fatalf("decided = %+v: a money call without never_auto_approve follows the approver", decided)
	}
	if !strings.Contains(h.logs.String(), "no risk has never_auto_approve") {
		t.Fatalf("logs = %s: a policy with no never_auto_approve risk is warned about at start", h.logs.String())
	}
}
