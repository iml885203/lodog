package review

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/policy"
)

const (
	unsurePrefix   = "needs review"
	reviewedPrefix = "reviewed by "
)

type policyGroup struct {
	risk   string
	unsure bool
	items  []Item
}

// PolicyQuestions asks about every rule of the policy file at path that no
// one has reviewed: one question per risk, the rules the model was unsure of
// first. Rules a review already wrote back are not asked again.
func PolicyQuestions(path string) ([]Question, error) {
	p, err := policy.LoadFile(path)
	if err != nil {
		return nil, err
	}
	order := riskOrder(p)
	groups := map[string]*policyGroup{}
	for _, r := range p.Rules {
		if strings.HasPrefix(r.Why, reviewedPrefix) {
			continue
		}
		unsure := strings.HasPrefix(r.Why, unsurePrefix)
		k := fmt.Sprintf("%s/%t", r.Risk, unsure)
		if groups[k] == nil {
			groups[k] = &policyGroup{risk: r.Risk, unsure: unsure}
		}
		groups[k].items = append(groups[k].items, Item{Key: r.Match, Why: r.Why})
	}
	var qs []Question
	for _, unsure := range []bool{true, false} {
		for _, risk := range order {
			if g := groups[fmt.Sprintf("%s/%t", risk, unsure)]; g != nil {
				qs = append(qs, policyQuestion(g, p.Risks[risk], order))
			}
		}
	}
	return qs, nil
}

// riskOrder is the policy's risks in the order its rules first use them,
// which for a draft is strictest first; risks no rule uses come last.
func riskOrder(p *policy.Policy) []string {
	var order []string
	for _, r := range p.Rules {
		if !slices.Contains(order, r.Risk) {
			order = append(order, r.Risk)
		}
	}
	var rest []string
	for name := range p.Risks {
		if !slices.Contains(order, name) {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append(order, rest...)
}

func policyQuestion(g *policyGroup, r policy.Risk, choices []string) Question {
	id := "policy-" + g.risk
	ask := fmt.Sprintf("These %d operations are %s: %s. Is that right for every one?", len(g.items), g.risk, effect(r))
	why := reasonOf(r)
	if g.unsure {
		id += "-unsure"
		ask = fmt.Sprintf("The model was not sure about these %d operations and drafted them as %s: %s. Is that right for every one?", len(g.items), g.risk, effect(r))
		why = "The model judged them from names and summaries alone and said it was not sure; the line under each says what it doubted."
	}
	return Question{
		ID: id, Source: SourcePolicy, Ask: ask, Why: why,
		Proposed: g.risk, Choices: slices.Clone(choices), Items: g.items,
	}
}

func effect(r policy.Risk) string {
	switch r.Action {
	case policy.Allow:
		return "an agent calls them with nobody approving"
	case policy.Approval:
		if r.NeverAutoApprove {
			return "every call waits for a person, never an automated approver"
		}
		return "every call waits for an approval"
	case policy.Deny:
		return "every call is refused"
	case policy.Hide:
		return "agents never see them"
	}
	return string(r.Action)
}

func reasonOf(r policy.Risk) string {
	switch r.Action {
	case policy.Allow:
		return "Anything here that changes data, moves money or sends something out runs unapproved: this is the one mistake the gateway cannot catch."
	case policy.Approval:
		if r.NeverAutoApprove {
			return "These were judged impossible to undo (deletes, pay-outs, settled transactions, messages sent out); say if one can be undone, or if one elsewhere belongs here."
		}
		return "A held call costs one approval; say if one cannot be undone and needs a person every time, or is only a lookup."
	case policy.Deny:
		return "An operation here can never be used through the gateway."
	case policy.Hide:
		return "An operation operators really use would be out of the agent's reach."
	}
	return ""
}

// ApplyPolicy writes the answered policy questions back: every confirmed rule
// keeps its risk, every changed one takes the new risk, and each says who
// reviewed it and when. It reads the policy at in and writes it to out,
// keeping the file's comments. It refuses a rule that is not pending with the
// risk the question was asked about: the checklist is out of date.
func ApplyPolicy(in, out string, qs []Question, by, date string) error {
	data, err := os.ReadFile(in) //nolint:gosec // G304: the operator names their own policy file
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse policy %s: %w", in, err)
	}
	rules := ruleNodes(&doc)
	for _, q := range qs {
		for _, it := range q.Items {
			r, ok := rules[it.Key]
			if !ok {
				return fmt.Errorf("question %s: %s has no rule in %s; ask again", q.ID, it.Key, in)
			}
			if err := reviewRule(r, q, it.Key, by, date); err != nil {
				return fmt.Errorf("question %s: %w", q.ID, err)
			}
		}
	}
	return writePolicy(out, &doc)
}

type ruleNode struct{ risk, why *yaml.Node }

func reviewRule(r ruleNode, q Question, key, by, date string) error {
	if strings.HasPrefix(r.why.Value, reviewedPrefix) {
		return fmt.Errorf("%s was already reviewed; ask again", key)
	}
	if r.risk.Value != q.Proposed {
		return fmt.Errorf("%s is %s in the file, not %s as asked; ask again", key, r.risk.Value, q.Proposed)
	}
	verdict := "confirmed"
	if v := q.valueOf(key); v != q.Proposed {
		verdict = "changed from " + q.Proposed
		r.risk.Value = v
	}
	drafted := strings.TrimPrefix(strings.TrimPrefix(r.why.Value, unsurePrefix), ": ")
	r.why.Value = fmt.Sprintf("%s%s on %s: %s; drafted as: %s", reviewedPrefix, by, date, verdict, drafted)
	return nil
}

func ruleNodes(doc *yaml.Node) map[string]ruleNode {
	out := map[string]ruleNode{}
	seq := mappingValue(root(doc), "rules")
	if seq == nil {
		return out
	}
	for _, rule := range seq.Content {
		match, risk, why := mappingValue(rule, "match"), mappingValue(rule, "risk"), mappingValue(rule, "why")
		if match == nil || risk == nil {
			continue
		}
		if _, dup := out[match.Value]; dup {
			continue
		}
		if why == nil {
			why = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
			rule.Content = append(rule.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "why"}, why)
		}
		out[match.Value] = ruleNode{risk: risk, why: why}
	}
	return out
}

func writePolicy(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode policy: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode policy: %w", err)
	}
	if _, err := policy.Parse(buf.Bytes()); err != nil {
		return fmt.Errorf("reviewed policy does not load: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func root(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
