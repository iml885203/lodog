package workflow

import (
	"fmt"
	"maps"
	"slices"

	"github.com/iml885203/lodog/internal/spec"
)

// withOperationRequired returns the inputs schema with every input that a step
// passes straight into a field the OpenAPI document requires added to its
// required list, unless the input has a default of its own. A drafted or
// hand-written workflow often marks only the inputs the author thought of; the
// agent then leaves out a field the legacy system insists on, and the call is
// refused only after a person approved it. The caller's schema is not changed.
func withOperationRequired(inputs map[string]any, steps []Step, ops []spec.Operation) map[string]any {
	props, _ := inputs["properties"].(map[string]any)
	if len(props) == 0 {
		return inputs
	}
	have := requiredInputs(inputs)
	added := false
	for _, name := range sortedKeys(requiredTargets(steps, ops)) {
		schema, _ := props[name].(map[string]any)
		if schema == nil || slices.Contains(have, name) || hasDefault(schema) {
			continue
		}
		have = append(have, name)
		added = true
	}
	if !added {
		return inputs
	}
	out := maps.Clone(inputs)
	list := make([]any, len(have))
	for i, n := range have {
		list[i] = n
	}
	out["required"] = list
	return out
}

func hasDefault(schema map[string]any) bool {
	_, plain := schema["default"]
	_, relative := schema[relativeDefault]
	return plain || relative
}

// requiredTargets is the set of inputs a step passes straight into a required
// parameter or a required body field.
func requiredTargets(steps []Step, ops []spec.Operation) map[string]bool {
	targets := map[string]bool{}
	for i, s := range steps {
		if i >= len(ops) {
			break
		}
		for _, p := range s.Parameters {
			declared, err := matchParam(p, ops[i])
			if err == nil && declared.Required {
				markInput(targets, p.Value)
			}
		}
		if s.RequestBody != nil {
			collectRequired(targets, s.RequestBody.Payload, ops[i].Body)
		}
	}
	return targets
}

func markInput(targets map[string]bool, value any) {
	if s, ok := value.(string); ok {
		if e, isExpr, _ := parseExpr(s); isExpr && e.kind == exprInput {
			targets[e.name] = true
		}
	}
}

// collectRequired walks a payload beside the schema it fills. A field the
// schema lists as required that holds "$inputs.<name>" marks that input; an
// object nested in the payload is checked against its own schema, so what it
// requires counts only because the payload sends that object.
func collectRequired(targets map[string]bool, value any, schema map[string]any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	required := requiredFields(schema)
	for key, child := range obj {
		sub, _ := props[key].(map[string]any)
		if slices.Contains(required, key) {
			markInput(targets, child)
		}
		if sub != nil {
			collectRequired(targets, child, sub)
		}
	}
}

// requiredFields is a schema's required list, however the parser kept it.
func requiredFields(schema map[string]any) []string {
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
