package proxy

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

// Guard decides which calls a read-only proxy sends. It fails closed: a call
// is sent only when it is a sign-in the operator named, or when it maps to an
// operation of the OpenAPI document that an explicit policy rule allows. The
// HTTP method decides nothing: a GET that resets a password is held like a
// POST.
type Guard struct {
	ops    []operation
	allow  []template
	policy *policy.Policy
}

type operation struct {
	template
	op spec.Operation
}

// template is "METHOD /path" with {name} segments matching any value.
type template struct {
	method   string
	segments []string
}

// verdict is what the guard decided for one call.
type verdict struct {
	send   bool
	reason string
	// signIn marks a sign-in call: its answer carries a session or a token,
	// so a recording keeps only its status and cookie names.
	signIn bool
	// op is the call's operation, nil when the document has none.
	op *spec.Operation
}

// NewGuard builds a guard from the system's OpenAPI document, its policy, and
// the sign-in calls to let through (e.g. "POST /api/operator/login").
func NewGuard(doc *spec.Document, pol *policy.Policy, allow []string) (*Guard, error) {
	if doc == nil || pol == nil {
		return nil, fmt.Errorf("a read-only proxy needs the OpenAPI document and a policy")
	}
	g := &Guard{policy: pol}
	for _, op := range doc.Operations {
		g.ops = append(g.ops, operation{template: newTemplate(op.Method, op.Path), op: op})
	}
	for _, a := range allow {
		method, path, ok := strings.Cut(strings.TrimSpace(a), " ")
		if !ok || !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("allowed call %q must look like \"POST /path\"", a)
		}
		path, _, _ = strings.Cut(path, "?")
		g.allow = append(g.allow, newTemplate(method, path))
	}
	return g, nil
}

func newTemplate(method, path string) template {
	return template{method: strings.ToUpper(method), segments: splitPath(path)}
}

func splitPath(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}

// literals is how many segments of t match path literally, or -1 when t does
// not match: the operation with the most literal segments wins, so
// /api/customer/searchPlayerProfile is not read as /api/customer/{id}.
func (t template) literals(method string, path []string) int {
	if !strings.EqualFold(t.method, method) || len(path) != len(t.segments) {
		return -1
	}
	n := 0
	for i, seg := range t.segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if !strings.EqualFold(seg, path[i]) {
			return -1
		}
		n++
	}
	return n
}

func (g *Guard) check(method, path string) verdict {
	segs := splitPath(path)
	for _, a := range g.allow {
		if a.literals(method, segs) >= 0 {
			return verdict{send: true, signIn: true}
		}
	}
	op := g.operation(method, segs)
	if op == nil {
		return verdict{reason: "not an operation of the OpenAPI document"}
	}
	d := g.policy.Decide(op.Method, op.Path)
	if d.Explicit() && d.Action == policy.Allow {
		return verdict{send: true, op: op}
	}
	return verdict{op: op, reason: fmt.Sprintf("policy: risk %s (%s, rule %s) is not an explicit allow", d.Risk, d.Action, d.Rule)}
}

func (g *Guard) operation(method string, path []string) *spec.Operation {
	best, most := -1, -1
	for i, o := range g.ops {
		if n := o.literals(method, path); n > most {
			best, most = i, n
		}
	}
	if best < 0 {
		return nil
	}
	return &g.ops[best].op
}

// GuardFromCase builds the guard of a read-only or replaying proxy from a
// case: its OpenAPI document, its policy (relative to caseDir unless
// absolute) and its sign-in calls, plus any extra allow entries.
func GuardFromCase(caseDir, policyPath string, allow []string) (*Guard, error) {
	if caseDir == "" {
		return nil, fmt.Errorf("%w: --read-only and --replay need --case", ErrBadOptions)
	}
	c, err := score.LoadCase(caseDir)
	if err != nil {
		return nil, err
	}
	doc, err := spec.LoadFile(c.Spec)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(policyPath) {
		policyPath = filepath.Join(caseDir, policyPath)
	}
	pol, err := policy.LoadFile(policyPath)
	if err != nil {
		return nil, err
	}
	signIn := append([]string{}, allow...)
	for _, e := range c.Ignore {
		signIn = append(signIn, e.String())
	}
	return NewGuard(doc, pol, signIn)
}
