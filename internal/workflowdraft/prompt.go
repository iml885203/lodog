package workflowdraft

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

const promptHead = `You design the task tools an AI agent gets for a legacy HTTP API.
The agent works for the people who operate the system day to day (operations, support, finance). Today it sees one tool per operation and must guess which to combine.
Group the operations below into about 20 task tools (at most 25, fewer when the API is small) that cover what these people do every day: finding a record, reading its details, listing recent or pending items, plus the everyday writes they ask for (the gateway holds those for human approval).
Work only from the operations listed; do not mention domains or tools the API does not have.
Leave out internal, test and debug endpoints, password or OTP resets, and configuration screens nobody uses daily.

Rules for each tool:
- name: snake_case, verb first, unique, at most 64 characters (e.g. find_customer, list_pending_orders).
- summary: one line; description: what it returns; whenToUse / whenNotToUse: routing hints naming the other tools.
- risk: read, write, money, irreversible or credential.
- inputs: plain names an operator would use (username, customerId, orderId, from, to, page, pageSize). Each input is a JSON schema with a type and a description; add "required": true when the tool cannot run without it.
  Lists: expose page (default 1) and pageSize (an integer with a default and "maximum": 50) and pass them to the operation's paging parameters.
  Date ranges an operation needs: expose from/to with "format": "date-time" and "x-default": "now-30d" / "now" (relative defaults: now, now-<n>d, now+<n>h), so the call is always bounded.
  Every input must be used by a step.
  A parameter or body field listed with values in braces, such as channelType{1 = Email, 2 = LiveChat}, is a code: when an input fills it, give the input the same type, list the values and their names in its description, and never say the codes are not documented.
- steps: usually one; chain two only when the second operation needs an id the first returns. Each step has an id, "operation": "METHOD /path" exactly as listed, "parameters": {paramName: value}, an optional JSON "body", and "outputs": {name: "$response.body#/json/pointer"} for values a later step or the tool returns.
  A value is a literal, "$inputs.<input>" or "$steps.<id>.outputs.<name>". Fix a parameter to a literal when it selects what the tool is about (e.g. a transaction type). Set every required parameter. A body field marked * is required: when an input fills it, mark that input "required": true (unless it has a default), so the agent cannot leave it out and have the call refused after a person approved it.
  Output pointers must exist in the listed result shape.
- outputs (optional): {name: "$steps.<id>.outputs.<name>"}; without them the tool returns the last step's response.
- next: names of the tools usually called after this one.
- undoes: only on a tool that takes back another write tool: that tool's name. For every write tool, also draft the tool that undoes it when the operations list one that restores what the write changed (the DELETE, PATCH or PUT of the same resource, or the counterpart that sets the opposite state). An undo tool is a write: its risk is never read, and its description says which tool it undoes. Do not invent an undo the operations do not offer.
- confident: false when the operations' descriptions do not settle what a parameter or value means; a person reviews those.

Also write "instructions": a short map for the agent (the domains, which tool to start from, the usual order, that writes wait for human approval).

Answer with only a JSON object:
{"instructions": "...", "tools": [{"name": "...", "summary": "...", "description": "...", "whenToUse": "...", "whenNotToUse": "...", "risk": "read", "confident": true,
  "inputs": {"name": {"type": "string", "description": "...", "required": true}},
  "steps": [{"id": "search", "operation": "GET /api/...", "parameters": {"Name": "$inputs.name"}, "outputs": {"id": "$response.body#/data/0/id"}}],
  "outputs": {}, "next": []}]}

Operations (method path | summary | when to use | parameters, * = required | body fields | result shape):
`

// prompt lists every operation the gateway can serve, as the overlay
// describes it.
func prompt(doc *spec.Document) string {
	var b strings.Builder
	b.WriteString(promptHead)
	served, _ := serving.RawTools(doc)
	for _, t := range served {
		op := t.Op
		fmt.Fprintf(&b, "%s %s | %s | %s | %s | %s | %s\n", op.Method, op.Path, oneLine(op.Summary), oneLine(op.WhenToUse),
			params(op), strings.Join(fields(op.Body), ","), outline(op.Result, 4))
	}
	return b.String()
}

func retryPrompt(doc *spec.Document, previous string, bad map[string]string) string {
	var b strings.Builder
	b.WriteString(prompt(doc))
	b.WriteString("\nYour previous answer:\n")
	b.WriteString(previous)
	b.WriteString("\n\nThese tools do not load. Fix them and answer with the same JSON object shape, holding only the fixed tools (and instructions if they were the problem):\n")
	names := make([]string, 0, len(bad))
	for n := range bad {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "- %s: %s\n", n, oneLine(bad[n]))
	}
	return b.String()
}

func params(op spec.Operation) string {
	parts := make([]string, 0, len(op.Params))
	for _, p := range op.Params {
		s := p.Name
		if p.Required {
			s += "*"
		}
		if t, ok := p.Schema["type"].(string); ok {
			s += ":" + t
		}
		if p.Description != "" {
			s += " (" + clipText(oneLine(p.Description), 80) + ")"
		}
		if list := spec.EnumList(p.Schema); list != "" {
			s += " {" + list + "}"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// fields lists a body's top-level fields, a required one with a *; a field with an enum carries its
// values, with their names when the document gives them: channelType{1 = Email, 2 = LiveChat}.
func fields(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	out := make([]string, 0, len(props))
	for name, raw := range props {
		field, _ := raw.(map[string]any)
		if slices.Contains(requiredFieldNames(schema), name) {
			name += "*"
		}
		if list := spec.EnumList(field); list != "" {
			name += "{" + list + "}"
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func requiredFieldNames(schema map[string]any) []string {
	var out []string
	switch list := schema["required"].(type) {
	case []any:
		for _, r := range list {
			out = append(out, fmt.Sprint(r))
		}
	case []string:
		out = list
	}
	return out
}

// outline is a compact picture of a response schema, enough to write a JSON
// pointer into it: {data:{items:[{id,name}],total}}.
func outline(schema map[string]any, depth int) string {
	if schema == nil {
		return "?"
	}
	if items, ok := schema["items"].(map[string]any); ok {
		return "[" + outline(items, depth) + "]"
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || depth == 0 {
		if t, ok := schema["type"].(string); ok {
			return t
		}
		return "any"
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		child, _ := props[name].(map[string]any)
		if _, nested := child["properties"]; nested {
			parts = append(parts, name+":"+outline(child, depth-1))
			continue
		}
		if items, ok := child["items"].(map[string]any); ok {
			parts = append(parts, name+":["+outline(items, depth-1)+"]")
			continue
		}
		parts = append(parts, name)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
