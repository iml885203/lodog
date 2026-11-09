// Package draft helps a team write a task bank for its own system: Scan
// proposes endpoints for each task type from the OpenAPI document, Record
// captures what the live system returns for a task's expected calls, and
// Check validates a finished answers.yaml. Every output is a draft for a
// person to confirm; the dangerous-call list in particular must be confirmed
// by someone who knows the system, never taken from Scan as is.
package draft

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// Candidate is one endpoint proposed for a task type.
type Candidate struct {
	Endpoint string `yaml:"endpoint"`
	Summary  string `yaml:"summary,omitempty"`
	Reason   string `yaml:"reason"`
}

// TypeCandidates are the proposals for one task type.
type TypeCandidates struct {
	Type       score.TaskType `yaml:"type"`
	Tests      string         `yaml:"tests"`
	Candidates []Candidate    `yaml:"candidates"`
}

// ScanResult is what Scan proposes.
type ScanResult struct {
	Types []TypeCandidates `yaml:"types"`
	// Risky lists every endpoint that may change state. A person decides which
	// of them go into must_not_call_without_approval.
	Risky []Candidate `yaml:"risky"`
}

// Scan proposes up to limit endpoints per task type.
func Scan(doc *spec.Document, limit int, sig readiness.Signals) ScanResult {
	byType := map[score.TaskType][]Candidate{}
	var risky []Candidate
	for _, op := range doc.Operations {
		for t, reason := range classify(sig, op) {
			byType[t] = append(byType[t], candidate(op, reason))
		}
		if reason, ok := riskReason(sig, op); ok {
			risky = append(risky, candidate(op, reason))
		}
	}
	byType[score.TypeCrossDomain] = crossDomain(doc)
	sort.SliceStable(byType[score.TypeGatedWrite], func(i, j int) bool {
		return isMoney(byType[score.TypeGatedWrite][i]) && !isMoney(byType[score.TypeGatedWrite][j])
	})
	var out ScanResult
	for _, info := range score.TaskTypes {
		cs := byType[info.Type]
		if len(cs) > limit {
			cs = cs[:limit]
		}
		out.Types = append(out.Types, TypeCandidates{Type: info.Type, Tests: info.Tests, Candidates: cs})
	}
	out.Risky = risky
	return out
}

func classify(sig readiness.Signals, op spec.Operation) map[score.TaskType]string {
	out := map[score.TaskType]string{}
	if readiness.MethodMutates(op.Method) {
		if reason, ok := riskReason(sig, op); ok {
			out[score.TypeGatedWrite] = reason
		}
		if reason, ok := reversibleWrite(sig, op); ok {
			out[score.TypeApprovedWrite] = reason
		}
		return out
	}
	if word, ok := sig.SideEffectGET(op); ok {
		out[score.TypeDisguisedWrite] = "GET whose path names an action: " + word
		return out
	}
	if endsWithParam(op.Path) {
		out[score.TypeLookup] = "GET by id"
		out[score.TypeNotFound] = "GET by id: ask for an id that does not exist"
	}
	if paging := readiness.PagingParams(op); len(paging) > 0 {
		out[score.TypeList] = "list with paging parameter " + paging[0].Name
	}
	if p, ok := findParam(op, func(p spec.Param) bool { return p.In == "query" && p.Required }); ok {
		out[score.TypeRecoverableError] = "required query parameter " + p.Name + ": omitting it should fail with a readable error"
	}
	return out
}

func riskReason(sig readiness.Signals, op spec.Operation) (string, bool) {
	if !sig.Mutating(op) {
		return "", false
	}
	if !readiness.MethodMutates(op.Method) {
		return "GET that names an action; it may change state", true
	}
	if word, ok := sig.Money(op); ok {
		return op.Method + " touching money (" + word + ")", true
	}
	if op.Method == "DELETE" {
		return "DELETE", true
	}
	return op.Method + " changes state", true
}

// reversibleWrite proposes writes an approved_write task may really execute:
// a POST or PATCH that creates or edits a record and touches no money. It is
// only a proposal; a person must confirm that the reset undoes the write and
// that it reaches nothing outside the system (no message, no payment).
func reversibleWrite(sig readiness.Signals, op spec.Operation) (string, bool) {
	if op.Method != "POST" && op.Method != "PATCH" && op.Method != "PUT" {
		return "", false
	}
	if _, money := sig.Money(op); money {
		return "", false
	}
	return op.Method + " without money: confirm the reset undoes it and it reaches nothing outside the system", true
}

// crossDomain pairs read endpoints from different tags that take the same
// identifying parameter, such as customerId: a question that needs both.
func crossDomain(doc *spec.Document) []Candidate {
	byParam := map[string]map[string]spec.Operation{}
	for _, op := range doc.Operations {
		if readiness.MethodMutates(op.Method) || op.Tag == "" {
			continue
		}
		for _, p := range op.Params {
			if !strings.HasSuffix(strings.ToLower(p.Name), "id") {
				continue
			}
			if byParam[p.Name] == nil {
				byParam[p.Name] = map[string]spec.Operation{}
			}
			if _, seen := byParam[p.Name][op.Tag]; !seen {
				byParam[p.Name][op.Tag] = op
			}
		}
	}
	var out []Candidate
	for _, name := range sortedKeys(byParam) {
		tags := byParam[name]
		if len(tags) < 2 {
			continue
		}
		var eps []string
		for _, tag := range sortedKeys(tags) {
			eps = append(eps, tags[tag].Method+" "+tags[tag].Path)
		}
		out = append(out, Candidate{
			Endpoint: strings.Join(eps[:2], " + "),
			Reason:   fmt.Sprintf("%d domains share the parameter %s", len(tags), name),
		})
	}
	return out
}

// isMoney ranks writes that move money first: they make the strongest
// gated-write tasks.
func isMoney(c Candidate) bool {
	return strings.Contains(c.Reason, "touching money")
}

func candidate(op spec.Operation, reason string) Candidate {
	return Candidate{Endpoint: op.Method + " " + op.Path, Summary: op.Summary, Reason: reason}
}

func endsWithParam(path string) bool {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	last := segs[len(segs)-1]
	return strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}")
}

func findParam(op spec.Operation, match func(spec.Param) bool) (spec.Param, bool) {
	for _, p := range op.Params {
		if match(p) {
			return p, true
		}
	}
	return spec.Param{}, false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
