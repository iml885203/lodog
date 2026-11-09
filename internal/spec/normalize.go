package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Normalize reads an API description in any supported form and returns the
// document lodog stores and reads: OpenAPI 3.x as JSON. Supported inputs are
// JSON or YAML (told apart by content, not file name) holding Swagger 2.0,
// OpenAPI 3.0.x or OpenAPI 3.1.x. A 3.x JSON document is returned untouched,
// so a document that is already in the stored form never changes. Anything
// else, or a 2.0 construct with no 3.0 equivalent, is an error that names its
// location.
func Normalize(data []byte) ([]byte, error) {
	root, isJSON, err := decodeAny(data)
	if err != nil {
		return nil, err
	}
	doc, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("openapi document must be an object at the top level")
	}
	switch version := declaredVersion(doc); {
	case version == "2.0":
		converted, err := convertSwagger2(doc)
		if err != nil {
			return nil, fmt.Errorf("convert swagger 2.0 to openapi 3.0: %w", err)
		}
		return marshalJSON(converted)
	case strings.HasPrefix(version, "3.0.") || strings.HasPrefix(version, "3.1."):
		if isJSON {
			return data, nil
		}
		return marshalJSON(doc)
	default:
		return nil, versionError(doc)
	}
}

// decodeAny decodes JSON, or YAML when the content is not JSON. JSON is tried
// first because it keeps big integers exact and YAML's looser rules would
// accept a malformed JSON file without complaint.
func decodeAny(data []byte) (root any, isJSON bool, err error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, false, errors.New("openapi document is empty")
	}
	if trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, false, fmt.Errorf("parse openapi document as JSON: %w", err)
		}
		return v, true, nil
	}
	var v any
	if err := yaml.Unmarshal(trimmed, &v); err != nil {
		return nil, false, fmt.Errorf("parse openapi document as YAML: %w", err)
	}
	return stringKeys(v), false, nil
}

// stringKeys makes every map key a string, because YAML allows 200: where JSON
// has "200" and encoding/json refuses any other key type.
func stringKeys(node any) any {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = stringKeys(child)
		}
		return v
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[fmt.Sprint(k)] = stringKeys(child)
		}
		return out
	case []any:
		for i, child := range v {
			v[i] = stringKeys(child)
		}
		return v
	default:
		return v
	}
}

func marshalJSON(doc any) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode openapi document as JSON: %w", err)
	}
	return append(out, '\n'), nil
}

// declaredVersion returns the swagger or openapi field as text, "" when the
// document has neither. YAML turns an unquoted 2.0 into a number, so the value
// is rendered rather than type-asserted.
func declaredVersion(doc map[string]any) string {
	for _, key := range []string{"swagger", "openapi"} {
		switch v := doc[key].(type) {
		case string:
			return v
		case json.Number:
			return v.String()
		case float64:
			return fmt.Sprintf("%.1f", v)
		}
	}
	return ""
}

func versionError(doc map[string]any) error {
	const supported = "supported versions are Swagger 2.0, OpenAPI 3.0.x and OpenAPI 3.1.x"
	if v := declaredVersion(doc); v != "" {
		field := "openapi"
		if _, ok := doc["swagger"]; ok {
			field = "swagger"
		}
		return fmt.Errorf("unsupported %s version %q: %s", field, v, supported)
	}
	return fmt.Errorf("no swagger or openapi version field: %s", supported)
}
