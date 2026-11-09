package workflow

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// relativeDefault is lodog's extension on an input schema: a default time
// relative to the call, such as now-30d. It is how a task tool always sends a
// bounded date range without the agent having to compute one.
const relativeDefault = "x-default"

var relativeTime = regexp.MustCompile(`^now(?:([+-])(\d+)([dh]))?$`)

// parseRelative reads now, now-30d or now+1h into an offset.
func parseRelative(s string) (time.Duration, error) {
	m := relativeTime.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%s %q must look like now, now-30d or now+12h", relativeDefault, s)
	}
	if m[1] == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", relativeDefault, s, err)
	}
	unit := time.Hour
	if m[3] == "d" {
		unit = 24 * time.Hour
	}
	d := time.Duration(n) * unit
	if m[1] == "-" {
		d = -d
	}
	return d, nil
}

// InputError is an argument the agent can fix.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Prepare checks the agent's arguments against the inputs schema and fills
// in defaults. now anchors relative defaults.
func (t Task) Prepare(args map[string]any, now time.Time) (map[string]any, error) {
	props, _ := t.Workflow.Inputs["properties"].(map[string]any)
	out := maps.Clone(args)
	if out == nil {
		out = map[string]any{}
	}
	for _, name := range sortedKeys(props) {
		schema, _ := props[name].(map[string]any)
		v, given := out[name]
		if !given || v == nil {
			filled, ok, err := defaultFor(schema, now)
			if err != nil {
				return nil, err
			}
			if ok {
				out[name] = filled
			} else {
				delete(out, name)
			}
			continue
		}
		if err := checkArg(name, schema, v); err != nil {
			return nil, err
		}
	}
	var missing []string
	for _, name := range requiredInputs(t.Workflow.Inputs) {
		if _, ok := out[name]; !ok {
			missing = append(missing, fmt.Sprintf("%q", name))
		}
	}
	if len(missing) == 1 {
		return nil, &InputError{fmt.Sprintf("argument %s is required", missing[0])}
	}
	if len(missing) > 1 {
		return nil, &InputError{fmt.Sprintf("arguments %s are required", strings.Join(missing, ", "))}
	}
	for _, name := range sortedKeys(args) {
		if _, ok := props[name]; !ok {
			return nil, &InputError{fmt.Sprintf("%q is not an argument of %s; it takes %s", name, t.Workflow.WorkflowID, argList(props))}
		}
	}
	return out, nil
}

func defaultFor(schema map[string]any, now time.Time) (any, bool, error) {
	if v, ok := schema["default"]; ok {
		return v, true, nil
	}
	rel, ok := schema[relativeDefault]
	if !ok {
		return nil, false, nil
	}
	d, err := parseRelative(fmt.Sprint(rel))
	if err != nil {
		return nil, false, err
	}
	at := now.Add(d).UTC()
	if schema["format"] == "date" {
		return at.Format(time.DateOnly), true, nil
	}
	return at.Format(time.RFC3339), true, nil
}

func checkArg(name string, schema map[string]any, v any) error {
	typ, _ := schema["type"].(string)
	if want, ok := typeOK(typ, v); !ok {
		return &InputError{fmt.Sprintf("argument %q must be %s, got %v", name, want, v)}
	}
	if f, isNum := v.(float64); isNum && (typ == "integer" || typ == "number") {
		if err := checkRange(name, schema, f); err != nil {
			return err
		}
	}
	return checkEnum(name, schema, v)
}

// checkEnum holds v to the schema's enum, when it has one.
func checkEnum(name string, schema map[string]any, v any) error {
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return sameValue(e, v) }) {
		return &InputError{fmt.Sprintf("argument %q must be one of %v, got %v", name, enum, v)}
	}
	return nil
}

// typeOK reports whether v is a JSON value of the schema type, and names the
// type for the agent when it is not.
func typeOK(typ string, v any) (string, bool) {
	switch typ {
	case "integer":
		f, ok := v.(float64)
		return "a whole number", ok && f == float64(int64(f))
	case "number":
		_, ok := v.(float64)
		return "a number", ok
	case "string":
		_, ok := v.(string)
		return "a string", ok
	case "boolean":
		_, ok := v.(bool)
		return "true or false", ok
	case "array":
		_, ok := v.([]any)
		return "a list", ok
	case "object":
		_, ok := v.(map[string]any)
		return "an object", ok
	}
	return "", true
}

func checkRange(name string, schema map[string]any, f float64) error {
	if maxV, ok := number(schema["maximum"]); ok && f > maxV {
		return &InputError{fmt.Sprintf("argument %q is at most %v, got %v", name, maxV, f)}
	}
	if minV, ok := number(schema["minimum"]); ok && f < minV {
		return &InputError{fmt.Sprintf("argument %q is at least %v, got %v", name, minV, f)}
	}
	return nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func sameValue(a, b any) bool {
	fa, aNum := number(a)
	fb, bNum := number(b)
	if aNum && bNum {
		return fa == fb
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func argList(props map[string]any) string {
	if len(props) == 0 {
		return "no arguments"
	}
	return strings.Join(sortedKeys(props), ", ")
}

// InputSchema is the inputs schema as a tool shows it: lodog's relative
// default becomes a sentence in the description, since a JSON schema default
// cannot say "30 days before the call".
func (t Task) InputSchema() map[string]any {
	out := map[string]any{"type": "object"}
	props, _ := t.Workflow.Inputs["properties"].(map[string]any)
	shown := make(map[string]any, len(props))
	for name, raw := range props {
		schema, _ := raw.(map[string]any)
		copied := maps.Clone(schema)
		if rel, ok := copied[relativeDefault]; ok {
			delete(copied, relativeDefault)
			note := "Default: " + describeRelative(fmt.Sprint(rel)) + "."
			if d, _ := copied["description"].(string); d != "" {
				note = d + " " + note
			}
			copied["description"] = note
		}
		shown[name] = copied
	}
	out["properties"] = shown
	if req := requiredInputs(t.Workflow.Inputs); len(req) > 0 {
		out["required"] = req
	}
	return out
}

func describeRelative(rel string) string {
	m := relativeTime.FindStringSubmatch(rel)
	if m == nil || m[1] == "" {
		return "the time of the call"
	}
	unit := map[string]string{"d": "days", "h": "hours"}[m[3]]
	dir := "before"
	if m[1] == "+" {
		dir = "after"
	}
	return fmt.Sprintf("%s %s %s the call", m[2], unit, dir)
}
