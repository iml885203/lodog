// Package workflowdraft drafts the L2 task tools: it asks a model to group a
// legacy API's operations, as the L1 overlay describes them, into about
// twenty tasks an operator does, and writes them as an Arazzo document for a
// person to review (docs/levels.md). Another team has nobody to write these
// by hand, so lodog starts them.
//
// The model answers in a small JSON shape; lodog builds the Arazzo document
// itself, so operation references, parameter locations and the document
// layout are never the model's to get wrong. Every drafted workflow is bound
// against the document before it is written: one that does not bind is sent
// back to the model once with the errors, then dropped and reported. That
// includes a workflow with a step the gateway does not serve: the model is
// only shown the operations the gateway serves, and a workflow that still
// uses another one is treated as not binding.
package workflowdraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// Asker sends a prompt to a model and returns its text.
type Asker interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

// ErrMalformedAnswer means the model's answer is not the JSON shape asked for.
var ErrMalformedAnswer = errors.New("model answer is not a set of task tools")

// Options shape a draft.
type Options struct {
	Title  string // the Arazzo info.title
	Source string // the sourceDescriptions name, e.g. backoffice
	URL    string // where the source document is, relative to the workflows file
}

// Draft is the drafted document and what was left out.
type Draft struct {
	Document *workflow.Document
	// Dropped are tools that still did not bind after one retry, with why.
	Dropped []string
	// Review are tools the model was not confident about.
	Review []string
	// Unreversed are write tools nothing takes back, with the question a
	// person answers; each is also marked x-agent.needsReview.
	Unreversed []string
}

// answer is the JSON shape the model returns.
type answer struct {
	Instructions string `json:"instructions"`
	Tools        []tool `json:"tools"`
}

type tool struct {
	Name         string                    `json:"name"`
	Summary      string                    `json:"summary"`
	Description  string                    `json:"description"`
	WhenToUse    string                    `json:"whenToUse"`
	WhenNotToUse string                    `json:"whenNotToUse"`
	Risk         string                    `json:"risk"`
	Confident    bool                      `json:"confident"`
	Inputs       map[string]map[string]any `json:"inputs"`
	Steps        []step                    `json:"steps"`
	Outputs      map[string]string         `json:"outputs"`
	Next         []string                  `json:"next"`
	// Undoes names the write tool this tool takes back.
	Undoes string `json:"undoes"`
}

type step struct {
	ID         string            `json:"id"`
	Operation  string            `json:"operation"`
	Parameters map[string]any    `json:"parameters"`
	Body       any               `json:"body"`
	Outputs    map[string]string `json:"outputs"`
}

// Run drafts the task tools for doc, the legacy document with its overlay.
func Run(ctx context.Context, doc *spec.Document, asker Asker, o Options) (*Draft, error) {
	text, err := asker.Ask(ctx, prompt(doc))
	if err != nil {
		return nil, fmt.Errorf("ask model for task tools: %w", err)
	}
	a, err := parse(text)
	if err != nil {
		return nil, err
	}
	d := build(a, doc, o)
	bad := unbound(d, doc)
	if len(bad) > 0 {
		retry, err := asker.Ask(ctx, retryPrompt(doc, text, bad))
		if err != nil {
			return nil, fmt.Errorf("ask model to fix task tools: %w", err)
		}
		if fixed, err := parse(retry); err == nil {
			a = merge(a, fixed)
			d = build(a, doc, o)
			bad = unbound(d, doc)
		}
	}
	a, askNote := addUndos(ctx, asker, doc, a, d, bad)
	d = build(a, doc, o)
	bad = unbound(d, doc)
	out, err := finish(d, bad, doc)
	if err != nil {
		return nil, err
	}
	markUnreversed(out, doc, declaredUndoes(a), askNote)
	return out, nil
}

// parse reads the model's JSON object; it accepts one wrapped in prose or a
// code fence.
func parse(text string) (answer, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return answer{}, fmt.Errorf("%w: no JSON object in %q", ErrMalformedAnswer, clip(text))
	}
	var a answer
	if err := json.Unmarshal([]byte(text[start:end+1]), &a); err != nil {
		return answer{}, fmt.Errorf("%w: %w", ErrMalformedAnswer, err)
	}
	if len(a.Tools) == 0 {
		return answer{}, fmt.Errorf("%w: no tools", ErrMalformedAnswer)
	}
	return a, nil
}

// merge takes the fixed tools from a retry, keeping the first answer's tools
// the retry left out, and its instructions unless the retry rewrote them.
func merge(first, fixed answer) answer {
	byName := map[string]tool{}
	for _, t := range fixed.Tools {
		byName[t.Name] = t
	}
	out := answer{Instructions: first.Instructions}
	if fixed.Instructions != "" {
		out.Instructions = fixed.Instructions
	}
	for _, t := range first.Tools {
		if f, ok := byName[t.Name]; ok {
			out.Tools = append(out.Tools, f)
			delete(byName, t.Name)
			continue
		}
		out.Tools = append(out.Tools, t)
	}
	for _, t := range fixed.Tools {
		if _, ok := byName[t.Name]; ok {
			out.Tools = append(out.Tools, t)
		}
	}
	return out
}

