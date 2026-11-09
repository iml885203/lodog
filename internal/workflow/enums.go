package workflow

import (
	"maps"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// withOperationEnums returns the inputs schema with the enum of every input
// that a step passes straight into an operation field that has one. A drafted
// or hand-written input often says only "the channel type" while the document
// lists the codes and their names; the agent should never have to ask a person
// for what the document already says. An input that already has its own enum
// keeps it. The caller's schema is not changed.
func withOperationEnums(inputs map[string]any, steps []Step, ops []spec.Operation) map[string]any {
	props, _ := inputs["properties"].(map[string]any)
	if len(props) == 0 {
		return inputs
	}
	targets := enumTargets(steps, ops)
	if len(targets) == 0 {
		return inputs
	}
	shown := maps.Clone(props)
	for name, target := range targets {
		if schema, ok := props[name].(map[string]any); ok {
			shown[name] = mergeEnum(schema, target)
		}
	}
	out := maps.Clone(inputs)
	out["properties"] = shown
	return out
}

// enumTargets maps each input a step passes straight into a field with an enum
// to that field's schema.
func enumTargets(steps []Step, ops []spec.Operation) map[string]map[string]any {
	targets := map[string]map[string]any{}
	for i, s := range steps {
		if i >= len(ops) {
			break
		}
		for _, p := range s.Parameters {
			if declared, err := matchParam(p, ops[i]); err == nil {
				collectEnum(targets, p.Value, declared.Schema)
			}
		}
		if s.RequestBody != nil {
			collectEnum(targets, s.RequestBody.Payload, ops[i].Body)
		}
	}
	return targets
}

// mergeEnum is the input's schema with the target's enum and a sentence that
// lists its values. The input keeps an enum of its own, and one of another type
// is not copied (the agent still gets the sentence).
func mergeEnum(input, target map[string]any) map[string]any {
	merged := maps.Clone(input)
	if _, has := merged["enum"]; !has && sameType(input, target) {
		merged["enum"] = target["enum"]
		if names, ok := target["x-enumNames"]; ok {
			merged["x-enumNames"] = names
		}
	}
	source := merged
	if _, has := merged["enum"]; !has {
		source = target
	}
	if sentence := spec.EnumDescription(source); sentence != "" {
		d, _ := merged["description"].(string)
		if !strings.Contains(d, sentence) {
			merged["description"] = spec.AppendSentence(d, sentence)
		}
	}
	return merged
}

// collectEnum walks a step value beside the schema of the field it fills and
// records, for each "$inputs.<name>" placed directly in a field with an enum,
// that field's schema.
func collectEnum(targets map[string]map[string]any, value any, schema map[string]any) {
	switch v := value.(type) {
	case string:
		e, isExpr, _ := parseExpr(v)
		if isExpr && e.kind == exprInput && schema["enum"] != nil {
			if _, seen := targets[e.name]; !seen {
				targets[e.name] = schema
			}
		}
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for key, child := range v {
			sub, _ := props[key].(map[string]any)
			if sub != nil {
				collectEnum(targets, child, sub)
			}
		}
	}
}

func sameType(a, b map[string]any) bool {
	ta, _ := a["type"].(string)
	tb, _ := b["type"].(string)
	return tb == "" || ta == tb
}
