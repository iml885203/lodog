// Package overlay reads and applies OpenAPI Overlay 1.0 documents: the L1
// layer that adds what a legacy OpenAPI document leaves out (what an
// operation is for, what a parameter means) without editing the document.
//
// Targets support the subset lodog writes:
//
//	$.paths['/api/x'].get                                   an operation
//	$.paths['/api/x'].get.parameters[?(@.name=='PageSize')] one of its parameters
//	$.paths['/api/x'].get.parameters[?(@.name=='id' && @.in=='header')] the one in a place, when a name is used in two
//
// A parameter is looked up among the operation's own, then its path item's;
// one declared by $ref is the shared component it points at.
//
// A target that matches nothing is an error, never skipped: an overlay that
// silently stops applying after the API changes would serve stale help.
package overlay

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Overlay is one OpenAPI Overlay 1.0 document.
type Overlay struct {
	Overlay string   `yaml:"overlay"`
	Info    Info     `yaml:"info"`
	Actions []Action `yaml:"actions"`
}

// Info names the overlay.
type Info struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

// Action merges Update into every node Target selects.
type Action struct {
	Target      string         `yaml:"target"`
	Description string         `yaml:"description,omitempty"`
	Update      map[string]any `yaml:"update"`
}

// LoadFile reads an overlay.
func LoadFile(path string) (*Overlay, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own overlay
	if err != nil {
		return nil, fmt.Errorf("read overlay: %w", err)
	}
	var o Overlay
	if err := yaml.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse overlay: %w", err)
	}
	if !strings.HasPrefix(o.Overlay, "1.") {
		return nil, fmt.Errorf("overlay %s: version %q is not 1.x", path, o.Overlay)
	}
	return &o, nil
}

// Save writes the overlay as YAML.
func (o *Overlay) Save(path string) error {
	data, err := yaml.Marshal(o)
	if err != nil {
		return fmt.Errorf("encode overlay: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}
	return nil
}

var (
	operationTarget = regexp.MustCompile(`^\$\.paths\['([^']+)'\]\.(get|put|post|delete|patch)$`)
	paramTarget     = regexp.MustCompile(`^\$\.paths\['([^']+)'\]\.(get|put|post|delete|patch)\.parameters\[\?\(@\.name=='([^']+)'( && @\.in=='([a-z]+)')?\)\]$`)
)

// OperationTarget is the target for one operation.
func OperationTarget(method, path string) string {
	return fmt.Sprintf("$.paths['%s'].%s", path, strings.ToLower(method))
}

// ParseOperationTarget is the method and path of an operation target, the
// inverse of OperationTarget; ok is false for any other target.
func ParseOperationTarget(target string) (method, path string, ok bool) {
	m := operationTarget.FindStringSubmatch(target)
	if m == nil {
		return "", "", false
	}
	return strings.ToUpper(m[2]), m[1], true
}

// ParamTargetIn is the target for the parameter called name in one place
// ("path", "query", "header" or "cookie"), for an operation that has a
// same-named parameter elsewhere.
func ParamTargetIn(method, path, name, in string) string {
	return OperationTarget(method, path) + fmt.Sprintf(".parameters[?(@.name=='%s' && @.in=='%s')]", name, in)
}

// ParamTarget is the target for one parameter of an operation.
func ParamTarget(method, path, name string) string {
	return OperationTarget(method, path) + fmt.Sprintf(".parameters[?(@.name=='%s')]", name)
}

// Apply merges every action into doc, a decoded OpenAPI document. It applies
// all it can and reports every target that matched nothing.
func (o *Overlay) Apply(doc map[string]any) error {
	var errs []error
	for i, a := range o.Actions {
		node, err := find(doc, a.Target)
		if err != nil {
			errs = append(errs, fmt.Errorf("actions[%d] %s: %w", i, a.Target, err))
			continue
		}
		merge(node, a.Update)
	}
	return errors.Join(errs...)
}

func find(doc map[string]any, target string) (map[string]any, error) {
	if m := paramTarget.FindStringSubmatch(target); m != nil {
		op, err := operation(doc, m[1], m[2])
		if err != nil {
			return nil, err
		}
		if pm, ok := findParam(doc, m[1], op, m[3], m[5]); ok {
			return pm, nil
		}
		return nil, fmt.Errorf("no parameter %q", m[3])
	}
	if m := operationTarget.FindStringSubmatch(target); m != nil {
		return operation(doc, m[1], m[2])
	}
	return nil, errors.New("unsupported target: use $.paths['/p'].method or its .parameters[?(@.name=='n')]")
}

// findParam looks for the parameter name (in a given place, when in is not
// empty) among the operation's parameters, then its path item's.
func findParam(doc map[string]any, path string, op map[string]any, name, in string) (map[string]any, bool) {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	for _, list := range []any{op["parameters"], item["parameters"]} {
		params, _ := list.([]any)
		for _, p := range params {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if ref, isRef := pm["$ref"].(string); isRef {
				pm = component(doc, ref)
			}
			if pm != nil && pm["name"] == name && (in == "" || pm["in"] == in) {
				return pm, true
			}
		}
	}
	return nil, false
}

func component(doc map[string]any, ref string) map[string]any {
	name, ok := strings.CutPrefix(ref, "#/components/parameters/")
	if !ok {
		return nil
	}
	components, _ := doc["components"].(map[string]any)
	params, _ := components["parameters"].(map[string]any)
	pm, _ := params[name].(map[string]any)
	return pm
}

func operation(doc map[string]any, path, method string) (map[string]any, error) {
	paths, _ := doc["paths"].(map[string]any)
	item, ok := paths[path].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no path %q", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no %s on %q", method, path)
	}
	return op, nil
}

// merge copies update into node: objects merge recursively, anything else
// replaces what is there.
func merge(node, update map[string]any) {
	for k, v := range update {
		if vm, ok := v.(map[string]any); ok {
			if existing, ok := node[k].(map[string]any); ok {
				merge(existing, vm)
				continue
			}
		}
		node[k] = v
	}
}
