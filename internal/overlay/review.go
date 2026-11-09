package overlay

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// uncertainty matches the wording a drafter uses when it did not confirm what
// it wrote.
var uncertainty = regexp.MustCompile(`(?i)\[猜\]|not confirmed|unconfirmed|unverified|not verified|not checked|did not read|assumed|inferred|guess|probably|presumably`)

// HasUncertainty reports whether text says the author was unsure.
func HasUncertainty(text string) bool { return uncertainty.MatchString(text) }

// UnflaggedUncertain lists the operations whose text admits a guess although
// no action of the operation carries x-agent.needsReview. An operation a
// person has not reviewed must say so; one marked reviewed must read as fact.
func (o *Overlay) UnflaggedUncertain() []string {
	flagged := map[string]bool{}
	unsure := map[string]bool{}
	for _, a := range o.Actions {
		op := a.Target
		if i := strings.Index(op, ".parameters"); i >= 0 {
			op = op[:i]
		}
		if x, ok := a.Update["x-agent"].(map[string]any); ok && x["needsReview"] == true {
			flagged[op] = true
		}
		if HasUncertainty(fmt.Sprint(a.Update)) {
			unsure[op] = true
		}
	}
	var out []string
	for op := range unsure {
		if !flagged[op] {
			out = append(out, op)
		}
	}
	sort.Strings(out)
	return out
}
