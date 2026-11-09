package proxy

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// HeaderDryRun is set on every made-up answer, so a person reading the
// traffic can tell it from the system's.
const HeaderDryRun = "X-Lodog-Dry-Run"

// maxStubDepth stops a deeply nested schema; the resolver already cuts cycles.
const maxStubDepth = 8

// writeStub answers a call that was not sent: the operation's lowest declared
// 2xx status with a body shaped like its JSON schema. A call outside the
// document gets 403, because there is no success to make up for it.
func writeStub(w http.ResponseWriter, op *spec.Operation, reason string) int {
	w.Header().Set(HeaderDryRun, "not sent: "+reason)
	if op == nil {
		http.Error(w, "lodog read-only proxy: not sent: "+reason, http.StatusForbidden)
		return http.StatusForbidden
	}
	status := successStatus(op.Responses)
	if op.Result == nil || status == http.StatusNoContent {
		w.WriteHeader(status)
		return status
	}
	body, err := json.Marshal(example(op.Result, 0))
	if err != nil {
		body = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return status
}

func successStatus(codes []string) int {
	for _, c := range codes {
		if n, err := strconv.Atoi(c); err == nil && n >= 200 && n < 300 {
			return n
		}
	}
	return http.StatusOK
}

// example is the plainest value that satisfies schema: its default or first
// enum value when it has one, every property of an object, empty lists,
// zero numbers, false, and empty strings or a zero time for a formatted one.
func example(schema map[string]any, depth int) any {
	if v, ok := schema["default"]; ok {
		return v
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if all, ok := schema["allOf"].([]any); ok {
		return mergeAll(all, depth)
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if alts, ok := schema[k].([]any); ok && len(alts) > 0 {
			if first, ok := alts[0].(map[string]any); ok {
				return example(first, depth)
			}
		}
	}
	switch schemaType(schema) {
	case "object":
		return object(schema, depth)
	case "array":
		return []any{}
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "string":
		return formatted(schema)
	}
	if _, ok := schema["properties"]; ok {
		return object(schema, depth)
	}
	return nil
}

func schemaType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

func object(schema map[string]any, depth int) map[string]any {
	out := map[string]any{}
	if depth >= maxStubDepth {
		return out
	}
	props, _ := schema["properties"].(map[string]any)
	for name, p := range props {
		if ps, ok := p.(map[string]any); ok {
			out[name] = example(ps, depth+1)
		}
	}
	return out
}

func mergeAll(parts []any, depth int) any {
	out := map[string]any{}
	for _, p := range parts {
		ps, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if m, ok := example(ps, depth).(map[string]any); ok {
			for k, v := range m {
				out[k] = v
			}
		}
	}
	return out
}

func formatted(schema map[string]any) string {
	switch f, _ := schema["format"].(string); strings.ToLower(f) {
	case "date-time":
		return "1970-01-01T00:00:00Z"
	case "date":
		return "1970-01-01"
	case "uuid":
		return "00000000-0000-0000-0000-000000000000"
	}
	return ""
}
