package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// Task is a workflow bound to the operations its steps call: one task tool.
type Task struct {
	Workflow Workflow
	// Ops holds each step's operation, in step order.
	Ops []spec.Operation
}

// toolName is what a client accepts as a tool name (Claude: 64 characters).
var toolName = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

// pageSizeParam names an operation parameter that sets how many rows a list
// returns. A task tool that lets the agent set it must cap it.
var pageSizeParam = regexp.MustCompile(`(?i)^(pagesize|limit|size|perpage|per_page|count)$`)

// Bind checks the document against the OpenAPI document its steps call and
// returns one task per workflow. Every problem is reported at once, so a
// drafted file is fixed in one pass rather than one error at a time.
func Bind(d *Document, doc *spec.Document) ([]Task, error) {
	var errs []error
	if !strings.HasPrefix(d.Arazzo, "1.0.") {
		errs = append(errs, fmt.Errorf("arazzo %q: lodog reads Arazzo 1.0.x", d.Arazzo))
	}
	if strings.TrimSpace(d.Info.Description) == "" {
		errs = append(errs, errors.New("info.description is required: it is the agent's map of the tools (server instructions)"))
	}
	source, err := oneSource(d.SourceDescriptions)
	if err != nil {
		errs = append(errs, err)
	}
	ids := map[string]bool{}
	for _, w := range d.Workflows {
		if ids[w.WorkflowID] {
			errs = append(errs, fmt.Errorf("workflow %q is defined twice", w.WorkflowID))
		}
		ids[w.WorkflowID] = true
	}
	if len(d.Workflows) == 0 {
		errs = append(errs, errors.New("no workflows"))
	}
	tasks := make([]Task, 0, len(d.Workflows))
	for _, w := range d.Workflows {
		t, err := bindWorkflow(w, doc, source, ids)
		if err != nil {
			errs = append(errs, fmt.Errorf("workflow %q: %w", w.WorkflowID, err))
			continue
		}
		tasks = append(tasks, t)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return tasks, nil
}

func oneSource(sources []Source) (string, error) {
	if len(sources) != 1 {
		return "", fmt.Errorf("sourceDescriptions: want exactly one OpenAPI source, got %d", len(sources))
	}
	s := sources[0]
	if s.Type != "" && s.Type != "openapi" {
		return "", fmt.Errorf("sourceDescriptions[0]: type %q, want openapi", s.Type)
	}
	if !validName(s.Name) {
		return "", fmt.Errorf("sourceDescriptions[0]: name %q", s.Name)
	}
	return s.Name, nil
}

func bindWorkflow(w Workflow, doc *spec.Document, source string, ids map[string]bool) (Task, error) {
	errs := checkHeader(w, ids)
	props, err := checkInputs(w.Inputs)
	if err != nil {
		errs = append(errs, err)
	}
	if len(w.Steps) == 0 {
		errs = append(errs, errors.New("no steps"))
	}
	b := binder{props: props, outputs: map[string]map[string]bool{}, used: map[string]bool{}}
	t := Task{Workflow: w}
	for i, s := range w.Steps {
		op, err := b.step(s, doc, source)
		if err != nil {
			errs = append(errs, fmt.Errorf("step %d %q: %w", i+1, s.StepID, err))
		}
		t.Ops = append(t.Ops, op)
	}
	t.Workflow.Inputs = withOperationRequired(withOperationEnums(w.Inputs, w.Steps, t.Ops), w.Steps, t.Ops)
	for name, value := range w.Outputs {
		if err := b.workflowOutput(value); err != nil {
			errs = append(errs, fmt.Errorf("output %q: %w", name, err))
		}
	}
	for _, name := range sortedKeys(props) {
		if !b.used[name] {
			errs = append(errs, fmt.Errorf("input %q is never used by a step", name))
		}
	}
	return t, errors.Join(errs...)
}

// checkHeader checks what a workflow says about itself: its name, summary,
// the features lodog does not run, and the tools it names as next.
func checkHeader(w Workflow, ids map[string]bool) []error {
	var errs []error
	if !toolName.MatchString(w.WorkflowID) {
		errs = append(errs, errors.New("workflowId must be 1-64 letters, digits or underscores: it is the tool name"))
	}
	if strings.TrimSpace(w.Summary) == "" {
		errs = append(errs, errors.New("summary is required: it is the first line the agent reads"))
	}
	if w.SuccessActions != nil || w.FailureActions != nil || w.DependsOn != nil || w.Parameters != nil {
		errs = append(errs, errors.New("successActions, failureActions, dependsOn and workflow parameters are not supported"))
	}
	for _, n := range w.Agent.Next {
		if !ids[n] {
			errs = append(errs, fmt.Errorf("x-agent.next names %q, which is not a workflow here", n))
		}
	}
	return errs
}

// checkInputs validates the inputs schema and returns its properties.
func checkInputs(inputs map[string]any) (map[string]map[string]any, error) {
	props := map[string]map[string]any{}
	if inputs == nil {
		return props, nil
	}
	if inputs["type"] != "object" {
		return nil, errors.New(`inputs must be a JSON schema with type: object`)
	}
	raw, _ := inputs["properties"].(map[string]any)
	var errs []error
	for name, v := range raw {
		p, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("input %q is not a schema", name))
			continue
		}
		if !validName(name) {
			errs = append(errs, fmt.Errorf("input name %q", name))
		}
		if _, ok := p["type"].(string); !ok {
			errs = append(errs, fmt.Errorf("input %q needs a type", name))
		}
		if rel, ok := p[relativeDefault]; ok {
			if _, err := parseRelative(fmt.Sprint(rel)); err != nil {
				errs = append(errs, fmt.Errorf("input %q: %w", name, err))
			}
		}
		props[name] = p
	}
	for _, r := range requiredInputs(inputs) {
		if _, ok := props[r]; !ok {
			errs = append(errs, fmt.Errorf("required input %q is not a property", r))
		}
	}
	return props, errors.Join(errs...)
}

