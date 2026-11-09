package readiness

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

// Max is the static share of a level's 100 points.
const Max = 30

// toolLimit is the tool count above which an agent's choice degrades.
const toolLimit = 20

// maxExamples caps the endpoints a finding lists.
const maxExamples = 10

// View is what an agent is given at one level.
type View struct {
	Source string
	Level  string
	Doc    *spec.Document
	// Policy is the gateway's risk policy (L3); nil when there is none.
	Policy *policy.Policy
	// Identity is true when the gateway gives the agent its own identity and
	// names the person it acts for.
	Identity bool
	// Tasks are the task tools an L2 or L3 gateway serves, already without
	// the ones its policy hides; nil when the agent gets one tool per
	// operation.
	Tasks []serving.TaskTool
	// TasksSkipped are the task tools the policy hides, by the step that
	// hid them.
	TasksSkipped []serving.Skipped
	// Signals is the vocabulary that guesses which operations act or touch
	// money; the case's own words on top of the generic ones.
	Signals Signals
}

// Report is the static score of a view.
type Report struct {
	Source string   `json:"source"`
	Level  string   `json:"level,omitempty"`
	Points float64  `json:"points"`
	Max    float64  `json:"max"`
	Tools  int      `json:"tools"`
	Checks []Check  `json:"checks"`
	Notes  []string `json:"notes,omitempty"`
	// Skipped is every operation the gateway does not serve at this level,
	// with the reason: the same list lodog serve warns about at start.
	Skipped []Unserved `json:"skipped,omitempty"`
	// L4 is the changes only the legacy code can make (l4.go).
	L4 *L4 `json:"l4,omitempty"`
}

// Unserved is an operation the agent is not given, and why.
type Unserved struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Check is one scored property.
type Check struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Points   float64  `json:"points"`
	Max      float64  `json:"max"`
	Detail   string   `json:"detail"`
	Examples []string `json:"examples,omitempty"`
}

type checkFunc func(v View, tools []spec.Operation) Check

// checks is the scoring standard; the weights sum to Max.
var checks = []checkFunc{
	descriptions, toolCount, riskDeclared, sideEffectGETs,
	authDeclared, agentIdentity, errorResponses, paging,
}

// Score scores what the agent is given in v.
func Score(v View) Report {
	tools, skipped := visible(v)
	r := Report{Source: v.Source, Level: v.Level, Max: Max, Tools: len(tools), Skipped: skipped}
	for _, c := range checks {
		ch := c(v, tools)
		ch.Points = round2(ch.Points)
		r.Points += ch.Points
		r.Checks = append(r.Checks, ch)
	}
	r.Points = round2(r.Points)
	r.L4 = recommendL4(v, endpoints(v, tools), r)
	return r
}

// visible is the tools the gateway serves, and the operations it does not:
// ones it cannot forward and ones the policy hides never reach the agent.
func visible(v View) ([]spec.Operation, []Unserved) {
	if v.Tasks != nil {
		out := make([]spec.Operation, len(v.Tasks))
		for i, t := range v.Tasks {
			out[i] = taskOperation(t, v.Signals)
		}
		return out, unservedFrom(v.TasksSkipped)
	}
	served, skipped := serving.RawTools(v.Doc)
	out := make([]spec.Operation, len(served))
	for i, t := range served {
		out[i] = t.Op
	}
	return out, unservedFrom(skipped)
}

func unservedFrom(skipped []serving.Skipped) []Unserved {
	unserved := make([]Unserved, len(skipped))
	for i, s := range skipped {
		unserved[i] = Unserved{Method: s.Op.Method, Path: s.Op.Path, Reason: s.Reason}
	}
	return unserved
}

func descriptions(_ View, tools []spec.Operation) Check {
	const weight = 6
	var opsDone, params, paramsDone int
	var missing []string
	for _, op := range tools {
		if strings.TrimSpace(op.Summary+op.Description) != "" {
			opsDone++
		} else {
			missing = append(missing, name(op))
		}
		for _, p := range op.Params {
			params++
			if strings.TrimSpace(p.Description) != "" {
				paramsDone++
			}
		}
	}
	share := ratio(opsDone, len(tools))
	if params > 0 {
		share = (share + ratio(paramsDone, params)) / 2
	}
	return Check{
		ID: "descriptions", Name: "說明覆蓋", Max: weight, Points: weight * share,
		Detail:   fmt.Sprintf("%d/%d 個 tool 有說明，%d/%d 個參數有說明", opsDone, len(tools), paramsDone, params),
		Examples: examples(missing),
	}
}

func toolCount(_ View, tools []spec.Operation) Check {
	const weight = 4
	share := 1.0
	if len(tools) > toolLimit {
		share = float64(toolLimit) / float64(len(tools))
	}
	return Check{
		ID: "tool_count", Name: "Tool 數量", Max: weight, Points: weight * share,
		Detail: fmt.Sprintf("agent 看到 %d 個 tool；%d 個以內拿滿分", len(tools), toolLimit),
	}
}

func riskDeclared(v View, tools []spec.Operation) Check {
	const weight = 5
	var mutating, declared int
	var missing []string
	for _, op := range tools {
		if !v.Signals.ScoreMutating(op) {
			continue
		}
		mutating++
		if declaredRisk(v, op) {
			declared++
		} else {
			missing = append(missing, name(op))
		}
	}
	detail := fmt.Sprintf("%d/%d 個會改資料的 tool 明確宣告了風險等級", declared, mutating)
	if v.Policy != nil {
		detail += fmt.Sprintf("；%d 個只落在 policy 的 default，不算宣告", len(missing))
	}
	return Check{
		ID: "risk_declared", Name: "風險標記", Max: weight, Points: weight * ratioOr1(declared, mutating),
		Detail: detail, Examples: examples(missing),
	}
}

