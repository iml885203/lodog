package workflow

import (
	"fmt"
	"strconv"
	"strings"
)

// exprKind is the kind of Arazzo runtime expression lodog evaluates.
type exprKind int

const (
	exprInput      exprKind = iota // $inputs.name
	exprStepOutput                 // $steps.id.outputs.name
	exprBody                       // $response.body, optionally #/json/pointer
	exprStatus                     // $statusCode
)

// expr is one parsed runtime expression.
type expr struct {
	kind    exprKind
	name    string   // input or output name
	step    string   // step id of a step output
	pointer []string // unescaped JSON pointer segments into the response body
}

// parseExpr reads s as a runtime expression. isExpr is false for a literal
// string. A string that embeds an expression ("id-{$inputs.id}") is refused:
// lodog substitutes whole values only.
func parseExpr(s string) (e expr, isExpr bool, err error) {
	if !strings.HasPrefix(s, "$") {
		if strings.Contains(s, "{$") {
			return expr{}, false, fmt.Errorf("%q embeds a runtime expression; only a whole value can be one", s)
		}
		return expr{}, false, nil
	}
	switch {
	case strings.HasPrefix(s, "$inputs."):
		name := strings.TrimPrefix(s, "$inputs.")
		if !validName(name) {
			return expr{}, true, fmt.Errorf("%q: input name %q", s, name)
		}
		return expr{kind: exprInput, name: name}, true, nil
	case strings.HasPrefix(s, "$steps."):
		step, name, ok := strings.Cut(strings.TrimPrefix(s, "$steps."), ".outputs.")
		if !ok || !validName(step) || !validName(name) {
			return expr{}, true, fmt.Errorf("%q must look like $steps.<stepId>.outputs.<name>", s)
		}
		return expr{kind: exprStepOutput, step: step, name: name}, true, nil
	case s == "$statusCode":
		return expr{kind: exprStatus}, true, nil
	case s == "$response.body":
		return expr{kind: exprBody}, true, nil
	case strings.HasPrefix(s, "$response.body#"):
		ptr, err := parsePointer(strings.TrimPrefix(s, "$response.body#"))
		if err != nil {
			return expr{}, true, fmt.Errorf("%q: %w", s, err)
		}
		return expr{kind: exprBody, pointer: ptr}, true, nil
	}
	return expr{}, true, fmt.Errorf("%q is not a runtime expression lodog reads ($inputs, $steps, $response.body, $statusCode)", s)
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '_' && r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// parsePointer splits an RFC 6901 JSON pointer into unescaped segments.
func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("JSON pointer %q must start with /", p)
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return segs, nil
}

// lookup follows pointer segments through decoded JSON. ok is false when a
// segment names nothing, for example the first row of an empty list.
func lookup(v any, pointer []string) (any, bool) {
	for _, seg := range pointer {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, v != nil
}

// checkPointer reports whether pointer can name something in a value of
// schema. A schema that leaves a level open (no properties, no items) accepts
// anything below it.
func checkPointer(schema map[string]any, pointer []string) error {
	node := schema
	for i, seg := range pointer {
		if props, ok := node["properties"].(map[string]any); ok {
			next, ok := props[seg].(map[string]any)
			if !ok {
				return fmt.Errorf("the response has no %q at /%s", seg, strings.Join(pointer[:i], "/"))
			}
			node = next
			continue
		}
		if items, ok := node["items"].(map[string]any); ok {
			if _, err := strconv.Atoi(seg); err != nil {
				return fmt.Errorf("the response is a list at /%s; %q is not an index", strings.Join(pointer[:i], "/"), seg)
			}
			node = items
			continue
		}
		return nil
	}
	return nil
}

// walkValues calls fn on every string in a literal value (a parameter value
// or a request payload), so expressions in it can be checked or replaced.
func walkValues(v any, fn func(s string) error) error {
	switch x := v.(type) {
	case string:
		return fn(x)
	case map[string]any:
		for _, child := range x {
			if err := walkValues(child, fn); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := walkValues(child, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
