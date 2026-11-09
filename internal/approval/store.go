// Package approval is how a held L3 call gets a human verdict: the store of
// requests (bound to one tool and one set of arguments, spent once), the
// channels that ask people (the MCP client's own prompt, a Slack webhook with a
// one-time link), and the endpoint that lists what is waiting. It knows nothing
// of the gateway that holds the calls (docs/approvals.md).
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/policy"
)

// Approval errors an agent can act on.
var (
	ErrUnknown  = errors.New("no such approval")
	ErrPending  = errors.New("approval is still waiting for a human")
	ErrDenied   = errors.New("approval was denied")
	ErrUsed     = errors.New("approval was already used")
	ErrExpired  = errors.New("approval expired")
	ErrMismatch = errors.New("approval was granted for a different call")
	ErrInFlight = errors.New("approval is permitting another call right now")
)

// Status is where a request stands.
type Status string

// The states of an approval request.
const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Denied   Status = "denied"
	InFlight Status = "in_flight"
	Used     Status = "used"
)

// Request is one held call waiting for, or carrying, a human's verdict.
type Request struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Method  string `json:"method"`
	Path    string `json:"path"` // the concrete path the call would hit
	Summary string `json:"summary"`
	Risk    string `json:"risk"`
	// NeverAutoApprove is the policy risk's attribute: an automated
	// approver denies the call, only a person can approve it.
	NeverAutoApprove bool           `json:"never_auto_approve,omitempty"`
	Arguments        map[string]any `json:"arguments"`
	Status           Status         `json:"status"`
	Actor            string         `json:"actor"`
	OnBehalfOf       string         `json:"on_behalf_of,omitempty"`
	DecidedBy        string         `json:"decided_by,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	argsHash         string
}

// TTL bounds how long an unanswered or unused approval stays valid,
// so a verdict given for one situation cannot be spent much later in another.
const TTL = 30 * time.Minute

// Store holds approval requests in memory. An approval is bound to one
// tool and one exact set of arguments, and is spent by the call it permits:
// Reserve claims it for one call, then Commit spends it once the request is
// sent, or Release returns it when the call failed before anything was sent.
type Store struct {
	mu   sync.Mutex
	byID map[string]*Request
	now  func() time.Time
	// observe, when set, is told after a verdict or a spend, outside the lock.
	observe func(Request)
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{byID: map[string]*Request{}, now: time.Now}
}

// Hold records a held call and returns it. The approval binds args.
func (a *Store) Hold(tool, method, path, summary string, d policy.Decision, args map[string]any, identity casecfg.IdentityConfig) (*Request, error) {
	hash, err := hashArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := newApprovalID()
	if err != nil {
		return nil, err
	}
	req := &Request{
		ID: id, Tool: tool, Method: method, Path: path, Summary: summary, Risk: d.Risk, NeverAutoApprove: d.NeverAutoApprove, Arguments: args, Status: Pending,
		Actor: identity.Actor, OnBehalfOf: identity.OnBehalfOf, CreatedAt: a.now(), argsHash: hash,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byID[id] = req
	return req, nil
}

// Decide records a human's verdict on a pending request.
func (a *Store) Decide(id string, approve bool, by string) (*Request, error) {
	copied, err := a.decide(id, approve, by)
	if err != nil {
		return nil, err
	}
	if a.observe != nil {
		a.observe(*copied)
	}
	return copied, nil
}

func (a *Store) decide(id string, approve bool, by string) (*Request, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.byID[id]
	if !ok {
		return nil, ErrUnknown
	}
	if a.expired(req) {
		return nil, ErrExpired
	}
	if req.Status != Pending {
		return nil, fmt.Errorf("approval %s is %s, not pending", id, req.Status)
	}
	req.Status, req.DecidedBy = Denied, by
	if approve {
		req.Status = Approved
	}
	copied := *req
	return &copied, nil
}

// status reports an approval's status; empty when unknown.
func (a *Store) status(id string) Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	if req, ok := a.byID[id]; ok {
		return req.Status
	}
	return ""
}

// Reserve checks that id permits this exact call and claims it for that call.
// While it is claimed no other call can claim it, so concurrent retries with
// one approval release at most one call. The caller must Commit or Release.
func (a *Store) Reserve(id, tool string, args map[string]any) error {
	hash, err := hashArgs(args)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.byID[id]
	switch {
	case !ok:
		return ErrUnknown
	case a.expired(req):
		return ErrExpired
	case req.Status == Pending:
		return ErrPending
	case req.Status == Denied:
		return ErrDenied
	case req.Status == InFlight:
		return ErrInFlight
	case req.Status == Used:
		return ErrUsed
	case req.Tool != tool || req.argsHash != hash:
		return ErrMismatch
	}
	req.Status = InFlight
	return nil
}

// Commit spends a reserved approval: the call it permitted was sent.
func (a *Store) Commit(id string) {
	a.settle(id, Used)
}

// Release returns a reserved approval unspent: the call failed before any
// request was sent, so the human's verdict still stands for a retry.
func (a *Store) Release(id string) {
	a.settle(id, Approved)
}

func (a *Store) settle(id string, to Status) {
	a.mu.Lock()
	req, ok := a.byID[id]
	if !ok || req.Status != InFlight {
		a.mu.Unlock()
		return
	}
	req.Status = to
	copied := *req
	a.mu.Unlock()
	if to == Used && a.observe != nil {
		a.observe(copied)
	}
}

// Pending lists requests waiting for a verdict, oldest first; never nil, so
// an empty list encodes as [] rather than null.
func (a *Store) Pending() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Request{}
	for _, req := range a.byID {
		if req.Status == Pending && !a.expired(req) {
			out = append(out, *req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns a copy of the request id, if there is one.
func (a *Store) Get(id string) (Request, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.byID[id]
	if !ok {
		return Request{}, false
	}
	return *req, true
}

// SetClock replaces the clock expiry is judged by; tests use it to age requests.
func (a *Store) SetClock(now func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = now
}

// All lists every request whatever its status, oldest first.
func (a *Store) All() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Request, 0, len(a.byID))
	for _, req := range a.byID {
		out = append(out, *req)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (a *Store) expired(req *Request) bool {
	return a.now().Sub(req.CreatedAt) > TTL
}

// hashArgs fingerprints arguments; encoding/json sorts map keys, so the same
// arguments in any order hash the same.
func hashArgs(args map[string]any) (string, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode arguments: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newApprovalID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("approval id: %w", err)
	}
	return "apr_" + hex.EncodeToString(b), nil
}
