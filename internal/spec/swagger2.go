package spec

import (
	"fmt"
	"sort"
	"strings"
)

// convertSwagger2 turns a Swagger 2.0 document into OpenAPI 3.0.3. Every
// construct it cannot carry over is an error naming where it is; nothing is
// dropped silently except what 3.0 has no place for and lodog does not use
// (operation-level schemes, since the gateway's upstream comes from its own
// config).
func convertSwagger2(doc map[string]any) (map[string]any, error) {
	c := &swagger2{
		consumes: stringList(doc["consumes"]),
		produces: stringList(doc["produces"]),
		params:   asMap(doc["parameters"]),
	}
	out := map[string]any{"openapi": "3.0.3"}
	known := map[string]bool{
		"swagger": true, "host": true, "basePath": true, "schemes": true,
		"consumes": true, "produces": true, "paths": true, "definitions": true,
		"parameters": true, "responses": true, "securityDefinitions": true,
	}
	for k, v := range doc {
		switch {
		case strings.HasPrefix(k, "x-"), k == "info", k == "tags", k == "externalDocs", k == "security":
			out[k] = v
		case !known[k]:
			return nil, fmt.Errorf("%s: unknown top-level field", k)
		}
	}
	if servers := serversOf(doc); servers != nil {
		out["servers"] = servers
	}

	components, err := c.components(doc)
	if err != nil {
		return nil, err
	}
	if len(components) > 0 {
		out["components"] = components
	}

	paths := map[string]any{}
	for _, path := range sortedKeys(asMap(doc["paths"])) {
		item, err := c.pathItem(asMap(asMap(doc["paths"])[path]), "paths."+path)
		if err != nil {
			return nil, err
		}
		paths[path] = item
	}
	out["paths"] = paths
	return out, nil
}

// components carries definitions, document-level responses and
// securityDefinitions over to their OpenAPI 3.0 homes.
func (c *swagger2) components(doc map[string]any) (map[string]any, error) {
	components := map[string]any{}
	if defs := asMap(doc["definitions"]); len(defs) > 0 {
		schemas := map[string]any{}
		for _, name := range sortedKeys(defs) {
			s, err := convertSchema(defs[name], "definitions."+name)
			if err != nil {
				return nil, err
			}
			schemas[name] = s
		}
		components["schemas"] = schemas
	}
	if resps := asMap(doc["responses"]); len(resps) > 0 {
		converted := map[string]any{}
		for _, name := range sortedKeys(resps) {
			r, err := c.response(resps[name], "responses."+name, nil)
			if err != nil {
				return nil, err
			}
			converted[name] = r
		}
		components["responses"] = converted
	}
	if defs := asMap(doc["securityDefinitions"]); len(defs) > 0 {
		schemes := map[string]any{}
		for _, name := range sortedKeys(defs) {
			s, err := securityScheme(defs[name], "securityDefinitions."+name)
			if err != nil {
				return nil, err
			}
			schemes[name] = s
		}
		components["securitySchemes"] = schemes
	}
	return components, nil
}

type swagger2 struct {
	consumes, produces []string
	params             map[string]any // document-level parameters, referenced as #/parameters/<name>
}

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true,
}