func requiredInputs(inputs map[string]any) []string {
	var out []string
	switch list := inputs["required"].(type) {
	case []any:
		for _, r := range list {
			out = append(out, fmt.Sprint(r))
		}
	case []string:
		out = list
	}
	return out
}

// binder tracks what a workflow's steps make available to later steps.
type binder struct {
	props   map[string]map[string]any
	outputs map[string]map[string]bool // step id -> output names
	used    map[string]bool            // inputs some step reads
}

func (b *binder) step(s Step, doc *spec.Document, source string) (spec.Operation, error) {
	if err := b.stepHeader(s); err != nil {
		return spec.Operation{}, err
	}
	op, err := resolveOperation(s, doc, source)
	if err != nil {
		return spec.Operation{}, err
	}
	var errs []error
	given := map[string]bool{}
	for _, p := range s.Parameters {
		if err := b.parameter(p, op, given); err != nil {
			errs = append(errs, fmt.Errorf("parameter %q: %w", p.Name, err))
		}
	}
	for _, p := range op.Params {
		if p.Required && !given[p.ArgName()] {
			errs = append(errs, fmt.Errorf("%s %s requires parameter %q and the step does not set it", op.Method, op.Path, p.Name))
		}
	}
	if err := b.body(s.RequestBody, op); err != nil {
		errs = append(errs, err)
	}
	names := map[string]bool{}
	for name, value := range s.Outputs {
		if err := stepOutput(value, op); err != nil {
			errs = append(errs, fmt.Errorf("output %q: %w", name, err))
		}
		names[name] = true
	}
	b.outputs[s.StepID] = names
	return op, errors.Join(errs...)
}

// stepHeader refuses a step lodog cannot run as written.
func (b *binder) stepHeader(s Step) error {
	if !validName(s.StepID) {
		return errors.New("stepId must be letters, digits, - or _")
	}
	if _, dup := b.outputs[s.StepID]; dup {
		return errors.New("stepId is used twice")
	}
	if s.WorkflowID != "" || s.SuccessCriteria != nil || s.OnSuccess != nil || s.OnFailure != nil {
		return errors.New("workflowId, successCriteria, onSuccess and onFailure on a step are not supported")
	}
	return nil
}

// resolveOperation finds the operation a step names, by operationId or by an
// operationPath of the form {$sourceDescriptions.<name>.url}#/paths/<path>/<method>.
func resolveOperation(s Step, doc *spec.Document, source string) (spec.Operation, error) {
	if (s.OperationID == "") == (s.OperationPath == "") {
		return spec.Operation{}, errors.New("give exactly one of operationId and operationPath")
	}
	if s.OperationID != "" {
		for _, op := range doc.Operations {
			if op.OperationID == s.OperationID {
				return op, nil
			}
		}
		return spec.Operation{}, fmt.Errorf("no operation has operationId %q", s.OperationID)
	}
	prefix := "{$sourceDescriptions." + source + ".url}#"
	ptr, ok := strings.CutPrefix(s.OperationPath, prefix)
	if !ok {
		return spec.Operation{}, fmt.Errorf("operationPath %q must start with %s", s.OperationPath, prefix)
	}
	segs, err := parsePointer(ptr)
	if err != nil || len(segs) != 3 || segs[0] != "paths" {
		return spec.Operation{}, fmt.Errorf("operationPath %q must point at #/paths/<path>/<method>", s.OperationPath)
	}
	method := strings.ToUpper(segs[2])
	for _, op := range doc.Operations {
		if op.Path == segs[1] && op.Method == method {
			return op, nil
		}
	}
	return spec.Operation{}, fmt.Errorf("the OpenAPI document has no %s %s", method, segs[1])
}

