package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"
)

const rejectPath = "/api/payment/transaction/withdraw/reject"

var bobsReject = map[string]any{"requestBody": map[string]any{"refNo": "BENCH-W-BOB-01"}}

func withApproval(args map[string]any, id string) map[string]any {
	out := map[string]any{serving.ApprovalArg: id}
	for k, v := range args {
		out[k] = v
	}
	return out
}

// assertLogMatchesUpstream checks that the decision log never claims a call
// was forwarded unless the legacy system got it.
func assertLogMatchesUpstream(t *testing.T, h *l3) {
	t.Helper()
	logged := len(linesWith(h.logs.String(), "tool call decided", "tool="+reject, "forwarded=true"))
	if got := len(h.forwarded(rejectPath)); logged != got {
		t.Fatalf("the log says %d calls were forwarded, the legacy system got %d:\n%s", logged, got, h.logs.String())
	}
}

func TestACallWithInvalidArgumentsGetsNoApproval(t *testing.T) {
	h := newL3(t)
	isErr, text := h.call(t, reject, map[string]any{"body": map[string]any{"refNo": "BENCH-W-BOB-01"}})
	if !isErr || !strings.Contains(text, `argument "requestBody" is required`) {
		t.Fatalf("want the argument error, got %v: %s", isErr, text)
	}
	if approvalID.MatchString(text) || len(h.gw.approvals.Pending()) != 0 {
		t.Fatalf("a call that could never be sent asked a human for approval: %s", text)
	}
	if len(linesWith(h.logs.String(), "tool call refused", "tool="+reject, "requestBody")) != 1 {
		t.Fatalf("the refusal has no log line saying why:\n%s", h.logs.String())
	}
	assertLogMatchesUpstream(t, h)
}

func TestAnApprovedCallThatFailsBeforeSendingKeepsItsApproval(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	h.decide(t, id, "approve")
	isErr, text := h.call(t, reject, map[string]any{"body": map[string]any{"refNo": "BENCH-W-BOB-01"}, serving.ApprovalArg: id})
	if !isErr || !strings.Contains(text, "is required") {
		t.Fatalf("want the argument error, got %v: %s", isErr, text)
	}
	if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); isErr {
		t.Fatalf("the approval was lost to a call that was never sent: %s", text)
	}
	if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); !isErr || !strings.Contains(text, "already used") {
		t.Fatalf("replay: %v %s", isErr, text)
	}
	if n := len(h.forwarded(rejectPath)); n != 1 {
		t.Fatalf("forwarded %d times, want exactly 1", n)
	}
	assertLogMatchesUpstream(t, h)
}

func TestConcurrentRetriesWithOneApprovalForwardOnce(t *testing.T) {
	h := newL3(t)
	id := holdReject(t, h)
	h.decide(t, id, "approve")
	const retries = 8
	var refused atomic.Int32
	var wg sync.WaitGroup
	for range retries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isErr, _ := h.call(t, reject, withApproval(bobsReject, id)); isErr {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := len(h.forwarded(rejectPath)); n != 1 || refused.Load() != retries-1 {
		t.Fatalf("forwarded %d, refused %d of %d: one approval releases exactly one call", n, refused.Load(), retries)
	}
	assertLogMatchesUpstream(t, h)
}

// A sign-in that never got an answer sent nothing, so the approval stays
// usable and the log does not say the call was forwarded.
func TestASignInFailureBeforeSendingKeepsTheApproval(t *testing.T) {
	var down atomic.Bool
	h := newL3Upstream(t, l3Policy, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down.Load() {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	id := holdReject(t, h)
	h.decide(t, id, "approve")
	down.Store(true)
	if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); !isErr || !strings.Contains(text, "could not be reached") {
		t.Fatalf("want the unreachable error, got %v: %s", isErr, text)
	}
	if len(linesWith(h.logs.String(), "tool call failed", "sent=false")) != 1 {
		t.Fatalf("the unsent call is not logged as not forwarded:\n%s", h.logs.String())
	}
	assertLogMatchesUpstream(t, h)
	down.Store(false)
	if isErr, text := h.call(t, reject, withApproval(bobsReject, id)); isErr {
		t.Fatalf("the approval was lost to a call that was never sent: %s", text)
	}
	if n := len(h.forwarded(rejectPath)); n != 1 {
		t.Fatalf("forwarded %d times, want 1", n)
	}
	assertLogMatchesUpstream(t, h)
}

func TestNoPendingApprovalsListsAsAnEmptyArray(t *testing.T) {
	h := newL3(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.admin+"/approvals", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Fatalf("GET /approvals with nothing pending = %s, want []", got)
	}
}
