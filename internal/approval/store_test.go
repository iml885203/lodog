package approval

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/policy"
)

var who = casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"}

func held(t *testing.T, a *Store, args map[string]any) string {
	t.Helper()
	req, err := a.Hold("reject", "POST", "/api/payment/transaction/withdraw/reject", "reject BENCH-W-BOB-01", policy.Decision{Action: policy.Approval, Risk: "money"}, args, who)
	if err != nil {
		t.Fatal(err)
	}
	return req.ID
}

// spend is a call that was sent: the approval is reserved, then committed.
func spend(a *Store, id, tool string, args map[string]any) error {
	if err := a.Reserve(id, tool, args); err != nil {
		return err
	}
	a.Commit(id)
	return nil
}

func approved(t *testing.T, a *Store, args map[string]any) string {
	t.Helper()
	id := held(t, a, args)
	if _, err := a.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAReleasedApprovalCanStillPermitTheCall(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "X"}
	id := approved(t, a, args)
	if err := a.Reserve(id, "reject", args); err != nil {
		t.Fatal(err)
	}
	a.Release(id)
	if err := spend(a, id, "reject", args); err != nil {
		t.Fatalf("after a release the approval must still permit the call: %v", err)
	}
	if err := a.Reserve(id, "reject", args); !errors.Is(err, ErrUsed) {
		t.Fatalf("err = %v, want ErrUsed", err)
	}
}

func TestAReservedApprovalCannotBeClaimedTwice(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "X"}
	id := approved(t, a, args)
	if err := a.Reserve(id, "reject", args); err != nil {
		t.Fatal(err)
	}
	if err := a.Reserve(id, "reject", args); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second claim err = %v, want ErrInFlight", err)
	}
	a.Commit(id)
	a.Release(id)
	if err := a.Reserve(id, "reject", args); !errors.Is(err, ErrUsed) {
		t.Fatalf("a release after the commit must not revive it: err = %v", err)
	}
}

func TestConcurrentClaimsReserveAnApprovalOnce(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "X"}
	id := approved(t, a, args)
	const callers = 16
	var won atomic.Int32
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.Reserve(id, "reject", args) == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d callers reserved one approval, want 1", won.Load())
	}
}

func TestNoPendingRequestIsAnEmptyListNotNull(t *testing.T) {
	encoded, err := json.Marshal(NewStore().Pending())
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty pending list encodes as %s, want []", encoded)
	}
}

func TestAnApprovedCallCanBeSpentOnce(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "BENCH-W-BOB-01", "remark": "資料不符"}
	id := held(t, a, args)
	if _, err := a.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	if err := spend(a, id, "reject", map[string]any{"remark": "資料不符", "refNo": "BENCH-W-BOB-01"}); err != nil {
		t.Fatalf("spend with the same arguments in another order: %v", err)
	}
	if err := spend(a, id, "reject", args); !errors.Is(err, ErrUsed) {
		t.Fatalf("second spend err = %v, want ErrUsed", err)
	}
}

func TestAnApprovalCannotBeSpentBeforeAHumanDecides(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "X"}
	if err := spend(a, held(t, a, args), "reject", args); !errors.Is(err, ErrPending) {
		t.Fatalf("err = %v, want ErrPending", err)
	}
}

func TestADeniedApprovalStaysDenied(t *testing.T) {
	a := NewStore()
	args := map[string]any{"refNo": "X"}
	id := held(t, a, args)
	if _, err := a.Decide(id, false, "user:lead"); err != nil {
		t.Fatal(err)
	}
	if err := spend(a, id, "reject", args); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.Decide(id, true, "user:lead"); err == nil {
		t.Fatal("a decided approval must not be decided again")
	}
}

func TestAnApprovalCoversOnlyTheCallItWasGrantedFor(t *testing.T) {
	a := NewStore()
	id := held(t, a, map[string]any{"refNo": "BENCH-W-BOB-01"})
	if _, err := a.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	if err := spend(a, id, "reject", map[string]any{"refNo": "BENCH-W-CAROL-01"}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("other arguments: err = %v, want ErrMismatch", err)
	}
	if err := spend(a, id, "complete", map[string]any{"refNo": "BENCH-W-BOB-01"}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("other tool: err = %v, want ErrMismatch", err)
	}
}

func TestAnApprovalExpires(t *testing.T) {
	a := NewStore()
	start := time.Now()
	a.now = func() time.Time { return start }
	args := map[string]any{"refNo": "X"}
	id := held(t, a, args)
	if _, err := a.Decide(id, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return start.Add(TTL + time.Second) }
	if err := spend(a, id, "reject", args); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if len(a.Pending()) != 0 {
		t.Fatal("an expired request is not pending")
	}
}

func TestAnUnknownApprovalIsRefused(t *testing.T) {
	if err := spend(NewStore(), "apr_nope", "reject", nil); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v", err)
	}
}
