package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

const taskDoc = `{"openapi":"3.0.4","info":{"title":"shop","version":"1"},"paths":{
 "/players":{"get":{"parameters":[{"name":"username","in":"query","schema":{"type":"string"}},
   {"name":"pageSize","in":"query","required":true,"schema":{"type":"integer"}}],
   "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","properties":{
     "data":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"}}}}}}}}}}}},
 "/players/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}},
 "/players/{id}/bonus":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"amount":{"type":"number"}}}}}}}},
 "/players/{id}/resetPassword":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}},
 "/audit":{"post":{}}
}}`

const taskWorkflows = `arazzo: 1.0.1
info: {title: shop tools, version: "1", description: Start with find_player; bonuses wait for approval.}
sourceDescriptions: [{name: shop, url: openapi.json, type: openapi}]
workflows:
  - workflowId: find_player
    summary: Find a player by username and read the first match.
    inputs:
      type: object
      properties:
        username: {type: string, description: The player's username.}
        pageSize: {type: integer, default: 10, maximum: 50}
      required: [username]
    steps:
      - stepId: search
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players/get'
        parameters:
          - {name: username, in: query, value: $inputs.username}
          - {name: pageSize, in: query, value: $inputs.pageSize}
        outputs: {id: '$response.body#/data/0/id'}
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}/get'
        parameters: [{name: id, in: path, value: $steps.search.outputs.id}]
    x-agent: {whenToUse: You have a username., whenNotToUse: You have the id., risk: read, next: [give_bonus]}
  - workflowId: list_players
    summary: List players.
    inputs: {type: object, properties: {pageSize: {type: integer, default: 10, maximum: 50}}}
    steps:
      - stepId: list
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players/get'
        parameters: [{name: pageSize, in: query, value: $inputs.pageSize}]
  - workflowId: give_bonus
    summary: Read a player, then give them a bonus.
    inputs:
      type: object
      properties: {id: {type: integer}, amount: {type: number}}
      required: [id, amount]
    steps:
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}/get'
        parameters: [{name: id, in: path, value: $inputs.id}]
      - stepId: give
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}~1bonus/post'
        parameters: [{name: id, in: path, value: $inputs.id}]
        requestBody: {payload: {amount: $inputs.amount}}
    x-agent: {risk: money}
  - workflowId: check_password_reset
    summary: Read a player and reset the password.
    inputs: {type: object, properties: {id: {type: integer}}, required: [id]}
    steps:
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}/get'
        parameters: [{name: id, in: path, value: $inputs.id}]
      - stepId: reset
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}~1resetPassword/get'
        parameters: [{name: id, in: path, value: $inputs.id}]
  - workflowId: write_audit
    summary: Write an audit entry.
    steps:
      - stepId: audit
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1audit/post'
`

// taskPolicy leaves POST /audit to the default, which must hold it.
const taskPolicy = `
risks: {read: allow, money: approval, credential: hide, write: approval}
default: write
rules:
  - {match: "GET /players/*/resetPassword", risk: credential}
  - {match: "POST /players/*/bonus", risk: money}
  - {match: "GET /**", risk: read}
`

// shop is a legacy system that answers each "METHOD /path" with a fixed
// status and body, and records what reached it.
type shop struct {
	mu      sync.Mutex
	answers map[string]answer
	seen    []*http.Request
	arrive  func()
}

type answer struct {
	status int
	body   string
}

func (s *shop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.arrive != nil {
		s.arrive()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r.Clone(r.Context()))
	a, ok := s.answers[r.Method+" "+r.URL.Path]
	if !ok {
		a = answer{status: http.StatusNotFound, body: `{"message":"no such thing"}`}
	}
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

func (s *shop) reached(method, path string) []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*http.Request
	for _, r := range s.seen {
		if r.Method == method && r.URL.Path == path {
			out = append(out, r)
		}
	}
	return out
}

type taskHarness struct {
	gw      *Gateway
	legacy  *shop
	session *mcp.ClientSession
	admin   string
	logs    *lockedBuffer
}

func newTaskGateway(t *testing.T, level casecfg.Level, legacy *shop) *taskHarness {
	t.Helper()
	return newTaskGatewayWith(t, level, legacy, taskDoc, taskWorkflows)
}