// build turns the answer into an Arazzo document. It does not judge it: Bind
// does.
func build(a answer, doc *spec.Document, o Options) *workflow.Document {
	d := &workflow.Document{
		Arazzo:             "1.0.1",
		Info:               workflow.Info{Title: o.Title, Version: "1", Description: strings.TrimSpace(a.Instructions)},
		SourceDescriptions: []workflow.Source{{Name: o.Source, URL: o.URL, Type: "openapi"}},
	}
	for _, t := range a.Tools {
		d.Workflows = append(d.Workflows, buildWorkflow(t, doc, o.Source))
	}
	return d
}

func buildWorkflow(t tool, doc *spec.Document, source string) workflow.Workflow {
	if t.Undoes != "" && !writeRisks[t.Risk] && t.Risk != "irreversible" && t.Risk != "credential" {
		t.Risk = "write" // taking a write back is a write
	}
	w := workflow.Workflow{
		WorkflowID:  t.Name,
		Summary:     t.Summary,
		Description: t.Description,
		Outputs:     t.Outputs,
		Agent: workflow.Agent{
			WhenToUse: t.WhenToUse, WhenNotToUse: t.WhenNotToUse, Risk: t.Risk,
			Next: t.Next, NeedsReview: !t.Confident,
		},
	}
	if len(t.Inputs) > 0 {
		props := map[string]any{}
		var required []any
		for _, name := range sortedKeys(t.Inputs) {
			schema := map[string]any{}
			for k, v := range t.Inputs[name] {
				if k == "required" {
					if r, _ := v.(bool); r {
						required = append(required, name)
					}
					continue
				}
				schema[k] = v
			}
			props[name] = schema
		}
		w.Inputs = map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			w.Inputs["required"] = required
		}
	}
	for _, s := range t.Steps {
		w.Steps = append(w.Steps, buildStep(s, doc, source))
	}
	return w
}

func buildStep(s step, doc *spec.Document, source string) workflow.Step {
	out := workflow.Step{StepID: s.ID, Outputs: s.Outputs}
	method, path, _ := strings.Cut(strings.TrimSpace(s.Operation), " ")
	op, found := find(doc, strings.ToUpper(method), path)
	if found {
		out.OperationPath = workflow.OperationPath(source, op)
	} else {
		// Left for Bind to report: the model named an operation that is not
		// in the document.
		out.OperationPath = workflow.OperationPath(source, spec.Operation{Method: strings.ToUpper(method), Path: path})
	}
	for _, name := range sortedKeys(s.Parameters) {
		p := workflow.Parameter{Name: name, Value: s.Parameters[name]}
		for _, declared := range op.Params {
			if declared.Name == name {
				p.In = declared.In
			}
		}
		out.Parameters = append(out.Parameters, p)
	}
	if s.Body != nil {
		out.RequestBody = &workflow.RequestBody{ContentType: "application/json", Payload: s.Body}
	}
	return out
}

func find(doc *spec.Document, method, path string) (spec.Operation, bool) {
	for _, op := range doc.Operations {
		if op.Method == method && op.Path == path {
			return op, true
		}
	}
	return spec.Operation{}, false
}

// unbound binds each workflow on its own, the way the gateway loads it, and
// returns the errors by name.
func unbound(d *workflow.Document, doc *spec.Document) map[string]string {
	bad := map[string]string{}
	for _, w := range d.Workflows {
		single := *d
		single.Workflows = []workflow.Workflow{withoutNext(w)}
		_, skipped, err := serving.TaskTools(doc, &single)
		switch {
		case err != nil:
			bad[w.WorkflowID] = err.Error()
		case len(skipped) > 0:
			bad[w.WorkflowID] = skipped[0].Reason
		}
	}
	if strings.TrimSpace(d.Info.Description) == "" {
		bad["(instructions)"] = "the instructions are empty"
	}
	return bad
}

// withoutNext drops next, which names other workflows and is checked once
// the whole set is known.
func withoutNext(w workflow.Workflow) workflow.Workflow {
	w.Agent.Next = nil
	return w
}

// finish drops what still does not bind, removes next entries that name a
// dropped tool, and binds the result as a whole.
func finish(d *workflow.Document, bad map[string]string, doc *spec.Document) (*Draft, error) {
	if reason, ok := bad["(instructions)"]; ok {
		return nil, fmt.Errorf("%w: %s", ErrMalformedAnswer, reason)
	}
	out := &Draft{Document: d}
	kept := d.Workflows[:0:0]
	names := map[string]bool{}
	for _, w := range d.Workflows {
		if reason, ok := bad[w.WorkflowID]; ok {
			out.Dropped = append(out.Dropped, w.WorkflowID+": "+reason)
			continue
		}
		if names[w.WorkflowID] {
			out.Dropped = append(out.Dropped, w.WorkflowID+": drafted twice")
			continue
		}
		names[w.WorkflowID] = true
		kept = append(kept, w)
	}
	for i, w := range kept {
		var next []string
		for _, n := range w.Agent.Next {
			if names[n] {
				next = append(next, n)
			}
		}
		kept[i].Agent.Next = next
		if w.Agent.NeedsReview {
			out.Review = append(out.Review, w.WorkflowID)
		}
	}
	d.Workflows = kept
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: no drafted tool binds to the document: %s", ErrMalformedAnswer, strings.Join(out.Dropped, "; "))
	}
	if _, err := workflow.Bind(d, doc); err != nil {
		return nil, fmt.Errorf("drafted workflows do not load: %w", err)
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func clip(s string) string {
	const n = 200
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
