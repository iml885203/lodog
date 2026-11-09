// Package policy decides what the gateway does with each legacy operation at
// L3: let it through, hold it for a human's approval, refuse it, or not offer
// it at all. It knows nothing about MCP or HTTP; it maps an operation (method
// and path template) to a decision.
package policy

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Action is what the gateway does with a call.
type Action string

// The actions a policy can take.
const (
	Allow    Action = "allow"    // forward
	Approval Action = "approval" // hold until a human approves this exact call
	Deny     Action = "deny"     // refuse, with a reason
	Hide     Action = "hide"     // never offered to the agent
)

// Decision is the outcome for one operation, with the rule that produced it
// so a refusal can say why.
type Decision struct {
	Action Action
	Risk   string
	// NeverAutoApprove is the risk's attribute: only a person may approve
	// the call, never an automated approver.
	NeverAutoApprove bool
	Rule             string // the matching pattern, or DefaultRule
}

// DefaultRule is Decision.Rule when no rule matched. No rule's pattern can
// equal it: a pattern always has a method and a path.
const DefaultRule = "default"

// Explicit reports whether a rule matched, rather than the default deciding.
func (d Decision) Explicit() bool {
	return d.Rule != DefaultRule
}

// Policy is cases/<system>/spec/policy.yaml.
type Policy struct {
	// Risks maps a risk level to the action taken for it and its
	// attributes. The names are the system's own: lodog reads only the
	// attributes, never a name.
	Risks map[string]Risk `yaml:"risks"`
	// Rules are tried in order; the first match decides.
	Rules []Rule `yaml:"rules"`
	// Default is the risk of an operation no rule matches. It must map to
	// approval or stricter: the gateway fails closed.
	Default string `yaml:"default"`
}

// Risk is what a policy does with one risk level.
type Risk struct {
	Action Action `yaml:"action"`
	// NeverAutoApprove holds a call of this risk for a person: an automated
	// approver, such as a benchmark's simulated operator, never approves it.
	NeverAutoApprove bool `yaml:"never_auto_approve,omitempty"`
}

// UnmarshalYAML reads a risk either as its action alone (read: allow) or as
// a mapping with attributes ({action: approval, never_auto_approve: true}).
func (r *Risk) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&r.Action)
	}
	type plain Risk
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*r = Risk(p)
	return nil
}

// MarshalYAML writes a risk with no attributes as its action alone.
func (r Risk) MarshalYAML() (any, error) {
	if !r.NeverAutoApprove {
		return string(r.Action), nil
	}
	type plain Risk
	return plain(r), nil
}

// Rule gives every operation matching Match a risk level.
type Rule struct {
	// Match is "METHOD /path/pattern". METHOD may be *. In the path, *
	// matches one segment and a trailing /** matches any remainder.
	// Patterns match the document's path template, e.g. {customerId}.
	Match string `yaml:"match"`
	Risk  string `yaml:"risk"`
	Why   string `yaml:"why"`
}

// LoadFile reads and validates a policy.
func LoadFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own policy file
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return p, nil
}

// Parse reads and validates a policy document.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

var strictness = map[Action]int{Allow: 0, Approval: 1, Deny: 2, Hide: 3}

// Validate reports everything that keeps p from loading.
func (p *Policy) Validate() error {
	var errs []error
	for name, risk := range p.Risks {
		if _, ok := strictness[risk.Action]; !ok {
			errs = append(errs, fmt.Errorf("risk %q: action %q is not allow, approval, deny or hide", name, risk.Action))
		}
		if risk.NeverAutoApprove && risk.Action == Allow {
			errs = append(errs, fmt.Errorf("risk %q: never_auto_approve with allow: an allowed call runs with nobody approving it", name))
		}
	}
	def, ok := p.Risks[p.Default]
	switch {
	case p.Default == "":
		errs = append(errs, errors.New("default is required: an unmatched operation needs a risk"))
	case !ok:
		errs = append(errs, fmt.Errorf("default risk %q has no entry in risks", p.Default))
	case strictness[def.Action] < strictness[Approval]:
		errs = append(errs, fmt.Errorf("default risk %q maps to %s; it must be approval or stricter (fail closed)", p.Default, def.Action))
	}
	for i, r := range p.Rules {
		if _, _, err := splitMatch(r.Match); err != nil {
			errs = append(errs, fmt.Errorf("rules[%d]: %w", i, err))
		}
		if _, ok := p.Risks[r.Risk]; !ok {
			errs = append(errs, fmt.Errorf("rules[%d] %q: risk %q has no entry in risks", i, r.Match, r.Risk))
		}
	}
	return errors.Join(errs...)
}

// Warnings are what a valid policy may still get wrong. Loading does not
// refuse them; whoever loads the policy shows them to a person.
func (p *Policy) Warnings() []string {
	for _, r := range p.Risks {
		if r.NeverAutoApprove {
			return nil
		}
	}
	return []string{"no risk has never_auto_approve: an automated approver may approve every held call, including ones that cannot be undone"}
}

// Decide returns what to do with method on the path template.
func (p *Policy) Decide(method, path string) Decision {
	for _, r := range p.Rules {
		m, pattern, _ := splitMatch(r.Match)
		if (m == "*" || strings.EqualFold(m, method)) && matchPath(pattern, path) {
			return p.decision(r.Risk, r.Match)
		}
	}
	return p.decision(p.Default, DefaultRule)
}

func (p *Policy) decision(risk, rule string) Decision {
	r := p.Risks[risk]
	return Decision{Action: r.Action, Risk: risk, NeverAutoApprove: r.NeverAutoApprove, Rule: rule}
}

func splitMatch(match string) (string, string, error) {
	method, path, ok := strings.Cut(strings.TrimSpace(match), " ")
	if !ok || !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("match %q must look like \"GET /api/**\"", match)
	}
	return method, path, nil
}

func matchPath(pattern, path string) bool {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	xs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range ps {
		if seg == "**" && i == len(ps)-1 {
			return len(xs) >= i
		}
		if i >= len(xs) || (seg != "*" && seg != xs[i]) {
			return false
		}
	}
	return len(ps) == len(xs)
}

// Strictest returns the index of the strictest decision, the first one on a
// tie: a call made of several operations is governed by its riskiest one.
// It returns -1 for no decisions.
func Strictest(ds []Decision) int {
	best := -1
	for i, d := range ds {
		if best < 0 || strictness[d.Action] > strictness[ds[best].Action] {
			best = i
		}
	}
	return best
}