// declaredRisk is true for the operation's own x-agent.risk or a policy rule
// that matches it. The policy default does not count: it gives every
// unmatched operation the same risk, so it cannot tell a careful policy from
// a one-line one.
func declaredRisk(v View, op spec.Operation) bool {
	if op.AgentRisk != "" {
		return true
	}
	return v.Policy != nil && v.Policy.Decide(op.Method, op.Path).Explicit()
}

func sideEffectGETs(v View, tools []spec.Operation) Check {
	const weight = 3
	var open []string
	total := 0
	for _, op := range tools {
		word, ok := v.Signals.ScoreSideEffectGET(op)
		if !ok {
			continue
		}
		total++
		if !neutralized(v, op) {
			open = append(open, name(op)+"（"+word+"）")
		}
	}
	points := 0.0
	if len(open) == 0 {
		points = weight
	}
	return Check{
		ID: "side_effect_gets", Name: "GET 卻會改資料", Max: weight, Points: points,
		Detail:   fmt.Sprintf("%d 個 GET 的名字是動作；%d 個沒有被標風險或擋下，有任何一個就不給分", total, len(open)),
		Examples: examples(open),
	}
}

// neutralized means the agent cannot call a side-effecting GET as a plain
// read: it carries a non-read risk, or the policy holds, refuses or hides it.
func neutralized(v View, op spec.Operation) bool {
	if op.AgentRisk != "" && op.AgentRisk != "read" {
		return true
	}
	return v.Policy != nil && v.Policy.Decide(op.Method, op.Path).Action != policy.Allow
}

func authDeclared(v View, tools []spec.Operation) Check {
	const weight = 2
	secured := 0
	for _, op := range tools {
		if op.Secured {
			secured++
		}
	}
	share := 0.0
	if len(v.Doc.SecuritySchemes) > 0 {
		share = (1 + ratio(secured, len(tools))) / 2
	}
	return Check{
		ID: "auth_declared", Name: "Auth 宣告", Max: weight, Points: weight * share,
		Detail: fmt.Sprintf("securitySchemes：%s；%d/%d 個 tool 標了 security", list(v.Doc.SecuritySchemes), secured, len(tools)),
	}
}

func agentIdentity(v View, _ []spec.Operation) Check {
	const weight = 2
	points, detail := 0.0, "呼叫帶不出是哪個 agent、代表哪個人"
	if v.Identity {
		points, detail = weight, "gateway 給 agent 自己的身份，並帶出代表的人"
	}
	return Check{ID: "agent_identity", Name: "Agent 身份", Max: weight, Points: points, Detail: detail}
}

func errorResponses(_ View, tools []spec.Operation) Check {
	const weight = 5
	documented := 0
	var missing []string
	for _, op := range tools {
		if hasErrorResponse(op) {
			documented++
		} else {
			missing = append(missing, name(op))
		}
	}
	return Check{
		ID: "error_responses", Name: "錯誤回應", Max: weight, Points: weight * ratio(documented, len(tools)),
		Detail:   fmt.Sprintf("%d/%d 個 tool 寫了 4xx/5xx 回應", documented, len(tools)),
		Examples: examples(missing),
	}
}

func hasErrorResponse(op spec.Operation) bool {
	for _, code := range op.Responses {
		if strings.HasPrefix(code, "4") || strings.HasPrefix(code, "5") || code == "default" {
			return true
		}
	}
	return false
}

// paging rewards one paging convention across the API and a documented page
// size limit, so an agent that learned one list can page every list.
func paging(_ View, tools []spec.Operation) Check {
	const weight = 3
	conventions := map[string]int{}
	var lists, bounded int
	for _, op := range tools {
		params := PagingParams(op)
		if len(params) == 0 {
			continue
		}
		lists++
		var names []string
		limited := false
		for _, p := range params {
			names = append(names, strings.ToLower(p.Name))
			if _, ok := p.Schema["maximum"]; ok {
				limited = true
			}
		}
		sort.Strings(names)
		conventions[strings.Join(names, "+")]++
		if limited {
			bounded++
		}
	}
	if lists == 0 {
		return Check{ID: "paging", Name: "分頁一致", Max: weight, Points: weight, Detail: "沒有分頁的列表"}
	}
	common, most := mostCommon(conventions)
	share := (ratio(most, lists) + ratio(bounded, lists)) / 2
	return Check{
		ID: "paging", Name: "分頁一致", Max: weight, Points: weight * share,
		Detail: fmt.Sprintf("%d 個列表；%d 個用最常見的分頁參數 %s；%d 個寫了 page size 上限", lists, most, common, bounded),
	}
}

func mostCommon(m map[string]int) (string, int) {
	best, n := "", 0
	for k, c := range m {
		if c > n || c == n && k < best {
			best, n = k, c
		}
	}
	return best, n
}

func name(op spec.Operation) string {
	return op.Method + " " + op.Path
}

func examples(xs []string) []string {
	if len(xs) > maxExamples {
		return append(xs[:maxExamples:maxExamples], fmt.Sprintf("…還有 %d 個", len(xs)-maxExamples))
	}
	return xs
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "無"
	}
	return strings.Join(xs, ", ")
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// ratioOr1 treats "nothing to check" as fully satisfied: an API with no
// mutating operation has no risk to declare.
func ratioOr1(n, d int) float64 {
	if d == 0 {
		return 1
	}
	return ratio(n, d)
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
