package policydraft

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// Draft is a drafted policy: one classification per operation of the document.
type Draft struct {
	// Source names the document the draft was made from.
	Source string
	// Model is the model asked about what heuristics could not settle.
	Model string
	// Risks is the risk set the classifications name.
	Risks           RiskSet
	Classifications []Classification
}

// Classify drafts a risk of rs for every operation of doc. Heuristics, using
// the system's signal words, decide what they can; the rest go to asker in
// batches. Besides the vocabulary and the risk set it takes the document and
// nothing else on purpose: see the package comment.
func Classify(ctx context.Context, doc *spec.Document, asker Asker, sig readiness.Signals, rs RiskSet) ([]Classification, error) {
	decided := make([]Classification, 0, len(doc.Operations))
	var undecided []spec.Operation
	for _, op := range doc.Operations {
		if c, ok := classify(sig, rs, op); ok {
			decided = append(decided, c)
		} else {
			undecided = append(undecided, op)
		}
	}
	if len(undecided) == 0 {
		return decided, nil
	}
	asked, err := askModel(ctx, asker, sig, rs, undecided)
	if err != nil {
		return nil, err
	}
	return append(decided, asked...), nil
}

// Policy is the draft as a policy: its risk set with the set's fail-closed
// default and one exact rule per operation, grouped by risk.
func (d Draft) Policy() policy.Policy {
	cs := d.sorted()
	rules := make([]policy.Rule, len(cs))
	for i, c := range cs {
		rules[i] = policy.Rule{Match: c.Method + " " + c.Path, Risk: c.Risk, Why: c.Why}
	}
	return d.Risks.policy(rules)
}

func (d Draft) sorted() []Classification {
	cs := slices.Clone(d.Classifications)
	slices.SortStableFunc(cs, func(a, b Classification) int {
		if r := d.Risks.rank(a.Risk) - d.Risks.rank(b.Risk); r != 0 {
			return r
		}
		if a.NeedsReview != b.NeedsReview {
			if a.NeedsReview {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return cs
}

// Summary counts what a draft decided and how.
type Summary struct {
	Operations  int
	PerRisk     map[string]int
	ByHeuristic int
	ByModel     int
	NeedsReview int
	levels      []Level
}

// Summary counts the draft.
func (d Draft) Summary() Summary {
	s := Summary{Operations: len(d.Classifications), PerRisk: map[string]int{}, levels: d.Risks.Risks}
	for _, c := range d.Classifications {
		s.PerRisk[c.Risk]++
		if c.Source == ByModel {
			s.ByModel++
		} else {
			s.ByHeuristic++
		}
		if c.NeedsReview {
			s.NeedsReview++
		}
	}
	return s
}

// Render writes the summary for a person.
func (s Summary) Render(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d operations: %d decided by heuristics, %d by the model, %d need review\n", s.Operations, s.ByHeuristic, s.ByModel, s.NeedsReview)
	for _, l := range s.levels {
		fmt.Fprintf(&b, "  %-12s %4d  -> %s\n", l.Name, s.PerRisk[l.Name], action(l))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Marshal renders the draft as a policy file with comments for its reviewer.
// The result always passes policy validation: a draft that would not load is
// an error here, not a file someone finds broken later.
func (d Draft) Marshal() ([]byte, error) {
	p := d.Policy()
	var doc yaml.Node
	if err := doc.Encode(p); err != nil {
		return nil, fmt.Errorf("encode policy: %w", err)
	}
	doc.HeadComment = d.header()
	annotate(&doc, p.Rules)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("write policy: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("write policy: %w", err)
	}
	if _, err := policy.Parse(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("drafted policy does not load: %w", err)
	}
	return buf.Bytes(), nil
}

func (d Draft) header() string {
	s := d.Summary()
	lines := []string{
		fmt.Sprintf("Drafted by `lodog policy draft` from %s; model %s.", d.Source, d.Model),
		fmt.Sprintf("%d operations: %d decided by heuristics, %d by the model, %d need review.", s.Operations, s.ByHeuristic, s.ByModel, s.NeedsReview),
		"A draft for an ops or risk owner to review before it is used (docs/policy.md):",
		"check the rules of the risks listed first and every \"needs review\" line.",
		"Each operation has its own rule; an operation the document did not have",
		"when this was drafted falls to the default, which needs approval.",
		"",
		"Risk levels:",
	}
	for _, l := range d.Risks.Risks {
		lines = append(lines, fmt.Sprintf("  %-12s %-28s %s", l.Name, action(l), l.Means))
	}
	return strings.Join(lines, "\n")
}

func action(l Level) string {
	if l.NeverAutoApprove {
		return string(l.Action) + ", never auto-approved"
	}
	return string(l.Action)
}

// annotate heads each risk group in the rules sequence with a comment.
func annotate(doc *yaml.Node, rules []policy.Rule) {
	root := doc
	if root.Kind == yaml.DocumentNode {
		root = root.Content[0]
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "rules" {
			continue
		}
		seq := root.Content[i+1].Content
		counts := map[string]int{}
		for _, r := range rules {
			counts[r.Risk]++
		}
		for j, r := range rules {
			if j == 0 || rules[j-1].Risk != r.Risk {
				seq[j].HeadComment = fmt.Sprintf("%s: %d operations", r.Risk, counts[r.Risk])
			}
		}
	}
}