func newTaskGatewayWith(t *testing.T, level casecfg.Level, legacy *shop, doc, workflows string) *taskHarness {
	t.Helper()
	up := httptest.NewServer(legacy)
	t.Cleanup(up.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"openapi.json":          doc,
		"overlay.yaml":          "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n",
		"workflows.arazzo.yaml": workflows,
		"policy.yaml":           taskPolicy,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &casecfg.Config{
		Case: dir, Level: level, Listen: "127.0.0.1:0", Upstream: up.URL,
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"},
		Identity: casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
	}
	if level == casecfg.L3 {
		cfg.Approvals.Listen = "127.0.0.1:1"
	}
	logs := &lockedBuffer{}
	gw, err := New(cfg, func(string) string { return "k" }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	admin := httptest.NewServer(gw.AdminHandler())
	t.Cleanup(admin.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &taskHarness{gw: gw, legacy: legacy, session: session, admin: admin.URL, logs: logs}
}

func (h *taskHarness) call(t *testing.T, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res, res.Content[0].(*mcp.TextContent).Text
}

func envelope(t *testing.T, text string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, text)
	}
	return env
}

func bob() *shop {
	return &shop{answers: map[string]answer{
		"GET /players":          {200, `{"data":[{"id":7}]}`},
		"GET /players/7":        {200, `{"id":7,"name":"bob"}`},
		"POST /players/7/bonus": {200, `{"ok":true}`},
	}}
}

func TestL2ServesTheWorkflowsAsToolsWithTheirMap(t *testing.T) {
	h := newTaskGateway(t, casecfg.L2, bob())
	if got := h.session.InitializeResult().Instructions; got != "Start with find_player; bonuses wait for approval." {
		t.Fatalf("instructions = %q: the workflows' description is the agent's map", got)
	}
	var names []string
	var find *mcp.Tool
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
		if tool.Name == "find_player" {
			find = tool
		}
	}
	if len(names) != 5 || find == nil {
		t.Fatalf("tools = %v: L2 serves every workflow and nothing else", names)
	}
	for _, want := range []string{
		"Find a player", "Use when: You have a username.", "Do not use when: You have the id.",
		"Risk: read", "Calls: GET /players, GET /players/{id}", "Usually followed by: give_bonus",
	} {
		if !strings.Contains(find.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, find.Description)
		}
	}
}

func TestATaskToolReturnsAnOkEnvelopeWithDataAndNoReason(t *testing.T) {
	h := newTaskGateway(t, casecfg.L2, bob())
	res, text := h.call(t, "find_player", map[string]any{"username": "bob"})
	env := envelope(t, text)
	if res.IsError || env.Status != StatusOK || env.Error != nil {
		t.Fatalf("result %v: %s", res.IsError, text)
	}
	if env.Data.(map[string]any)["name"] != "bob" || len(env.Next) != 1 || env.Next[0] != "give_bonus" {
		t.Fatalf("envelope = %+v", env)
	}
	if res.StructuredContent == nil {
		t.Error("no structured content: a client reading structuredContent gets nothing")
	}
	search := h.legacy.reached("GET", "/players")
	if len(search) != 1 || search[0].URL.Query().Get("pageSize") != "10" || search[0].URL.Query().Get("username") != "bob" {
		t.Fatalf("search = %v: the default page size and the username reach the legacy system", search)
	}
}

func TestAnUnknownPlayerIsAnErrorWithANextActionNotAGuess(t *testing.T) {
	legacy := &shop{answers: map[string]answer{"GET /players": {200, `{"data":[]}`}}}
	h := newTaskGateway(t, casecfg.L2, legacy)
	res, text := h.call(t, "find_player", map[string]any{"username": "eve"})
	env := envelope(t, text)
	if !res.IsError || env.Status != StatusError || env.Error == nil || env.Error.Reason == "" || env.Error.NextAction == "" {
		t.Fatalf("result %v: %s", res.IsError, text)
	}
	if !strings.Contains(env.Error.NextAction, "do not guess") || env.Data != nil {
		t.Fatalf("envelope = %+v", env)
	}
	if n := len(legacy.seen); n != 1 {
		t.Fatalf("%d calls reached the legacy system, want only the search: the read has no id", n)
	}
}

func TestALegacyRefusalCarriesTheStepStatusAndANextAction(t *testing.T) {
	h := newTaskGateway(t, casecfg.L2, &shop{answers: map[string]answer{}})
	res, text := h.call(t, "list_players", nil)
	env := envelope(t, text)
	if !res.IsError || env.Error == nil || env.Error.HTTPStatus != 404 || env.Error.Step != "list" ||
		env.Error.Call != "GET /players" || !strings.Contains(env.Error.NextAction, "does not exist") {
		t.Fatalf("envelope = %s", text)
	}
	if env.Error.Detail.(map[string]any)["message"] != "no such thing" {
		t.Errorf("detail = %v: the legacy system's own message reaches the agent", env.Error.Detail)
	}
}

