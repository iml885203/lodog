// Package spec reads a legacy system's OpenAPI document into the operations a
// gateway exposes. It knows the document format and nothing about MCP or HTTP
// transport: the gateway decides what a tool is, this package only says what
// the legacy API offers.
package spec

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"sort"
	"strings"
)

// Operation is one method on one path of the legacy API, with every schema it
// references already inlined so a caller never has to resolve a $ref.
type Operation struct {
	Method      string // upper case: GET, POST, ...
	Path        string // as written in the document, e.g. /api/customer/{customerId}
	OperationID string // empty when the document gives none (Backoffice gives none)
	Tag         string
	Summary     string
	Description string
	// Params are the operation's path, query, header and cookie parameters,
	// with path-level ones merged in and parameter references resolved.
	Params []Param
	// Body is the request body schema (JSON, urlencoded or multipart form),
	// or nil when the operation takes none. BodyContentType records what the
	// document declared; it is set without a Body when the only types declared
	// are ones lodog does not read.
	Body            map[string]any
	BodyRequired    bool
	BodyContentType string
	// Responses are the status codes the document declares, e.g. "200", "404".
	Responses []string
	// Result is the JSON schema of the lowest 2xx response, inlined; nil when
	// the document declares none. Task tools read values out of it.
	Result map[string]any
	// Secured is true when the operation, or the document as a whole, names a
	// security requirement.
	Secured bool
	// AgentRisk is the operation's x-agent.risk, empty when undeclared.
	AgentRisk string
	// NeedsReview is x-agent.needsReview: the risk and hints were guessed by a
	// model that was unsure and no person has confirmed them yet.
	NeedsReview bool
	// WhenToUse and WhenNotToUse are x-agent's routing hints: the situations
	// an agent should and should not pick this operation for.
	WhenToUse    string
	WhenNotToUse string
}

// Param is a path, query, header or cookie parameter.
type Param struct {
	Name string
	// Arg is the tool argument that carries the parameter. It is Name unless
	// another parameter of the operation has the same name in another place,
	// then "<in>.<name>" for each of them. Read it through ArgName.
	Arg         string
	In          string // "path", "query", "header" or "cookie"
	Required    bool
	Description string
	Schema      map[string]any
}

// ArgName is the name of the tool argument for p: Name unless it clashes with
// a same-named parameter in another place (see Arg).
func (p Param) ArgName() string {
	if p.Arg != "" {
		return p.Arg
	}
	return p.Name
}

// NameArgs gives each parameter its tool argument name. Arguments are keyed by
// name alone, so a name used in two places is prefixed with its place in both;
// every other parameter keeps its plain name. Call it again after removing
// parameters: a clash that is gone no longer needs its prefix.
func NameArgs(params []Param) {
	places := map[string]map[string]bool{}
	for _, p := range params {
		if places[p.Name] == nil {
			places[p.Name] = map[string]bool{}
		}
		places[p.Name][p.In] = true
	}
	for i, p := range params {
		params[i].Arg = p.Name
		if len(places[p.Name]) > 1 {
			params[i].Arg = p.In + "." + p.Name
		}
	}
}

// Document is the subset of an OpenAPI 3.x document lodog reads.
type Document struct {
	Title      string
	Operations []Operation
	// SecuritySchemes names the document's components.securitySchemes.
	SecuritySchemes []string
}