func (c *swagger2) pathItem(item map[string]any, loc string) (map[string]any, error) {
	if _, ok := item["$ref"]; ok {
		return nil, fmt.Errorf("%s.$ref: a path item reference is not supported", loc)
	}
	shared, err := c.resolveParams(item["parameters"], loc+".parameters")
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range item {
		switch {
		case k == "parameters":
		case httpMethods[k]:
			op, err := c.operation(asMap(v), shared, loc+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = op
		case strings.HasPrefix(k, "x-"):
			out[k] = v
		default:
			return nil, fmt.Errorf("%s.%s: unknown path item field", loc, k)
		}
	}
	return out, nil
}

// resolveParams expands #/parameters/<name> references so a parameter is
// always the object itself from here on.
func (c *swagger2) resolveParams(node any, loc string) ([]map[string]any, error) {
	list, _ := node.([]any)
	out := make([]map[string]any, 0, len(list))
	for i, raw := range list {
		p := asMap(raw)
		at := fmt.Sprintf("%s[%d]", loc, i)
		if ref, ok := p["$ref"].(string); ok {
			name, found := strings.CutPrefix(ref, "#/parameters/")
			target, exists := c.params[name]
			if !found || !exists {
				return nil, fmt.Errorf("%s.$ref: %q points at no document-level parameter", at, ref)
			}
			p = asMap(target)
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *swagger2) operation(op map[string]any, shared []map[string]any, loc string) (map[string]any, error) {
	own, err := c.resolveParams(op["parameters"], loc+".parameters")
	if err != nil {
		return nil, err
	}
	// An operation's parameter replaces a path-level one of the same name and location.
	merged := append([]map[string]any{}, own...)
	for _, p := range shared {
		if !hasParam(own, p) {
			merged = append(merged, p)
		}
	}
	consumes := orDefault(stringList(op["consumes"]), c.consumes, "application/json")
	produces := orDefault(stringList(op["produces"]), c.produces, "application/json")

	out := map[string]any{}
	for k, v := range op {
		switch k {
		case "parameters", "consumes", "produces", "responses", "schemes":
		default:
			out[k] = v
		}
	}
	params, body, form, err := splitParams(merged, loc)
	if err != nil {
		return nil, err
	}
	if params != nil {
		out["parameters"] = params
	}
	switch {
	case body != nil:
		rb, err := requestBody(body, consumes, loc+".parameters (body)")
		if err != nil {
			return nil, err
		}
		out["requestBody"] = rb
	case len(form) > 0:
		rb, err := formBody(form, consumes, loc)
		if err != nil {
			return nil, err
		}
		out["requestBody"] = rb
	}
	responses := map[string]any{}
	for _, code := range sortedKeys(asMap(op["responses"])) {
		r, err := c.response(asMap(op["responses"])[code], loc+".responses."+code, produces)
		if err != nil {
			return nil, err
		}
		responses[code] = r
	}
	out["responses"] = responses
	return out, nil
}

// splitParams sorts an operation's 2.0 parameters into the 3.0 parameters that
// stay parameters, the single body, and the formData fields.
func splitParams(merged []map[string]any, loc string) (params []any, body map[string]any, form []map[string]any, err error) {
	for i, p := range merged {
		at := fmt.Sprintf("%s.parameters[%d] (%v)", loc, i, p["name"])
		switch p["in"] {
		case "body", "formData":
			if (p["in"] == "body" && (body != nil || len(form) > 0)) || (p["in"] == "formData" && body != nil) {
				return nil, nil, nil, fmt.Errorf("%s: an operation takes one body parameter and cannot mix it with formData", at)
			}
			if p["in"] == "body" {
				body = p
			} else {
				form = append(form, p)
			}
		case "path", "query", "header":
			converted, err := convertParam(p, at)
			if err != nil {
				return nil, nil, nil, err
			}
			params = append(params, converted)
		default:
			return nil, nil, nil, fmt.Errorf("%s: parameter location %q is not defined in Swagger 2.0", at, p["in"])
		}
	}
	return params, body, form, nil
}

func hasParam(list []map[string]any, p map[string]any) bool {
	for _, q := range list {
		if q["name"] == p["name"] && q["in"] == p["in"] {
			return true
		}
	}
	return false
}

// schemaKeys are the validation keywords a non-body parameter or header
// carries inline in 2.0 and 3.0 moves under schema.
var schemaKeys = []string{
	"type", "format", "items", "default", "maximum", "exclusiveMaximum",
	"minimum", "exclusiveMinimum", "maxLength", "minLength", "pattern", "maxItems",
	"minItems", "uniqueItems", "enum", "multipleOf",
}

func inlineSchema(p map[string]any, loc string) (map[string]any, error) {
	s := map[string]any{}
	for _, k := range schemaKeys {
		if v, ok := p[k]; ok {
			s[k] = v
		}
	}
	if s["type"] == "file" {
		return nil, fmt.Errorf("%s: type file is only valid in a formData parameter or a response", loc)
	}
	if items, ok := s["items"]; ok {
		nested, err := inlineSchema(asMap(items), loc+".items")
		if err != nil {
			return nil, err
		}
		s["items"] = nested
	}
	return s, nil
}

func convertParam(p map[string]any, loc string) (map[string]any, error) {
	schema, err := inlineSchema(p, loc)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"name": p["name"], "in": p["in"], "schema": schema}
	for _, k := range []string{"description", "required", "allowEmptyValue", "deprecated"} {
		if v, ok := p[k]; ok {
			out[k] = v
		}
	}
	for k, v := range p {
		if strings.HasPrefix(k, "x-") {
			out[k] = v
		}
	}
	if schema["type"] == "array" {
		format, _ := p["collectionFormat"].(string)
		in, _ := p["in"].(string)
		style, explode, err := arrayStyle(format, in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", loc, err)
		}
		out["style"] = style
		if explode {
			out["explode"] = true
		} else if style == "form" {
			out["explode"] = false
		}
	}
	return out, nil
}

func arrayStyle(format, in string) (style string, explode bool, err error) {
	switch format {
	case "", "csv":
		if in == "query" {
			return "form", false, nil
		}
		return "simple", false, nil
	case "multi":
		if in != "query" {
			return "", false, fmt.Errorf("collectionFormat multi is only valid in a query parameter")
		}
		return "form", true, nil
	case "ssv":
		return "spaceDelimited", false, nil
	case "pipes":
		return "pipeDelimited", false, nil
	default:
		return "", false, fmt.Errorf("collectionFormat %q has no OpenAPI 3.0 style", format)
	}
}

func requestBody(p map[string]any, consumes []string, loc string) (map[string]any, error) {
	schema, err := convertSchema(p["schema"], loc+".schema")
	if err != nil {
		return nil, err
	}
	content := map[string]any{}
	for _, ct := range consumes {
		content[ct] = map[string]any{"schema": schema}
	}
	rb := map[string]any{"content": content}
	if v, ok := p["required"]; ok {
		rb["required"] = v
	}
	if v, ok := p["description"]; ok {
		rb["description"] = v
	}
	return rb, nil
}

// formBody gathers an operation's formData parameters into one object schema.
// A file parameter becomes a binary string and forces multipart.
func formBody(form []map[string]any, consumes []string, loc string) (map[string]any, error) {
	props := map[string]any{}
	var required []any
	hasFile := false
	for i, p := range form {
		at := fmt.Sprintf("%s.parameters[formData %d] (%v)", loc, i, p["name"])
		var s map[string]any
		if p["type"] == "file" {
			hasFile = true
			s = map[string]any{"type": "string", "format": "binary"}
			if d, ok := p["description"]; ok {
				s["description"] = d
			}
		} else {
			var err error
			if s, err = inlineSchema(p, at); err != nil {
				return nil, err
			}
			if d, ok := p["description"]; ok {
				s["description"] = d
			}
		}
		name, _ := p["name"].(string)
		props[name] = s
		if r, _ := p["required"].(bool); r {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if required != nil {
		schema["required"] = required
	}
	var types []string
	for _, ct := range consumes {
		if ct == "multipart/form-data" || ct == "application/x-www-form-urlencoded" {
			types = append(types, ct)
		}
	}
	if types == nil {
		if hasFile {
			types = []string{"multipart/form-data"}
		} else {
			types = []string{"application/x-www-form-urlencoded"}
		}
	}
	content := map[string]any{}
	for _, ct := range types {
		content[ct] = map[string]any{"schema": schema}
	}
	return map[string]any{"content": content}, nil
}

func (c *swagger2) response(node any, loc string, produces []string) (map[string]any, error) {
	r := asMap(node)
	if ref, ok := r["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/responses/")
		if !found {
			return nil, fmt.Errorf("%s.$ref: %q is not a #/responses/ reference", loc, ref)
		}
		return map[string]any{"$ref": "#/components/responses/" + name}, nil
	}
	if produces == nil {
		produces = orDefault(nil, c.produces, "application/json")
	}
	out := map[string]any{"description": r["description"]}
	if out["description"] == nil {
		out["description"] = ""
	}
	if rs, ok := r["schema"]; ok {
		schema, err := convertSchema(rs, loc+".schema")
		if err != nil {
			return nil, err
		}
		content := map[string]any{}
		for _, ct := range produces {
			content[ct] = map[string]any{"schema": schema}
		}
		for ct, ex := range asMap(r["examples"]) {
			entry, ok := content[ct].(map[string]any)
			if !ok {
				entry = map[string]any{"schema": schema}
				content[ct] = entry
			}
			entry["example"] = ex
		}
		out["content"] = content
	}
	if hs := asMap(r["headers"]); len(hs) > 0 {
		headers := map[string]any{}
		for _, name := range sortedKeys(hs) {
			h := asMap(hs[name])
			s, err := inlineSchema(h, loc+".headers."+name)
			if err != nil {
				return nil, err
			}
			headers[name] = map[string]any{"description": h["description"], "schema": s}
		}
		out["headers"] = headers
	}
	return out, nil
}

// convertSchema rewrites the references and the few schema keywords whose
// shape changed between 2.0 and 3.0; every other keyword carries over as is.
func convertSchema(node any, loc string) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			switch k {
			case "$ref":
				ref, ok := child.(string)
				if !ok {
					out[k] = child
					continue
				}
				rewritten, err := rewriteRef(ref, loc)
				if err != nil {
					return nil, err
				}
				out[k] = rewritten
			case "x-nullable":
				if b, ok := child.(bool); ok {
					out["nullable"] = b
				} else {
					out[k] = child
				}
			case "discriminator":
				if name, ok := child.(string); ok {
					out[k] = map[string]any{"propertyName": name}
				} else {
					out[k] = child
				}
			default:
				converted, err := convertSchema(child, loc+"."+k)
				if err != nil {
					return nil, err
				}
				out[k] = converted
			}
		}
		if out["type"] == "file" {
			return nil, fmt.Errorf("%s: type file is only valid in a formData parameter or a response", loc)
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			converted, err := convertSchema(child, fmt.Sprintf("%s[%d]", loc, i))
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	default:
		return v, nil
	}
}

func rewriteRef(ref, loc string) (string, error) {
	if name, ok := strings.CutPrefix(ref, "#/definitions/"); ok {
		return "#/components/schemas/" + name, nil
	}
	return "", fmt.Errorf("%s.$ref: %q is not a #/definitions/ reference (external and non-definition references are not supported)", loc, ref)
}

func securityScheme(node any, loc string) (map[string]any, error) {
	s := asMap(node)
	out := map[string]any{}
	if d, ok := s["description"]; ok {
		out["description"] = d
	}
	switch s["type"] {
	case "basic":
		out["type"], out["scheme"] = "http", "basic"
	case "apiKey":
		out["type"], out["name"], out["in"] = "apiKey", s["name"], s["in"]
	case "oauth2":
		scopes := s["scopes"]
		if scopes == nil {
			scopes = map[string]any{}
		}
		flow := map[string]any{"scopes": scopes}
		var kind string
		switch s["flow"] {
		case "implicit":
			kind = "implicit"
			flow["authorizationUrl"] = s["authorizationUrl"]
		case "password":
			kind = "password"
			flow["tokenUrl"] = s["tokenUrl"]
		case "application":
			kind = "clientCredentials"
			flow["tokenUrl"] = s["tokenUrl"]
		case "accessCode":
			kind = "authorizationCode"
			flow["authorizationUrl"], flow["tokenUrl"] = s["authorizationUrl"], s["tokenUrl"]
		default:
			return nil, fmt.Errorf("%s.flow: oauth2 flow %q is not defined in Swagger 2.0", loc, s["flow"])
		}
		out["type"], out["flows"] = "oauth2", map[string]any{kind: flow}
	default:
		return nil, fmt.Errorf("%s.type: security type %q is not defined in Swagger 2.0", loc, s["type"])
	}
	return out, nil
}

// serversOf builds servers from host, basePath and schemes. Without a host the
// document gives only a path, which 3.0 accepts as a relative server URL.
func serversOf(doc map[string]any) []any {
	host, _ := doc["host"].(string)
	base, _ := doc["basePath"].(string)
	if host == "" && base == "" {
		return nil
	}
	if host == "" {
		return []any{map[string]any{"url": base}}
	}
	schemes := stringList(doc["schemes"])
	if len(schemes) == 0 {
		return []any{map[string]any{"url": "//" + host + base}}
	}
	out := make([]any, 0, len(schemes))
	for _, s := range schemes {
		out = append(out, map[string]any{"url": s + "://" + host + base})
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func orDefault(op, global []string, fallback string) []string {
	switch {
	case len(op) > 0:
		return op
	case len(global) > 0:
		return global
	default:
		return []string{fallback}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