func TestBadArgumentsAreRefusedBeforeAnythingIsSent(t *testing.T) {
	h := newTaskGateway(t, casecfg.L2, bob())
	for name, args := range map[string]map[string]any{
		"page size above the cap": {"username": "bob", "pageSize": 500},
		"unknown argument":        {"username": "bob", "PageNumber": 2},
		"missing required":        {},
	} {
		res, text := h.call(t, "find_player", args)
		if !res.IsError || !strings.Contains(text, "Next:") {
			t.Errorf("%s: %v %s", name, res.IsError, text)
		}
	}
	if n := len(h.legacy.reached("GET", "/players")); n != 0 {
		t.Fatalf("%d refused calls reached the legacy system", n)
	}
}

func TestALongListIsCutToFitAndTheCutIsStated(t *testing.T) {
	rows := make([]string, 2000)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"id":%d,"note":"%s"}`, i, strings.Repeat("x", 40))
	}
	legacy := &shop{answers: map[string]answer{"GET /players": {200, `{"total":2000,"data":[` + strings.Join(rows, ",") + `]}`}}}
	h := newTaskGateway(t, casecfg.L2, legacy)
	res, text := h.call(t, "list_players", nil)
	env := envelope(t, text)
	if res.IsError || len(text) > maxTaskResult+1024 {
		t.Fatalf("result of %d bytes, error %v", len(text), res.IsError)
	}
	if len(env.Truncated) != 1 || env.Truncated[0].Path != "/data" || env.Truncated[0].Total != 2000 ||
		env.Truncated[0].Kept != len(env.Data.(map[string]any)["data"].([]any)) || env.Truncated[0].Kept == 0 {
		t.Fatalf("truncated = %+v", env.Truncated)
	}
	if env.Data.(map[string]any)["total"] != 2000.0 {
		t.Error("the fields beside the list were lost")
	}
}

func TestFitLeavesASmallValueAlone(t *testing.T) {
	v := map[string]any{"data": []any{1.0, 2.0}}
	got, cuts := fit(v, 1024)
	if cuts != nil || len(got.(map[string]any)["data"].([]any)) != 2 {
		t.Fatalf("%v %v", got, cuts)
	}
}

func TestFitCutsAValueWithNoListToItsStart(t *testing.T) {
	got, cuts := fit(map[string]any{"text": strings.Repeat("y", 5000)}, 100)
	partial, _ := got.(map[string]any)["partialJSON"].(string)
	if len(partial) != 100 || len(cuts) != 1 || cuts[0].Kept != 100 {
		t.Fatalf("%v %v", got, cuts)
	}
}

// Fail closed: a task tool is decided by its strictest step, so a read step
// cannot carry a write past the gate, and nothing runs while it is held.
func TestAnL3TaskIsHeldBeforeAnyStepRunsAndRunsOnceApproved(t *testing.T) {
	h := newTaskGateway(t, casecfg.L3, bob())
	args := map[string]any{"id": 7, "amount": 100}
	res, text := h.call(t, "give_bonus", args)
	if res.IsError || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want held, got %v: %s", res.IsError, text)
	}
	if len(h.legacy.seen) != 0 {
		t.Fatalf("%d steps reached the legacy system while the task was held", len(h.legacy.seen))
	}
	pending := h.gw.approvals.Pending()
	if len(pending) != 1 || pending[0].Method != "POST" || pending[0].Path != "/players/7/bonus" || pending[0].Risk != "money" ||
		!strings.Contains(pending[0].Summary, "POST /players/{id}/bonus body fields: amount") {
		t.Fatalf("pending = %+v: the person approving sees the write step and its body", pending)
	}
	id := approvalID.FindString(text)
	if _, err := h.gw.approvals.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	args["approvalId"] = id
	res, text = h.call(t, "give_bonus", args)
	if res.IsError || envelope(t, text).Status != StatusOK {
		t.Fatalf("approved run: %s", text)
	}
	gave := h.legacy.reached("POST", "/players/7/bonus")
	if len(gave) != 1 || gave[0].Header.Get("X-Lodog-Approval") != id {
		t.Fatalf("bonus calls %d", len(gave))
	}
	if res, _ := h.call(t, "give_bonus", args); !res.IsError {
		t.Fatal("an approval ran a task twice")
	}
}

func TestAnL3TaskWithAHiddenStepIsNotOffered(t *testing.T) {
	h := newTaskGateway(t, casecfg.L3, bob())
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "check_password_reset" {
			t.Fatal("a task with a step the policy hides is offered")
		}
	}
	if len(h.gw.Tasks()) != 4 {
		t.Fatalf("tasks = %d, want 4", len(h.gw.Tasks()))
	}
}

func TestAnL3TaskOnAnOperationNoRuleMatchesNeedsApproval(t *testing.T) {
	h := newTaskGateway(t, casecfg.L3, bob())
	res, text := h.call(t, "write_audit", nil)
	if res.IsError || !strings.HasPrefix(text, "NOT EXECUTED") || len(h.legacy.reached("POST", "/audit")) != 0 {
		t.Fatalf("an unclassified write was not held: %v %s", res.IsError, text)
	}
}

func TestAnL3ReadTaskRunsWithoutApproval(t *testing.T) {
	h := newTaskGateway(t, casecfg.L3, bob())
	res, text := h.call(t, "find_player", map[string]any{"username": "bob"})
	if res.IsError || envelope(t, text).Status != StatusOK {
		t.Fatalf("%v %s", res.IsError, text)
	}
}

func TestL2RecordsEveryStepBeforeForwardingIt(t *testing.T) {
	arrive, arrived, release := hold()
	legacy := &shop{answers: map[string]answer{"GET /players": {200, `{"data":[]}`}}, arrive: arrive}
	h := newTaskGateway(t, casecfg.L2, legacy)
	assertRecordedThenClosed(t, h.logs, arrived, release, func() {
		h.call(t, "list_players", nil)
	})
}

func TestTheL2AndL3ToolsAreTheSameApartFromApproval(t *testing.T) {
	l2 := newTaskGateway(t, casecfg.L2, bob())
	l3 := newTaskGateway(t, casecfg.L3, bob())
	schemas := func(h *taskHarness) map[string]map[string]any {
		out := map[string]map[string]any{}
		for tool, err := range h.session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
			delete(props, serving.ApprovalArg)
			delete(props, serving.ReasonArg)
			out[tool.Name] = props
		}
		return out
	}
	a, b := schemas(l2), schemas(l3)
	delete(a, "check_password_reset") // hidden by the L3 policy
	if len(a) != len(b) {
		t.Fatalf("L2 %d tools, L3 %d", len(a), len(b))
	}
	for name, props := range a {
		if fmt.Sprint(props) != fmt.Sprint(b[name]) {
			t.Errorf("%s differs: %v vs %v", name, props, b[name])
		}
	}
}

func TestAWorkflowInputNamedLikeTheApprovalArgumentIsRefused(t *testing.T) {
	wf := strings.Replace(taskWorkflows, "username: {type: string, description: The player's username.}",
		"approvalId: {type: string}", 1)
	wf = strings.Replace(wf, "required: [username]", "required: [approvalId]", 1)
	wf = strings.Replace(wf, "value: $inputs.username}", "value: $inputs.approvalId}", 1)
	doc, err := spec.Parse([]byte(taskDoc))
	if err != nil {
		t.Fatal(err)
	}
	d, err := workflow.Parse([]byte(wf))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := serving.TaskTools(doc, d); err == nil || !strings.Contains(err.Error(), "reserved for approvals") {
		t.Fatalf("err = %v: an input the gate strips would never reach the step", err)
	}
}

// Credentials never reach a record: a task tool's envelope carries the legacy
// system's answer, so it is masked like a one-to-one tool's, on an error page
// and in a 200 that echoes the request.
func TestATaskEnvelopeNeverCarriesTheCredential(t *testing.T) {
	for _, mode := range []string{"text500", "json500", "json200"} {
		for name, auth := range authSchemes {
			t.Run(mode+"/"+name, func(t *testing.T) {
				up := httptest.NewServer(&echoLegacy{mode: mode})
				t.Cleanup(up.Close)
				dir := t.TempDir()
				if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
					t.Fatal(err)
				}
				for file, body := range map[string]string{
					"openapi.json": taskDoc, "workflows.arazzo.yaml": taskWorkflows,
					"overlay.yaml": "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, "spec", file), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				cfg := &casecfg.Config{
					Case: dir, Level: casecfg.L2, Listen: "127.0.0.1:0", Upstream: up.URL, Auth: auth,
					Identity: casecfg.IdentityConfig{Actor: "agent:test"},
				}
				logs := &lockedBuffer{}
				gw, err := New(cfg, func(k string) string { return sentinels[k] }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil)))
				if err != nil {
					t.Fatal(err)
				}
				srv := httptest.NewServer(gw.Handler())
				t.Cleanup(srv.Close)
				session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
					Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = session.Close() })
				res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_players"})
				if err != nil {
					t.Fatal(err)
				}
				text := res.Content[0].(*mcp.TextContent).Text
				if !strings.Contains(text, `"status"`) {
					t.Fatalf("not an envelope: %s", text)
				}
				structured, err := json.Marshal(res.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				for where, what := range map[string]string{"text": text, "structured": string(structured), "log": logs.String()} {
					if found := leaks(what); len(found) > 0 {
						t.Fatalf("%s leaked %v:\n%s", where, found, what)
					}
				}
			})
		}
	}
}
