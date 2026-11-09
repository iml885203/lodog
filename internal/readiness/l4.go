package readiness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

// Status values of a published agent standard.
const (
	StandardFound     = "found"
	StandardMissing   = "missing"
	StandardUnchecked = "unchecked"
)

var (
	dryRunParam = regexp.MustCompile(`(?i)^(dry_?run|validate_?only|preview|simulate)$`)
	idempotency = regexp.MustCompile(`(?i)^(idempotency[-_]?key|(client[-_]?)?request[-_]?id)$`)
)

// L4 is what only a change to the legacy code can improve (docs/levels.md):
// the part of the static score L1 to L3 cannot reach, plus the practices
// that are not in the static score. lodog recommends; the owner decides.
type L4 struct {
	// Gain is the static points the scored items add up to.
	Gain      float64    `json:"gain"`
	Items     []L4Item   `json:"items"`
	Standards []Standard `json:"standards"`
}

// L4Item is one change to the legacy system.
type L4Item struct {
	ID   string `json:"id"`
	Name Text   `json:"name"`
	// Check is the static check this item earns points in; empty when the
	// item is not in the static score.
	Check string `json:"check,omitempty"`
	// Measured says where the item shows up in the score.
	Measured Text `json:"measured"`
	// Gain is the static points the change adds: the check's maximum minus
	// what it scores now. 0 for an item outside the static score.
	Gain float64 `json:"gain"`
	// Handled is true for an item in the static score whose check is already
	// full (an overlay, a policy or the gateway took care of it): it earns
	// nothing more, but the legacy code should still change.
	Handled  bool   `json:"handled,omitempty"`
	Detail   Text   `json:"detail"`
	Examples []Text `json:"examples,omitempty"`
	// Instruction is written to be pasted to a coding agent as it stands.
	Instruction Text `json:"instruction"`
	// Lead is the instruction's first sentence, the one-line hint of what to
	// change.
	Lead Text `json:"-"`
	// Endpoints are the endpoints the item asks the legacy API to add.
	Endpoints []MissingEndpoint `json:"endpoints,omitempty"`
}

// Counted is true for an item that still adds static points; the rest are
// listed apart, because the legacy code should change all the same.
func (it L4Item) Counted() bool {
	return it.Check != "" && !it.Handled
}

// CountedItems are the items that still add static points, in list order.
func (l *L4) CountedItems() []L4Item {
	return l.items(true)
}

// UncountedItems are the items outside the static score or already full.
func (l *L4) UncountedItems() []L4Item {
	return l.items(false)
}

func (l *L4) items(counted bool) []L4Item {
	var out []L4Item
	for _, it := range l.Items {
		if it.Counted() == counted {
			out = append(out, it)
		}
	}
	return out
}

