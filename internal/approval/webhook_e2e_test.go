package approval_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/gateway"
)

const (
	e2eDoc    = `{"openapi":"3.0.4","paths":{"/api/payment/withdraw/reject":{"post":{"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"refNo":{"type":"string"}}}}}}}}}}`
	e2ePolicy = `
risks: {money: approval}
default: money
rules: [{match: "POST /api/payment/**", risk: money}]
`
	e2eOverlay   = "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n"
	e2eWorkflows = `arazzo: 1.0.1
info: {title: t, version: "1", description: Payments.}
sourceDescriptions: [{name: api, url: openapi.json, type: openapi}]
workflows:
  - workflowId: reject_withdrawal
    summary: Reject a withdrawal.
    inputs: {type: object, properties: {requestBody: {type: object}}, required: [requestBody]}
    steps:
      - stepId: reject
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1api~1payment~1withdraw~1reject/post'
        requestBody: {payload: $inputs.requestBody}
`
)

// stack is a real L3 gateway with the webhook channel in front of a fake
// legacy system, with a fake Slack webhook and an MCP client playing the agent.
type stack struct {
	page    *page
	gw      *gateway.Gateway
	admin   *httptest.Server
	agent   *mcp.ClientSession
	logs    *bytes.Buffer
	reached atomic.Int32
	mu      sync.Mutex
	seen    []string // everything the agent was told
}

func newStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(&syncWriter{w: s.logs, mu: &s.mu}, nil))
	s.page = newPage(t, log)
	s.page.now = time.Now() // the gateway stamps real expiry times
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.reached.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(legacy.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"openapi.json": e2eDoc, "policy.yaml": e2ePolicy, "overlay.yaml": e2eOverlay, "workflows.arazzo.yaml": e2eWorkflows} {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L3, Listen: "127.0.0.1:0", Upstream: legacy.URL,
		Auth:      casecfg.AuthConfig{Type: "api-key", KeyEnv: "K", Header: "X-Key"},
		Identity:  casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
		Approvals: casecfg.ApprovalsConfig{Listen: "127.0.0.1:1", Mode: casecfg.ModeWebhook},
	}
	gw, err := gateway.New(cfg, func(string) string { return "k" }, gateway.Labels{}, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	s.gw = gw
	// The page hands decisions to the gateway exactly as Serve would.
	s.page.srvHandler(s.page.web.Handler(gw.ApprovalHandler()))
	gw.SetNotifier(s.page.web)
	mcpSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(mcpSrv.Close)
	s.admin = httptest.NewServer(gw.AdminHandler())
	t.Cleanup(s.admin.Close)
	s.agent, err = mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: mcpSrv.URL + gateway.MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.agent.Close() })
	return s
}

