package gateway

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/policy"
)

var nextAction = regexp.MustCompile(`Next: \S`)

// A verdict is never ambiguous: a call held for approval says why it was held
// (the risk and the call) and what the agent does next.
func TestNeedsApprovalAlwaysCarriesAReasonAndANextAction(t *testing.T) {
	h := newL3(t)
	isErr, text := h.call(t, reject, map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}})
	if isErr {
		t.Fatalf("a held call is not an error: %s", text)
	}
	for _, want := range []string{"NOT EXECUTED: needs approval.", "Approval id: apr_", "Risk: money", "POST /api/payment/transaction/withdraw/reject"} {
		if !strings.Contains(text, want) {
			t.Errorf("held call lacks %q:\n%s", want, text)
		}
	}
	if !nextAction.MatchString(text) || !strings.Contains(text, "approvalId=") {
		t.Errorf("held call names no next action the agent can follow:\n%s", text)
	}
}

// An error is never an allow: when the gateway cannot decide, it refuses and
// nothing reaches the legacy system.
func TestWhenTheGatewayCannotDecideItRefusesAndForwardsNothing(t *testing.T) {
	signInRefused := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	bearer := casecfg.AuthConfig{Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P"}
	rejectArgs := map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}}
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		// approve first holds and approves the reject call, then calls with its id.
		approve bool
		// signIn refused: the gateway cannot act as the account at all.
		signInRefused bool
	}{
		"the sign-in is refused on a read":            {tool: list, args: map[string]any{"PageSize": 5}, signInRefused: true},
		"the sign-in is refused on an approved write": {tool: reject, args: rejectArgs, approve: true, signInRefused: true},
		"the approval id is not a string":             {tool: reject, args: map[string]any{"requestBody": map[string]any{"refNo": "x"}, "approvalId": 7}},
		"the approval id is unknown":                  {tool: reject, args: map[string]any{"requestBody": map[string]any{"refNo": "x"}, "approvalId": "apr_nope"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newL3(t)
			if tc.signInRefused {
				h = newL3Upstream(t, l3Policy, bearer, signInRefused)
			}
			args := tc.args
			if tc.approve {
				id := holdReject(t, h)
				h.decide(t, id, "approve")
				args = map[string]any{"requestBody": rejectArgs["requestBody"], "approvalId": id}
			}
			isErr, text := h.call(t, tc.tool, args)
			if !isErr || !nextAction.MatchString(text) {
				t.Fatalf("result = %v %q, want an error with a next action", isErr, text)
			}
			h.legacy.mu.Lock()
			defer h.legacy.mu.Unlock()
			if len(h.legacy.seen) != 0 {
				t.Fatalf("the legacy system received %d requests after a failed decision", len(h.legacy.seen))
			}
		})
	}
}

// refuse is the last gate before forwarding: every action that is not allow or
// approval ends in an error, including one the gateway does not know.
func TestOnlyAllowAndApprovalGoOnToTheLegacySystem(t *testing.T) {
	e := enforcer{}
	for action, onward := range map[policy.Action]bool{
		policy.Allow: true, policy.Approval: true,
		policy.Deny: false, policy.Hide: false, "": false, "allow-ish": false,
	} {
		res := e.refuse(subject{tool: "t", decision: policy.Decision{Action: action}})
		if onward != (res == nil) {
			t.Errorf("action %q: refuse = %v, want onward=%v", action, res, onward)
		}
		if res != nil && (!res.IsError || !nextAction.MatchString(resultText(t, res))) {
			t.Errorf("action %q: refusal %+v is not an error with a next action", action, res)
		}
	}
}