// Standard is a public agent standard a native system publishes.
type Standard struct {
	ID     string `json:"id"`
	Name   Text   `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail Text   `json:"detail"`
}

// standards are the well-known documents of docs/reference-isitagentready.md.
var standards = []Standard{
	{ID: "mcp_server_card", Name: Lit("MCP Server Card"), Path: "/.well-known/mcp/server-card.json"},
	{ID: "oauth_protected_resource", Name: say("rd.std.oauth_protected_resource"), Path: "/.well-known/oauth-protected-resource"},
	{ID: "api_catalog", Name: say("rd.std.api_catalog"), Path: "/.well-known/api-catalog"},
}

// recommendL4 lists the code changes for what v gives the agent. The gain of a
// scored item is read off the check it belongs to, so it moves with the
// scoring standard.
func recommendL4(v View, tools []spec.Operation, r Report) *L4 {
	l4 := &L4{Standards: uncheckedStandards()}
	byID := map[string]Check{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	scored := func(it L4Item) {
		c := byID[it.Check]
		it.Gain = round2(c.Max - c.Points)
		it.Measured = say("rd.l4.static", c.Name)
		if it.Gain == 0 {
			it.Handled = true
			it.Measured = say("rd.l4.static.full", c.Name)
		}
		l4.Gain += it.Gain
		l4.Items = append(l4.Items, it)
	}
	if it, ok := sideEffectGETItem(v); ok {
		scored(it)
	}
	if it, ok := errorItem(tools); ok {
		scored(it)
	}
	if it, ok := authItem(v, tools); ok {
		scored(it)
	}
	scored(identityItem())
	if it, ok := pagingItem(tools); ok {
		scored(it)
	}
	for _, it := range []func(View, []spec.Operation) (L4Item, bool){dryRunItem, idempotencyItem} {
		if item, ok := it(v, tools); ok {
			item.Measured = say("rd.l4.dynamic")
			l4.Items = append(l4.Items, item)
		}
	}
	if it, ok := missingItem(v, tools); ok {
		l4.Items = append(l4.Items, it)
	}
	l4.Gain = round2(l4.Gain)
	return l4
}

func uncheckedStandards() []Standard {
	out := make([]Standard, len(standards))
	copy(out, standards)
	for i := range out {
		out[i].Status = StandardUnchecked
		out[i].Detail = say("rd.std.unchecked")
	}
	return out
}

// sideEffectGETItem looks at every operation the gateway can forward, hidden
// or not: a GET that policy hides still has to change in the legacy code.
func sideEffectGETItem(v View) (L4Item, bool) {
	served, _ := serving.RawTools(v.Doc)
	var found []Text
	var byName, byDeclaration int
	for _, t := range served {
		op := t.Op
		if op.Method != "GET" || !v.Signals.ScoreMutating(op) {
			continue
		}
		var word Text
		if w, ok := v.Signals.ScoreSideEffectGET(op); ok {
			byName++
			word = Lit(w)
		} else {
			byDeclaration++
			word = say("rd.l4.side_effect_gets.declared", op.AgentRisk)
		}
		found = append(found, say("rd.marked", name(op), word))
	}
	if len(found) == 0 {
		return L4Item{}, false
	}
	var sources []Text
	if byName > 0 {
		sources = append(sources, say("rd.l4.side_effect_gets.byname", byName))
	}
	if byDeclaration > 0 {
		sources = append(sources, say("rd.l4.side_effect_gets.byrisk", byDeclaration))
	}
	return withFix(L4Item{
		ID: "side_effect_gets", Name: say("rd.l4.side_effect_gets.name"), Check: "side_effect_gets",
		Detail:   say("rd.l4.side_effect_gets.detail", joined("rd.join", sources)),
		Examples: examples(found),
	}, say("rd.l4.side_effect_gets.lead"), "rd.l4.side_effect_gets.fix", bullets(found)), true
}

func errorItem(tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if !hasErrorResponse(op) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	listed := examples(lits(missing))
	return withFix(L4Item{
		ID: "error_responses", Name: say("rd.l4.error_responses.name"), Check: "error_responses",
		Detail:   say("rd.l4.error_responses.detail", len(missing), len(tools)),
		Examples: listed,
	}, say("rd.l4.error_responses.lead"), "rd.l4.error_responses.fix", bullets(listed)), true
}

func authItem(v View, tools []spec.Operation) (L4Item, bool) {
	var open []string
	for _, op := range tools {
		if !op.Secured {
			open = append(open, name(op))
		}
	}
	if len(v.Doc.SecuritySchemes) > 0 && len(open) == 0 {
		return L4Item{}, false
	}
	return withFix(L4Item{
		ID: "auth_declared", Name: say("rd.l4.auth_declared.name"), Check: "auth_declared",
		Detail:   say("rd.l4.auth_declared.detail", list(v.Doc.SecuritySchemes), len(open), len(tools)),
		Examples: examples(lits(open)),
	}, say("rd.l4.auth_declared.lead"), "rd.l4.auth_declared.fix"), true
}

func identityItem() L4Item {
	return withFix(L4Item{
		ID: "agent_identity", Name: say("rd.l4.agent_identity.name"), Check: "agent_identity",
		Detail: say("rd.l4.agent_identity.detail"),
	}, say("rd.l4.agent_identity.lead"), "rd.l4.agent_identity.fix")
}

func pagingItem(tools []spec.Operation) (L4Item, bool) {
	conventions := map[string]int{}
	convOf := map[string]string{}
	var lists, bounded int
	var unbounded []string
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
		key := strings.Join(names, "+")
		conventions[key]++
		convOf[name(op)] = key
		if limited {
			bounded++
		} else {
			unbounded = append(unbounded, name(op))
		}
	}
	if lists == 0 {
		return L4Item{}, false
	}
	common, most := mostCommon(conventions)
	var odd []string
	for n, k := range convOf {
		if k != common {
			odd = append(odd, n)
		}
	}
	sort.Strings(odd)
	if most == lists && bounded == lists {
		return L4Item{}, false
	}
	listed := examples(lits(union(odd, unbounded)))
	return withFix(L4Item{
		ID: "paging", Name: say("rd.l4.paging.name"), Check: "paging",
		Detail:   say("rd.l4.paging.detail", lists, len(odd), common, len(unbounded)),
		Examples: listed,
	}, say("rd.l4.paging.lead", common), "rd.l4.paging.fix", bullets(listed)), true
}

func dryRunItem(v View, tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if v.Signals.ScoreMutating(op) && !hasInput(op, dryRunParam) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	listed := examples(lits(missing))
	return withFix(L4Item{
		ID: "dry_run", Name: say("rd.l4.dry_run.name"),
		Detail:   say("rd.l4.dry_run.detail", len(missing)),
		Examples: listed,
	}, say("rd.l4.dry_run.lead"), "rd.l4.dry_run.fix", bullets(listed)), true
}

func idempotencyItem(v View, tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if v.Signals.ScoreMutating(op) && !hasInput(op, idempotency) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	listed := examples(lits(missing))
	return withFix(L4Item{
		ID: "idempotency_key", Name: say("rd.l4.idempotency_key.name"),
		Detail:   say("rd.l4.idempotency_key.detail", len(missing)),
		Examples: listed,
	}, say("rd.l4.idempotency_key.lead"), "rd.l4.idempotency_key.fix", bullets(listed)), true
}

// hasInput reports whether op takes a query, header or cookie parameter, or a top-level body
// field whose name matches re.
func hasInput(op spec.Operation, re *regexp.Regexp) bool {
	for _, p := range op.Params {
		if p.In != "path" && re.MatchString(p.Name) {
			return true
		}
	}
	props, _ := op.Body["properties"].(map[string]any)
	for k := range props {
		if re.MatchString(k) {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// withFix sets the item's instruction: the template fix, whose {0} is the
// lead and whose other places are args.
func withFix(it L4Item, lead Text, fix string, args ...any) L4Item {
	it.Lead = lead
	it.Instruction = say(fix, append([]any{lead}, args...)...)
	return it
}

// bullets is one line per x; the line that says how many more there are asks
// for those too.
func bullets(xs []Text) Text {
	lines := make([]Text, len(xs))
	for i, x := range xs {
		key := "rd.bullet"
		if x.Key == "rd.more" {
			key = "rd.bullet.more"
		}
		lines[i] = say(key, x)
	}
	return joined("rd.lines", lines)
}

// CheckStandards looks for each public agent standard under origin and
// records what it found on the report. It is the one part of the score that
// touches the network, so the caller opts in with an origin.
func CheckStandards(ctx context.Context, client *http.Client, origin string, r *Report) error {
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("origin %q must be an absolute URL such as https://api.example.com", origin)
	}
	if r.L4 == nil {
		return nil
	}
	for i, s := range r.L4.Standards {
		r.L4.Standards[i].Status, r.L4.Standards[i].Detail = probe(ctx, client, base.ResolveReference(&url.URL{Path: s.Path}).String())
	}
	return nil
}

func probe(ctx context.Context, client *http.Client, target string) (string, Text) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return StandardMissing, Lit(err.Error())
	}
	resp, err := client.Do(req)
	if err != nil {
		return StandardMissing, say("rd.std.unreachable", err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	// A catch-all page answers 200 for any path; only JSON counts as the document.
	if resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return StandardFound, Lit(target)
	}
	return StandardMissing, say("rd.std.status", target, resp.StatusCode)
}

func renderL4(b *strings.Builder, l4 *L4) {
	if l4 == nil {
		return
	}
	if l4.Gain == 0 {
		b.WriteString("\nL4：要改原系統 code 才拿得到的（靜態分已經沒有可再加的）\n")
	} else {
		fmt.Fprintf(b, "\nL4：要改原系統 code 才拿得到的（靜態分最多再 +%.2f）\n", l4.Gain)
	}
	for _, it := range l4.CountedItems() {
		renderItem(b, fmt.Sprintf("+%.2f 分", it.Gain), it)
	}
	if uncounted := l4.UncountedItems(); len(uncounted) > 0 {
		b.WriteString("\n靜態分不計、原系統仍建議改（已由 overlay、policy 或 gateway 處理的，或只在動態分看得到的）\n")
		for _, it := range uncounted {
			renderItem(b, "·", it)
		}
	}
	b.WriteString("\n公開的 agent 標準（參考 docs/reference-isitagentready.md，不計分）\n")
	for _, s := range l4.Standards {
		fmt.Fprintf(b, "  %-8s %s %s：%s\n", s.Status, s.Name, s.Path, s.Detail)
	}
}

func renderItem(b *strings.Builder, mark string, it L4Item) {
	fmt.Fprintf(b, "\n%s  %s（%s）\n    %s\n", mark, it.Name, it.Measured, it.Detail)
	for _, e := range it.Examples {
		fmt.Fprintf(b, "      - %s\n", e)
	}
	fmt.Fprintf(b, "    貼給 coding agent：\n%s\n", indent(it.Instruction.String(), "      "))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