// OperationPath is the operationPath that names op in source.
func OperationPath(source string, op spec.Operation) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(op.Path, "~", "~0"), "/", "~1")
	return "{$sourceDescriptions." + source + ".url}#/paths/" + escaped + "/" + strings.ToLower(op.Method)
}

func (b *binder) parameter(p Parameter, op spec.Operation, given map[string]bool) error {
	if p.Ref != "" {
		return errors.New("reusable parameter references are not supported")
	}
	declared, err := matchParam(p, op)
	if err != nil {
		return err
	}
	if given[declared.ArgName()] {
		return errors.New("set twice")
	}
	given[declared.ArgName()] = true
	if err := b.value(p.Value); err != nil {
		return err
	}
	if s, ok := p.Value.(string); ok && pageSizeParam.MatchString(p.Name) {
		if e, isExpr, _ := parseExpr(s); isExpr && e.kind == exprInput {
			if _, capped := b.props[e.name]["maximum"]; !capped {
				return fmt.Errorf("input %q sets the page size and needs a maximum", e.name)
			}
		}
	}
	return nil
}

// matchParam finds the operation parameter a step parameter sets. in is
// needed only when the name is used in two places.
func matchParam(p Parameter, op spec.Operation) (spec.Param, error) {
	var found []spec.Param
	for _, declared := range op.Params {
		if declared.Name == p.Name && (p.In == "" || p.In == declared.In) {
			found = append(found, declared)
		}
	}
	switch len(found) {
	case 0:
		return spec.Param{}, fmt.Errorf("%s %s has no such parameter", op.Method, op.Path)
	case 1:
		return found[0], nil
	}
	return spec.Param{}, fmt.Errorf("%s %s uses the name in more than one place; say which with in", op.Method, op.Path)
}

func (b *binder) body(rb *RequestBody, op spec.Operation) error {
	if rb == nil {
		if op.BodyRequired {
			return fmt.Errorf("%s %s requires a request body and the step sends none", op.Method, op.Path)
		}
		return nil
	}
	if op.Body == nil {
		return fmt.Errorf("%s %s takes no request body", op.Method, op.Path)
	}
	if rb.ContentType != "" && rb.ContentType != "application/json" {
		return fmt.Errorf("requestBody: contentType %q; lodog sends application/json", rb.ContentType)
	}
	if rb.Replacements != nil {
		return errors.New("requestBody: replacements are not supported; put expressions in the payload")
	}
	if payload, ok := rb.Payload.(map[string]any); ok {
		if props, ok := op.Body["properties"].(map[string]any); ok {
			for key := range payload {
				if _, ok := props[key]; !ok {
					return fmt.Errorf("requestBody: %s %s has no body field %q", op.Method, op.Path, key)
				}
			}
		}
	}
	if err := b.value(rb.Payload); err != nil {
		return fmt.Errorf("requestBody: %w", err)
	}
	return nil
}

// value checks every expression in a literal value: an input must be
// declared, a step output must come from an earlier step.
func (b *binder) value(v any) error {
	return walkValues(v, func(s string) error {
		e, isExpr, err := parseExpr(s)
		if err != nil || !isExpr {
			return err
		}
		switch e.kind {
		case exprInput:
			if _, ok := b.props[e.name]; !ok {
				return fmt.Errorf("%s: no input %q", s, e.name)
			}
			b.used[e.name] = true
		case exprStepOutput:
			if !b.outputs[e.step][e.name] {
				return fmt.Errorf("%s: no earlier step %q with output %q", s, e.step, e.name)
			}
		case exprBody, exprStatus:
			return fmt.Errorf("%s: a value sent to an operation can only be $inputs or $steps", s)
		}
		return nil
	})
}

func stepOutput(value string, op spec.Operation) error {
	e, isExpr, err := parseExpr(value)
	if err != nil {
		return err
	}
	if !isExpr || (e.kind != exprBody && e.kind != exprStatus) {
		return errors.New("a step output must be $response.body (with an optional #/pointer) or $statusCode")
	}
	if e.kind == exprBody && op.Result != nil {
		return checkPointer(op.Result, e.pointer)
	}
	return nil
}

func (b *binder) workflowOutput(value string) error {
	e, isExpr, err := parseExpr(value)
	if err != nil {
		return err
	}
	if !isExpr || e.kind != exprStepOutput {
		return errors.New("a workflow output must be $steps.<stepId>.outputs.<name>")
	}
	if !b.outputs[e.step][e.name] {
		return fmt.Errorf("no step %q with output %q", e.step, e.name)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
