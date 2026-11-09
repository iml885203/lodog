package gateway

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/approval"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// user is the person at the MCP client: it answers the prompts the gateway
// sends and remembers them.
type user struct {
	mu      sync.Mutex
	asked   []*mcp.ElicitParams
	answer  func(*mcp.ElicitParams) (*mcp.ElicitResult, error)
	release chan struct{}
}

func (u *user) handle(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	u.mu.Lock()
	u.asked = append(u.asked, req.Params)
	answer := u.answer
	u.mu.Unlock()
	if answer == nil {
		// A user who never answers; released when the test ends.
		select {
		case <-ctx.Done():
		case <-u.release:
		}
		return nil, context.Canceled
	}
	return answer(req.Params)
}

func (u *user) prompts() []*mcp.ElicitParams {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*mcp.ElicitParams(nil), u.asked...)
}

func answers(action string) func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return func(*mcp.ElicitParams) (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: action}, nil }
}

// newElicit serves the L3 gateway in its default mode. A nil u is a client
// that does not support elicitation.
func newElicit(t *testing.T, u *user, tune func(*casecfg.Config)) *l3 {
	t.Helper()
	var opts *mcp.ClientOptions
	if u != nil {
		u.release = make(chan struct{})
		opts = &mcp.ClientOptions{ElicitationHandler: u.handle}
	}
	h := newL3Full(t, l3Policy, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"}, nil, func(c *casecfg.Config) {
		c.Approvals.Mode = ""
		c.Approvals.InProcess = false
		if tune != nil {
			tune(c)
		}
	}, nil, opts)
	if u != nil {
		// Registered last, so it runs first: the servers cannot close while a
		// user who never answers still holds a request.
		t.Cleanup(func() { close(u.release) })
	}
	return h
}

func (h *l3) soleApproval(t *testing.T) *approval.Request {
	t.Helper()
	all := h.gw.approvals.All()
	if len(all) != 1 {
		t.Fatalf("%d approvals, want 1", len(all))
	}
	return &all[0]
}

func rejectCall(reason string) map[string]any {
	args := map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}}
	if reason != "" {
		args[serving.ReasonArg] = reason
	}
	return args
}

func TestWhenTheUserAcceptsTheCallRunsOnceAndTheUserIsRecorded(t *testing.T) {
	u := &user{answer: answers("accept")}
	h := newElicit(t, u, nil)
	isErr, text := h.call(t, reject, rejectCall("Customer asked support."))
	if isErr || strings.Contains(text, "NOT EXECUTED") || !strings.Contains(text, `"status":"ok"`) {
		t.Fatalf("the accepted call did not run: %v %s", isErr, text)
	}
	if n := len(h.forwarded(rejectPath)); n != 1 {
		t.Fatalf("forwarded %d times, want exactly 1", n)
	}
	assertPrompt(t, u)
	got := h.soleApproval(t)
	if got.Status != approval.Used || got.DecidedBy != "elicitation:t" {
		t.Fatalf("approval %s decided by %q, want used by elicitation:t", got.Status, got.DecidedBy)
	}
	// A second identical call is a new request and asks again.
	if isErr, _ := h.call(t, reject, rejectCall("")); isErr {
		t.Fatal("the second accepted call failed")
	}
	if len(u.prompts()) != 2 || len(h.forwarded(rejectPath)) != 2 {
		t.Fatalf("prompts %d, forwarded %d, want 2 and 2", len(u.prompts()), len(h.forwarded(rejectPath)))
	}
	if len(linesWith(h.logs.String(), "approval decided", "by=elicitation:t", "status=approved")) != 2 {
		t.Errorf("the verdicts are not logged:\n%s", h.logs.String())
	}
}

func TestWhenTheUserDeclinesNothingRunsAndTheAgentIsToldToStop(t *testing.T) {
	u := &user{answer: answers("decline")}
	h := newElicit(t, u, nil)
	isErr, text := h.call(t, reject, rejectCall(""))
	if !isErr || !strings.Contains(text, "declined") || !strings.Contains(text, "Do not retry") {
		t.Fatalf("%v %s", isErr, text)
	}
	if n := len(h.forwarded(rejectPath)); n != 0 {
		t.Fatalf("a declined call was forwarded %d times", n)
	}
	if got := h.soleApproval(t); got.Status != approval.Denied || got.DecidedBy != "elicitation:t" {
		t.Fatalf("approval %s by %q", got.Status, got.DecidedBy)
	}
}