type rawDocument struct {
	Info struct {
		Title string `json:"title"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Security   []map[string]any                      `json:"security"`
	Components struct {
		Schemas         map[string]any      `json:"schemas"`
		Parameters      map[string]rawParam `json:"parameters"`
		SecuritySchemes map[string]any      `json:"securitySchemes"`
	} `json:"components"`
}

type rawOperation struct {
	OperationID string                     `json:"operationId"`
	Tags        []string                   `json:"tags"`
	Summary     string                     `json:"summary"`
	Description string                     `json:"description"`
	Parameters  []rawParam                 `json:"parameters"`
	RequestBody *rawRequestBody            `json:"requestBody"`
	Responses   map[string]json.RawMessage `json:"responses"`
	Security    *[]map[string]any          `json:"security"`
	XAgent      struct {
		Risk         string `json:"risk"`
		NeedsReview  bool   `json:"needsReview"`
		WhenToUse    string `json:"whenToUse"`
		WhenNotToUse string `json:"whenNotToUse"`
	} `json:"x-agent"`
}

type rawRequestBody struct {
	Required bool `json:"required"`
	Content  map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"content"`
}

type rawParam struct {
	Ref         string         `json:"$ref"`
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

var methods = []string{"get", "post", "put", "patch", "delete"}

// LoadFile reads an API description from disk in any form Normalize accepts.
func LoadFile(path string) (*Document, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own config, not request input
	if err != nil {
		return nil, fmt.Errorf("read openapi document: %w", err)
	}
	return Parse(data)
}

// Parse reads an API description in any form Normalize accepts. Operations
// come back sorted by path then method, so tool lists are stable across runs.
func Parse(data []byte) (*Document, error) {
	data, err := Normalize(data)
	if err != nil {
		return nil, fmt.Errorf("read openapi document: %w", err)
	}
	var raw rawDocument
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse openapi document: %w", err)
	}
	res := resolver{schemas: raw.Components.Schemas}
	doc := &Document{Title: raw.Info.Title}
	for name := range raw.Components.SecuritySchemes {
		doc.SecuritySchemes = append(doc.SecuritySchemes, name)
	}
	sort.Strings(doc.SecuritySchemes)
	for path, item := range raw.Paths {
		var shared []rawParam
		if rawShared, ok := item["parameters"]; ok {
			if err := json.Unmarshal(rawShared, &shared); err != nil {
				return nil, fmt.Errorf("parse %s parameters: %w", path, err)
			}
		}
		for _, m := range methods {
			body, ok := item[m]
			if !ok {
				continue
			}
			var ro rawOperation
			if err := json.Unmarshal(body, &ro); err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", strings.ToUpper(m), path, err)
			}
			ro.Parameters = mergeParams(ro.Parameters, shared)
			op, err := buildOperation(strings.ToUpper(m), path, ro, res, raw.Components.Parameters)
			if err != nil {
				return nil, err
			}
			// An operation's own security list replaces the document's; an
			// empty list there explicitly opts the operation out.
			if ro.Security != nil {
				op.Secured = len(*ro.Security) > 0
			} else {
				op.Secured = len(raw.Security) > 0
			}
			doc.Operations = append(doc.Operations, op)
		}
	}
	sort.Slice(doc.Operations, func(i, j int) bool {
		a, b := doc.Operations[i], doc.Operations[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	return doc, nil
}

// mergeParams puts the path-level parameters after the operation's own; an
// operation's parameter replaces a path-level one with the same name and place.
func mergeParams(own, shared []rawParam) []rawParam {
	out := append([]rawParam{}, own...)
	for _, sp := range shared {
		replaced := false
		for _, op := range own {
			if op.Ref != "" || sp.Ref != "" {
				replaced = replaced || (op.Ref != "" && op.Ref == sp.Ref)
			} else if op.Name == sp.Name && op.In == sp.In {
				replaced = true
			}
		}
		if !replaced {
			out = append(out, sp)
		}
	}
	return out
}

const paramRefPrefix = "#/components/parameters/"

func resolveParam(p rawParam, known map[string]rawParam) (rawParam, error) {
	if p.Ref == "" {
		return p, nil
	}
	name, ok := strings.CutPrefix(p.Ref, paramRefPrefix)
	if !ok {
		return rawParam{}, fmt.Errorf("parameter reference %q is outside %s", p.Ref, paramRefPrefix)
	}
	target, ok := known[name]
	if !ok {
		return rawParam{}, fmt.Errorf("parameter reference %q points at no parameter", p.Ref)
	}
	return target, nil
}

// openAPIIgnoredHeaders are the header parameters the OpenAPI 3.0 spec says to
// ignore: the transport sets them.
var openAPIIgnoredHeaders = map[string]bool{"accept": true, "content-type": true, "authorization": true}

// readBody sets op's body from the request body the document declares.
func readBody(op *Operation, rb *rawRequestBody, res resolver) error {
	if rb == nil {
		return nil
	}
	types := make([]string, 0, len(rb.Content))
	for ct := range rb.Content {
		types = append(types, ct)
	}
	sort.Strings(types)
	if readable := bodyPreference(types); len(readable) > 0 {
		ct := readable[0]
		schema, err := res.inline(rb.Content[ct].Schema)
		if err != nil {
			return fmt.Errorf("%s %s: request body: %w", op.Method, op.Path, err)
		}
		op.Body, op.BodyRequired, op.BodyContentType = schema, rb.Required, ct
		return nil
	}
	// A body in a form lodog does not read (XML, plain text, binary) has no
	// schema here, but the operation still takes one: record its type so
	// the gateway can refuse to serve it as if it took none.
	if len(types) > 0 {
		op.BodyContentType, op.BodyRequired = types[0], rb.Required
	}
	return nil
}

// IsJSON reports whether a declared media type is JSON: application/json, or
// any type with the +json structured syntax suffix (RFC 6838), such as
// application/vnd.api+json or application/problem+json; parameters like
// charset are ignored.
func IsJSON(contentType string) bool {
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

// bodyPreference lists the declared types lodog reads, best first: plain
// application/json, other JSON types, then the form types, urlencoded before
// multipart. types is sorted.
func bodyPreference(types []string) []string {
	var plain, other, urlencoded, multipart []string
	for _, ct := range types {
		base, _, _ := mime.ParseMediaType(ct)
		switch {
		case base == "application/json":
			plain = append(plain, ct)
		case IsJSON(ct):
			other = append(other, ct)
		case base == "application/x-www-form-urlencoded":
			urlencoded = append(urlencoded, ct)
		case base == "multipart/form-data":
			multipart = append(multipart, ct)
		}
	}
	return append(append(append(plain, other...), urlencoded...), multipart...)
}

func buildOperation(method, path string, ro rawOperation, res resolver, known map[string]rawParam) (Operation, error) {
	op := Operation{
		Method:      method,
		Path:        path,
		OperationID: ro.OperationID,
		Summary:     ro.Summary,
		Description: ro.Description,
		AgentRisk:   ro.XAgent.Risk,
		NeedsReview: ro.XAgent.NeedsReview,

		WhenToUse:    ro.XAgent.WhenToUse,
		WhenNotToUse: ro.XAgent.WhenNotToUse,
	}
	for code := range ro.Responses {
		op.Responses = append(op.Responses, code)
	}
	sort.Strings(op.Responses)
	result, err := resultSchema(op.Responses, ro.Responses, res)
	if err != nil {
		return Operation{}, fmt.Errorf("%s %s: %w", method, path, err)
	}
	op.Result = result
	if len(ro.Tags) > 0 {
		op.Tag = ro.Tags[0]
	}
	for _, declared := range ro.Parameters {
		p, err := resolveParam(declared, known)
		if err != nil {
			return Operation{}, fmt.Errorf("%s %s: %w", method, path, err)
		}
		switch p.In {
		case "path", "query", "cookie":
		case "header":
			if openAPIIgnoredHeaders[strings.ToLower(p.Name)] {
				continue
			}
		default:
			return Operation{}, fmt.Errorf("%s %s: parameter %q in %q is not supported", method, path, p.Name, p.In)
		}
		schema, err := res.inline(p.Schema)
		if err != nil {
			return Operation{}, fmt.Errorf("%s %s: parameter %q: %w", method, path, p.Name, err)
		}
		op.Params = append(op.Params, Param{
			Name:        p.Name,
			In:          p.In,
			Required:    p.Required || p.In == "path",
			Description: p.Description,
			Schema:      schema,
		})
	}
	NameArgs(op.Params)
	if err := readBody(&op, ro.RequestBody, res); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// resultSchema is the JSON schema of the lowest 2xx response. codes is sorted.
func resultSchema(codes []string, responses map[string]json.RawMessage, res resolver) (map[string]any, error) {
	for _, code := range codes {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		var r struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		}
		if err := json.Unmarshal(responses[code], &r); err != nil {
			return nil, fmt.Errorf("response %s: %w", code, err)
		}
		content, ok := r.Content["application/json"]
		if !ok {
			continue
		}
		schema, err := res.inline(content.Schema)
		if err != nil {
			return nil, fmt.Errorf("response %s: %w", code, err)
		}
		return schema, nil
	}
	return nil, nil
}

// resolver inlines #/components/schemas references. A schema that refers back
// to itself is cut at the second visit and replaced by a plain object, because
// an agent cannot use an infinitely deep schema and a tool list must be finite.
type resolver struct {
	schemas map[string]any
}

const refPrefix = "#/components/schemas/"

func (r resolver) inline(schema map[string]any) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}
	out, err := r.walk(schema, map[string]bool{})
	if err != nil {
		return nil, err
	}
	describeEnums(out)
	m, _ := out.(map[string]any)
	return m, nil
}

func (r resolver) walk(node any, visiting map[string]bool) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			return r.follow(ref, visiting)
		}
		out := make(map[string]any, len(v))
		for k, child := range v {
			resolved, err := r.walk(child, visiting)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			resolved, err := r.walk(child, visiting)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}

func (r resolver) follow(ref string, visiting map[string]bool) (any, error) {
	name, ok := strings.CutPrefix(ref, refPrefix)
	if !ok {
		return nil, fmt.Errorf("reference %q is outside %s", ref, refPrefix)
	}
	target, ok := r.schemas[name]
	if !ok {
		return nil, fmt.Errorf("reference %q points at no schema", ref)
	}
	if visiting[name] {
		return map[string]any{"type": "object", "description": "recursive " + name + " (cut)"}, nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	return r.walk(target, visiting)
}
