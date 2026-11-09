package enrich

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// Prompt asks the model to describe a batch of operations for an agent that
// will call them. withSource tells it the legacy source is available to read.
func Prompt(ops []spec.Operation, withSource bool, risks []string) string {
	if len(risks) == 0 {
		risks = DefaultRisks()
	}
	var b strings.Builder
	b.WriteString(strings.NewReplacer("RISKS", strings.Join(risks, ", "), "RISKDEFS", riskDefinitions(risks)).Replace(`You describe operations of a legacy back-office REST API for an AI agent that will call them
as tools. The agent sees only what you write, so be specific and short. Write in English.

For every operation below return:
- summary: one line, what it does, in business terms (not "GET endpoint").
- description: what it returns or changes, and anything surprising (required time windows, paging,
  side effects of a GET, statuses it filters). At most three sentences.
- whenToUse: the operator requests this operation answers.
- whenNotToUse: the nearby requests it does NOT answer, and which operation to use instead if you can tell.
- risk: one of RISKS. A GET that changes state is NOT read.
RISKDEFS- params: for each parameter by name, its meaning, allowed values (spell out enum numbers, e.g.
  "0 = draft, 1 = published"), format, and where an id comes from (e.g. "orderId from
  the list-orders operation"). Only parameters listed for the operation.
- errors: the failures an agent will meet, by HTTP status (4xx/5xx only), each with when it happens and
  how to fix the call, e.g. {"400": "PageSize or PageNumber missing; send both"}. Leave out what you
  cannot tell.
- confidence: high only if you are sure of the meanings; otherwise low, and say what is unsure in notes.
`))
	if withSource {
		b.WriteString(`
The API's source code is available in your working directory. Before answering, read the handlers
and the enums the parameters use (Grep for the route and the parameter types) so meanings and enum
values come from the code, not from guesses. Never modify files.
`)
	} else {
		b.WriteString(`
Only the API document is available. Infer from names and schemas; mark confidence low wherever a
meaning or an enum value is a guess.
`)
	}
	b.WriteString(`
Answer with only this JSON, no other text:
{"operations":[{"method":"GET","path":"/api/...","summary":"","description":"","whenToUse":"",
"whenNotToUse":"","risk":"read","params":{"Name":"meaning"},"errors":{"400":"when and how to fix"},
"confidence":"high","notes":""}]}

Operations:
`)
	for _, op := range ops {
		b.WriteString(describeOp(op))
	}
	return b.String()
}

func describeOp(op spec.Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s %s", op.Method, op.Path)
	if op.Tag != "" {
		fmt.Fprintf(&b, "  [tag %s]", op.Tag)
	}
	b.WriteString("\n")
	for _, p := range op.Params {
		req := ""
		if p.Required {
			req = ", required"
		}
		fmt.Fprintf(&b, "  param %s (%s%s): %s\n", p.ArgName(), p.In, req, schemaHint(p.Schema))
	}
	if op.Body != nil {
		fmt.Fprintf(&b, "  body (%s): %s\n", op.BodyContentType, schemaHint(op.Body))
	}
	return b.String()
}

// schemaHint is a compact view of a schema: type, enum and, for objects, the
// top-level fields. Deep schemas are cut so a batch stays within reason.
func schemaHint(s map[string]any) string {
	if s == nil {
		return "unknown"
	}
	parts := []string{}
	if t, ok := s["type"].(string); ok {
		parts = append(parts, t)
	}
	if f, ok := s["format"].(string); ok {
		parts = append(parts, f)
	}
	if list := spec.EnumList(s); list != "" {
		parts = append(parts, "enum {"+list+"}")
	}
	if props, ok := s["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fields := []string{}
		for _, k := range keys {
			sub, _ := props[k].(map[string]any)
			fields = append(fields, k+":"+shortType(sub))
		}
		parts = append(parts, "{"+strings.Join(fields, ", ")+"}")
	}
	if items, ok := s["items"].(map[string]any); ok {
		parts = append(parts, "of "+schemaHint(items))
	}
	out := strings.Join(parts, " ")
	if len(out) > 600 {
		out = out[:600] + "…"
	}
	return out
}

func shortType(s map[string]any) string {
	if s == nil {
		return "?"
	}
	t, _ := s["type"].(string)
	if list := spec.EnumList(s); list != "" {
		return t + " enum {" + list + "}"
	}
	if t == "" {
		return "object"
	}
	return t
}

// riskDefinitions explains the levels the model is most likely to meet. A
// level it does not know is left to its name, which the policy chose.
func riskDefinitions(risks []string) string {
	var b strings.Builder
	for _, r := range risks {
		if d := riskMeaning(r); d != "" {
			fmt.Fprintf(&b, "  %s = %s.\n", r, d)
		}
	}
	return b.String()
}

func riskMeaning(risk string) string {
	switch risk {
	case "read":
		return "changes nothing"
	case "write":
		return "changes data"
	case "money":
		return "moves money or anything of monetary value (credits, points, rewards)"
	case "irreversible":
		return "cannot be undone or reaches outside the system (payments, deletes, messages)"
	case "credential":
		return "passwords, OTPs, unlocking accounts"
	}
	return ""
}
