package policydraft

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/policy"
)

// The classes lodog's heuristics detect. They are the values an overlay's
// x-agent.risk declares, plus internal and test. A risk set says which of its
// risks takes each class; the policy never sees a class.
const (
	ClassRead         = "read"
	ClassWrite        = "write"
	ClassMoney        = "money"
	ClassIrreversible = "irreversible"
	ClassCredential   = "credential"
	ClassInternal     = "internal"
	ClassTest         = "test"
)

var classes = []string{ClassRead, ClassWrite, ClassMoney, ClassIrreversible, ClassCredential, ClassInternal, ClassTest}

//go:embed risks.yaml
var defaultRiskSet []byte

// RiskSet is the risk levels a drafted policy uses: risks.yaml in this
// package, or a system's own file.
type RiskSet struct {
	Default string  `yaml:"default"`
	Risks   []Level `yaml:"risks"`
}

// Level is one risk of a set.
type Level struct {
	Name             string        `yaml:"name"`
	Action           policy.Action `yaml:"action"`
	NeverAutoApprove bool          `yaml:"never_auto_approve"`
	Classes          []string      `yaml:"classes"`
	Means            string        `yaml:"means"`
}

// DefaultRiskSet is the risk set lodog ships.
func DefaultRiskSet() RiskSet {
	rs, err := ParseRiskSet(defaultRiskSet)
	if err != nil {
		panic(fmt.Sprintf("the embedded risks.yaml is invalid: %v", err))
	}
	return rs
}

// LoadRiskSet reads and validates a risk set file.
func LoadRiskSet(path string) (RiskSet, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own risk set
	if err != nil {
		return RiskSet{}, fmt.Errorf("read risk set: %w", err)
	}
	rs, err := ParseRiskSet(data)
	if err != nil {
		return RiskSet{}, fmt.Errorf("risk set %s: %w", path, err)
	}
	return rs, nil
}

// ParseRiskSet reads and validates a risk set.
func ParseRiskSet(data []byte) (RiskSet, error) {
	var rs RiskSet
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&rs); err != nil {
		return RiskSet{}, fmt.Errorf("parse risk set: %w", err)
	}
	if err := rs.validate(); err != nil {
		return RiskSet{}, err
	}
	return rs, nil
}

func (rs RiskSet) validate() error {
	var errs []error
	owner := map[string]string{}
	names := map[string]bool{}
	for i, l := range rs.Risks {
		switch {
		case strings.TrimSpace(l.Name) == "":
			errs = append(errs, fmt.Errorf("risks[%d]: name is required", i))
		case names[l.Name]:
			errs = append(errs, fmt.Errorf("risk %q is listed twice", l.Name))
		}
		names[l.Name] = true
		if strings.TrimSpace(l.Means) == "" {
			errs = append(errs, fmt.Errorf("risk %q: means is required: the model and the reviewer read it", l.Name))
		}
		for _, c := range l.Classes {
			switch {
			case !slices.Contains(classes, c):
				errs = append(errs, fmt.Errorf("risk %q: class %q is not one of %s", l.Name, c, strings.Join(classes, ", ")))
			case owner[c] != "":
				errs = append(errs, fmt.Errorf("class %q goes to both %q and %q", c, owner[c], l.Name))
			default:
				owner[c] = l.Name
			}
		}
		if slices.Contains(l.Classes, ClassRead) && len(l.Classes) > 1 {
			errs = append(errs, fmt.Errorf("risk %q: the read risk takes no other class; the model never chooses it", l.Name))
		}
	}
	for _, c := range classes {
		if owner[c] == "" {
			errs = append(errs, fmt.Errorf("class %q goes to no risk", c))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	p := rs.policy(nil)
	return p.Validate()
}

// policy is the set as a policy with rules.
func (rs RiskSet) policy(rules []policy.Rule) policy.Policy {
	risks := make(map[string]policy.Risk, len(rs.Risks))
	for _, l := range rs.Risks {
		risks[l.Name] = policy.Risk{Action: l.Action, NeverAutoApprove: l.NeverAutoApprove}
	}
	return policy.Policy{Risks: risks, Rules: rules, Default: rs.Default}
}

// riskOf names the risk that takes class.
func (rs RiskSet) riskOf(class string) string {
	for _, l := range rs.Risks {
		if slices.Contains(l.Classes, class) {
			return l.Name
		}
	}
	return ""
}

// rank is name's place in the review order, -1 when the set has no such risk.
func (rs RiskSet) rank(name string) int {
	return slices.IndexFunc(rs.Risks, func(l Level) bool { return l.Name == name })
}

// modelChoices are the risks the model may choose: all but the read risk,
// since every operation it is asked about may change state.
func (rs RiskSet) modelChoices() []Level {
	read := rs.riskOf(ClassRead)
	return slices.DeleteFunc(slices.Clone(rs.Risks), func(l Level) bool { return l.Name == read })
}

// overlayClasses are the classes a model may declare in an overlay's
// x-agent.risk, in the order it is shown them. internal and test are left
// out: a service endpoint or a debug route is read from the path, not from a
// summary.
var overlayClasses = []string{ClassRead, ClassWrite, ClassMoney, ClassIrreversible, ClassCredential}

// OverlayRisks are the risk names the model may propose in an overlay: the
// risks that take overlayClasses.
func (rs RiskSet) OverlayRisks() []string {
	out := make([]string, 0, len(overlayClasses))
	for _, c := range overlayClasses {
		out = append(out, rs.riskOf(c))
	}
	return out
}