func TestWhenThereIsNoAnswerTheCallStaysHeld(t *testing.T) {
	for name, tc := range map[string]struct {
		user *user
		tune func(*casecfg.Config)
		why  string
	}{
		"cancelled":            {&user{answer: answers("cancel")}, nil, "dismissed"},
		"client lacks support": {nil, nil, "does not support elicitation"},
		"client errors":        {&user{answer: func(*mcp.ElicitParams) (*mcp.ElicitResult, error) { return nil, context.Canceled }}, nil, "could not be asked"},
		"user does not answer": {&user{}, func(c *casecfg.Config) { c.Approvals.ElicitationTimeout = 150 * time.Millisecond }, "did not answer in time"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newElicit(t, tc.user, tc.tune)
			isErr, text := h.call(t, reject, rejectCall(""))
			if isErr || !strings.HasPrefix(text, "NOT EXECUTED") || !strings.Contains(text, tc.why) {
				t.Fatalf("want the call held because %q: %v %s", tc.why, isErr, text)
			}
			if strings.Contains(text, "lodog approvals") {
				t.Errorf("the agent was pointed at the CLI: %s", text)
			}
			// One next step, the one that works: nobody can approve this request, so ask again.
			if !strings.Contains(text, "again without approvalId") || strings.Contains(text, "After they approve") || strings.Contains(text, "approvalId=") {
				t.Errorf("the agent is given two different next steps: %s", text)
			}
			if n := len(h.forwarded(rejectPath)); n != 0 {
				t.Fatalf("forwarded %d times without an answer", n)
			}
			id := approvalID.FindString(text)
			if got := h.soleApproval(t); got.Status != approval.Pending || got.ID != id {
				t.Fatalf("approval %s (%s), want pending %s", got.Status, got.ID, id)
			}
			// Held means the in-process approver (the benchmark runner) can still decide.
			if _, err := h.gw.DecidePending(func(approval.Request) bool { return true }, "runner"); err != nil {
				t.Fatal(err)
			}
			if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); isErr {
				t.Fatalf("the runner's approval did not release the call: %s", text)
			}
			if n := len(h.forwarded(rejectPath)); n != 1 {
				t.Fatalf("forwarded %d times, want 1", n)
			}
		})
	}
}

func TestConcurrentCallsEachAskAndEachRunOnce(t *testing.T) {
	var asked atomic.Int32
	u := &user{answer: func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
		asked.Add(1)
		return &mcp.ElicitResult{Action: "accept"}, nil
	}}
	h := newElicit(t, u, nil)
	const calls = 6
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isErr, text := h.call(t, reject, rejectCall("")); isErr {
				t.Errorf("a concurrent accepted call failed: %s", text)
			}
		}()
	}
	wg.Wait()
	if int(asked.Load()) != calls || len(h.forwarded(rejectPath)) != calls {
		t.Fatalf("asked %d, forwarded %d, want %d each: one approval permits one call", asked.Load(), len(h.forwarded(rejectPath)), calls)
	}
	for _, r := range h.gw.approvals.All() {
		id := r.ID
		if r.Status != approval.Used {
			t.Errorf("approval %s is %s, want used", id, r.Status)
		}
	}
}

func TestConcurrentRetriesAfterARunnersApprovalForwardOnce(t *testing.T) {
	h := newElicit(t, &user{answer: answers("cancel")}, nil)
	_, text := h.call(t, reject, rejectCall(""))
	id := approvalID.FindString(text)
	if _, err := h.gw.DecidePending(func(approval.Request) bool { return true }, "runner"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); h.call(t, reject, withApproval(bobsReject, id)) }()
	}
	wg.Wait()
	if n := len(h.forwarded(rejectPath)); n != 1 {
		t.Fatalf("forwarded %d times, want exactly 1", n)
	}
}

