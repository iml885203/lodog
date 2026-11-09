package review

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iml885203/lodog/internal/enrich"
	"github.com/iml885203/lodog/internal/overlay"
)

// overlayOrder is the order overlay risks are asked in: what moves money or
// cannot be undone first, then read, which a yes turns into a lookup the
// policy draft trusts.
var overlayOrder = []string{"irreversible", "money", "credential", "read", "write"}

const unsetRisk = "unset"

// OverlayQuestions asks about every operation of the overlay at path that is
// still marked x-agent.needsReview: one question per risk the overlay gives.
func OverlayQuestions(path string) ([]Question, error) {
	o, err := overlay.LoadFile(path)
	if err != nil {
		return nil, err
	}
	groups := map[string][]Item{}
	var risks []string
	for _, a := range o.Actions {
		method, p, ok := overlay.ParseOperationTarget(a.Target)
		agent, isMap := a.Update["x-agent"].(map[string]any)
		if !ok || !isMap || agent["needsReview"] != true {
			continue
		}
		risk, _ := agent["risk"].(string)
		if risk == "" {
			risk = unsetRisk
		}
		if !slices.Contains(risks, risk) {
			risks = append(risks, risk)
		}
		summary, _ := a.Update["summary"].(string)
		groups[risk] = append(groups[risk], Item{Key: method + " " + p, Why: summary})
	}
	choices := enrich.DefaultRisks()
	for _, r := range risks {
		if r != unsetRisk && !slices.Contains(choices, r) {
			choices = append(choices, r)
		}
	}
	var qs []Question
	for _, risk := range askOrder(risks) {
		qs = append(qs, overlayQuestion(risk, groups[risk], choices))
	}
	return qs, nil
}

func askOrder(risks []string) []string {
	var out []string
	for _, r := range overlayOrder {
		if slices.Contains(risks, r) {
			out = append(out, r)
		}
	}
	var rest []string
	for _, r := range risks {
		if !slices.Contains(out, r) {
			rest = append(rest, r)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

func overlayQuestion(risk string, items []Item, choices []string) Question {
	q := Question{
		ID: "overlay-" + risk, Source: SourceOverlay, Proposed: risk,
		Choices: slices.Clone(choices), Items: items,
		Ask: fmt.Sprintf("The model described these %d operations and labeled them %s, but was not sure. Is %s right for every one?", len(items), risk, risk),
		Why: "The label tells the agent how careful to be; the line under each is the model's own summary of the operation.",
	}
	switch risk {
	case "read":
		q.Why = "A yes makes these trusted lookups: the next policy draft lets agents call them with nobody approving."
	case unsetRisk:
		q.Ask = fmt.Sprintf("The model described these %d operations but gave no risk. Which risk does each have?", len(items))
	}
	return q
}

// ApplyOverlay writes the answered overlay questions back to the overlay at
// path: each operation takes its reviewed risk and loses needsReview. It
// refuses an operation that is no longer pending with the risk asked about.
func ApplyOverlay(path string, qs []Question) error {
	o, err := overlay.LoadFile(path)
	if err != nil {
		return err
	}
	agents := map[string]map[string]any{}
	for _, a := range o.Actions {
		if agent, ok := a.Update["x-agent"].(map[string]any); ok {
			agents[a.Target] = agent
		}
	}
	for _, q := range qs {
		for _, it := range q.Items {
			method, p, _ := strings.Cut(it.Key, " ")
			agent := agents[overlay.OperationTarget(method, p)]
			if agent == nil || agent["needsReview"] != true {
				return fmt.Errorf("question %s: %s is not pending review in %s; ask again", q.ID, it.Key, path)
			}
			if risk, _ := agent["risk"].(string); risk != q.Proposed && (risk != "" || q.Proposed != unsetRisk) {
				return fmt.Errorf("question %s: %s is %q in the file, not %s as asked; ask again", q.ID, it.Key, risk, q.Proposed)
			}
			agent["risk"] = q.valueOf(it.Key)
			delete(agent, "needsReview")
		}
	}
	return o.Save(path)
}