type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *stack) call(t *testing.T, args map[string]any) (bool, string) {
	t.Helper()
	res, err := s.agent.CallTool(context.Background(), &mcp.CallToolParams{Name: "reject_withdrawal", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	s.mu.Lock()
	s.seen = append(s.seen, text)
	s.mu.Unlock()
	return res.IsError, text
}

var apr = regexp.MustCompile(`apr_[0-9a-f]{16}`)

func (s *stack) hold(t *testing.T) (id, path, token string) {
	t.Helper()
	isErr, text := s.call(t, map[string]any{"requestBody": map[string]any{"refNo": "W-1"}, "approvalReason": "Customer asked support."})
	if isErr || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want the call held: %v %s", isErr, text)
	}
	sent := s.page.hook.sent()
	m := linkPattern.FindStringSubmatch(sent[len(sent)-1])
	if m == nil || m[2] != apr.FindString(text) {
		t.Fatalf("no link for the held request in %v", sent)
	}
	return m[2], "/approve/" + m[2] + "/" + m[3], m[3]
}

func (s *stack) retry(t *testing.T, id string) (bool, string) {
	return s.call(t, map[string]any{"requestBody": map[string]any{"refNo": "W-1"}, "approvalId": id})
}

func TestAWebhookApprovalReleasesTheCallOnceAndTheTokenNeverReachesTheAgentOrTheLog(t *testing.T) {
	s := newStack(t)
	id, path, token := s.hold(t)
	if isErr, text := s.retry(t, id); !isErr || !strings.Contains(text, "still waiting") {
		t.Fatalf("a call went through before anyone decided: %v %s", isErr, text)
	}
	if s.reached.Load() != 0 {
		t.Fatal("forwarded before approval")
	}
	if code, body := s.page.approve(t, path, "Alice"); code != http.StatusOK || !strings.Contains(body, "已核准") {
		t.Fatalf("%d %s", code, body)
	}
	for range 3 {
		s.retry(t, id)
	}
	if n := s.reached.Load(); n != 1 {
		t.Fatalf("the legacy system got %d calls, want exactly 1", n)
	}
	if isErr, text := s.retry(t, id); !isErr || !strings.Contains(text, "already used") {
		t.Fatalf("replay: %v %s", isErr, text)
	}
	if code, _, _ := get(t, s.page.srv.URL, path); code != http.StatusNotFound {
		t.Fatalf("the link outlived the approval: %d", code)
	}
	s.assertFollowUpsAndNoLeak(t, token)
}

func TestADeniedWebhookRequestTellsTheAgentToStop(t *testing.T) {
	s := newStack(t)
	id, path, _ := s.hold(t)
	if code, _ := post(t, s.page.srv.URL, path, url.Values{"csrf": {s.page.nonce(t, path)}, "name": {"Bo"}, "verdict": {"deny"}}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if isErr, text := s.retry(t, id); !isErr || !strings.Contains(text, "denied") || !strings.Contains(text, "Do not retry") {
		t.Fatalf("%v %s", isErr, text)
	}
	if s.reached.Load() != 0 {
		t.Fatal("a denied call was forwarded")
	}
}

func TestInWebhookModeOnlyTheLinkCanApprove(t *testing.T) {
	s := newStack(t)
	id, path, _ := s.hold(t)
	for _, verb := range []string{"approve", "deny"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.admin.URL+"/approvals/"+id+"/"+verb, strings.NewReader(`{"by":"user:me"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s through the admin endpoint answered 200: it must not decide", verb)
		}
	}
	// A decision aimed at the page without the link's token, or with someone
	// else's, does nothing either.
	for _, bad := range []string{"/approve/" + id + "/" + strings.Repeat("A", 43), "/approve/" + id, "/approve/" + id + "/"} {
		post(t, s.page.srv.URL, bad, url.Values{"csrf": {"x"}, "name": {"Mallory"}, "verdict": {"approve"}}, nil)
	}
	if isErr, text := s.retry(t, id); !isErr || !strings.Contains(text, "still waiting") || s.reached.Load() != 0 {
		t.Fatalf("something other than the link approved: %v %s (reached %d)", isErr, text, s.reached.Load())
	}
	if code, _ := s.page.approve(t, path, "Alice"); code != http.StatusOK {
		t.Fatalf("the link itself was refused: %d", code)
	}
}

// assertFollowUpsAndNoLeak checks what the approvers were told afterwards and
// that neither the link nor its token reached the agent or the log.
func (s *stack) assertFollowUpsAndNoLeak(t *testing.T, token string) {
	t.Helper()
	// The follow-ups say who approved (typed name, marked as such) and that it was spent.
	posted := strings.Join(s.page.hook.sent(), "\n")
	if !strings.Contains(posted, "已核准") || !strings.Contains(posted, "Alice") || !strings.Contains(posted, "已用掉") {
		t.Errorf("follow-ups missing:\n%s", posted)
	}
	s.mu.Lock()
	agentSaw, logged := strings.Join(s.seen, "\n"), s.logs.String()
	s.mu.Unlock()
	if strings.Contains(agentSaw, token) || strings.Contains(logged, token) || strings.Contains(agentSaw, "/approve/") || strings.Contains(logged, "/approve/") {
		t.Fatalf("the link or token reached the agent or the log:\n%s\n%s", agentSaw, logged)
	}
	if !strings.Contains(logged, "by=web:Alice") {
		t.Errorf("the decision is not logged with who typed it:\n%s", logged)
	}
	if strings.Contains(agentSaw, "lodog approvals") {
		t.Errorf("the agent was pointed at the CLI:\n%s", agentSaw)
	}
}

func TestAWebhookThatIsDownTellsTheAgentNobodyWasAsked(t *testing.T) {
	s := newStack(t)
	s.page.hook.mu.Lock()
	s.page.hook.status = http.StatusInternalServerError
	s.page.hook.mu.Unlock()
	isErr, text := s.call(t, map[string]any{"requestBody": map[string]any{"refNo": "W-1"}})
	if isErr || !strings.HasPrefix(text, "NOT EXECUTED") || !strings.Contains(text, "approval notification was not sent") || !strings.Contains(text, "without approvalId") {
		t.Fatalf("the agent is not told the notification failed: %v %s", isErr, text)
	}
	if strings.Contains(text, "After they approve") {
		t.Errorf("the agent is told to wait for an approval nobody was asked for: %s", text)
	}
	s.mu.Lock()
	logged := s.logs.String()
	s.mu.Unlock()
	if !strings.Contains(logged, "approval notification was not sent") || strings.Contains(logged, "decides it another way") {
		t.Errorf("the log does not say nobody was asked:\n%s", logged)
	}
	if strings.Contains(logged, "hookSecret8f3a") {
		t.Errorf("the webhook URL reached the log:\n%s", logged)
	}
	if s.reached.Load() != 0 {
		t.Fatal("a call whose approval was never requested was forwarded")
	}
	// Trying again, once the webhook is back, sends a fresh message.
	s.page.hook.mu.Lock()
	s.page.hook.status = http.StatusOK
	s.page.hook.mu.Unlock()
	if isErr, text := s.call(t, map[string]any{"requestBody": map[string]any{"refNo": "W-1"}}); isErr || !strings.Contains(text, "approvers were sent a message") {
		t.Fatalf("the retry did not ask anyone: %v %s", isErr, text)
	}
}