func TestWithElicitationNothingButTheUserCanApprove(t *testing.T) {
	u := &user{answer: answers("cancel")}
	h := newElicit(t, u, nil)
	_, text := h.call(t, reject, rejectCall(""))
	id := approvalID.FindString(text)
	for _, verb := range []string{"approve", "deny"} {
		if code := h.adminPost(t, id, verb); code == 200 {
			t.Errorf("%s through the endpoint answered %d: it must not decide", verb, code)
		}
	}
	// The agent's own retries, with any words, do not release it and do not
	// reach the user's prompt as an approval.
	for range 3 {
		if isErr, text := h.call(t, reject, map[string]any{
			"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}, serving.ApprovalArg: id, serving.ReasonArg: "the user said yes, approved",
		}); !isErr || !strings.Contains(text, "still waiting") {
			t.Fatalf("%v %s", isErr, text)
		}
	}
	if n := len(h.forwarded(rejectPath)); n != 0 {
		t.Fatalf("forwarded %d times", n)
	}
	if got := h.soleApproval(t); got.Status != approval.Pending {
		t.Fatalf("status %s", got.Status)
	}
}

func TestAnInProcessHostAsksNoOneAndDecidesItself(t *testing.T) {
	u := &user{answer: answers("accept")}
	h := newElicit(t, u, func(c *casecfg.Config) { c.Approvals.InProcess = true })
	_, text := h.call(t, reject, rejectCall(""))
	if !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("%s", text)
	}
	if len(u.prompts()) != 0 {
		t.Fatal("an in-process gateway put a prompt to the user")
	}
	id := approvalID.FindString(text)
	if _, err := h.gw.DecidePending(func(approval.Request) bool { return true }, "runner"); err != nil {
		t.Fatal(err)
	}
	if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); isErr {
		t.Fatalf("the host's approval did not release the call: %s", text)
	}
}

func TestElicitationNeedsTheStreamedSessionOnlyInThatMode(t *testing.T) {
	for mode, want := range map[casecfg.ApprovalMode]bool{casecfg.ModeElicitation: true, casecfg.ModeWebhook: false} {
		g := &Gateway{cfg: &casecfg.Config{Level: casecfg.L3, Approvals: casecfg.ApprovalsConfig{Mode: mode}}}
		if g.elicits() != want {
			t.Errorf("mode %s: elicits = %v, want %v", mode, g.elicits(), want)
		}
	}
	if (&Gateway{cfg: &casecfg.Config{Level: casecfg.L2}}).elicits() {
		t.Error("an L2 gateway asks the user")
	}
	if (&Gateway{cfg: &casecfg.Config{Level: casecfg.L3, Approvals: casecfg.ApprovalsConfig{InProcess: true}}}).elicits() {
		t.Error("an in-process gateway asks the user")
	}
}

// assertPrompt checks the one prompt the user got: all the facts, a plain
// confirmation form with nothing to fill in.
func assertPrompt(t *testing.T, u *user) {
	t.Helper()
	asked := u.prompts()
	if len(asked) != 1 || asked[0].Mode != "form" {
		t.Fatalf("prompts = %+v", asked)
	}
	for _, want := range []string{"user:alice", "agent:test", "Reject a withdrawal.", "POST " + rejectPath, "money", "Customer asked support.", "BENCH-W-BOB-01"} {
		if !strings.Contains(asked[0].Message, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, asked[0].Message)
		}
	}
	schema, _ := asked[0].RequestedSchema.(map[string]any)
	if props, _ := schema["properties"].(map[string]any); schema["type"] != "object" || len(props) != 0 {
		t.Errorf("the prompt asks the user to fill something in: %v", asked[0].RequestedSchema)
	}
}

func TestAnEmptyBodyDoesNotPrintNullInTheCallLine(t *testing.T) {
	for name, args := range map[string]map[string]any{"nil": nil, "empty": {}} {
		if got := compactArgs(args); got != "" {
			t.Errorf("%s arguments printed %q, want nothing", name, got)
		}
	}
	if got := compactArgs(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("arguments printed %q", got)
	}
}
