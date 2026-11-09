package workflowdraft

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iml885203/lodog/internal/serving"
	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// An agent that may write must be able to take the write back, or "do it, then
// put it right" needs a person outside the gateway. So the draft looks for an
// undo of every write tool, and where it finds none it says so for a person to
// decide; it never leaves the gap unmentioned.
//
// The undo is found two ways. The model is asked for it while it groups the
// operations (the counterpart of a state change has a name only a reader of
// the document can see). Separately lodog checks the document's own shape: an
// operation on the same resource with another method (POST and DELETE of one
// path) or on one item of the collection a POST creates into (PUT, PATCH or
// DELETE of /things/{id}) can take the write back. A write with such an
// operation but no tool that calls it is put to the model once more; a write
// still without an undo is marked x-agent.needsReview with the question.

// writeRisks are the risks of a tool that changes something and could in
// principle be undone. irreversible and credential tools are declared
// beyond undoing.
var writeRisks = map[string]bool{"write": true, "money": true}

func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// shape is a path with every parameter name erased, so /a/{id} and /a/{logId}
// compare equal.
func shape(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// undoCandidates are the operations that could take back op, from the ones
// the gateway serves.
func undoCandidates(op spec.Operation, served []spec.Operation) []spec.Operation {
	var out []spec.Operation
	for _, c := range served {
		if !isMutating(c.Method) || (c.Method == op.Method && c.Path == op.Path) {
			continue
		}
		sameResource := shape(c.Path) == shape(op.Path)
		itemOfCollection := op.Method == http.MethodPost && shape(c.Path) == shape(op.Path)+"/{}" &&
			(c.Method == http.MethodPut || c.Method == http.MethodPatch || c.Method == http.MethodDelete)
		if sameResource || itemOfCollection {
			out = append(out, c)
		}
	}
	return out
}

// gap is a write tool no tool takes back.
type gap struct {
	Tool       string
	Summary    string
	Writes     []spec.Operation
	Candidates []spec.Operation
}

func (g gap) note() string {
	writes := opList(g.Writes)
	if len(g.Candidates) == 0 {
		return fmt.Sprintf("%s changes data and no operation in the document undoes it (nothing else on the same resource or its items changes data). Agents cannot take it back through the gateway; decide whether that is acceptable or an undo has to be added by hand.", writes)
	}
	return fmt.Sprintf("%s changes data and no tool undoes it. The document has %s on the same resource: decide whether to add a tool for it, or accept that agents cannot take this back.", writes, opList(g.Candidates))
}

func opList(ops []spec.Operation) string {
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = op.Method + " " + op.Path
	}
	return strings.Join(parts, ", ")
}

// gaps finds the write tools of d that nothing takes back. undoes maps a tool
// to the tool the model said it undoes. d must bind: when it does not, nothing
// can be said and no gap is reported.
func gaps(d *workflow.Document, doc *spec.Document, undoes map[string]string) []gap {
	tasks, err := workflow.Bind(d, doc)
	if err != nil {
		return nil
	}
	rawTools, _ := serving.RawTools(doc)
	served := make([]spec.Operation, len(rawTools))
	for i, t := range rawTools {
		served[i] = t.Op
	}
	var out []gap
	for _, t := range tasks {
		name := t.Workflow.WorkflowID
		if !writeRisks[t.Workflow.Agent.Risk] || undoes[name] != "" {
			continue
		}
		g := gap{Tool: name, Summary: t.Workflow.Summary}
		own := map[string]bool{}
		for _, op := range t.Ops {
			own[op.Method+" "+op.Path] = true
			if isMutating(op.Method) {
				g.Writes = append(g.Writes, op)
			}
		}
		if len(g.Writes) == 0 {
			continue
		}
		seen := map[string]bool{}
		for _, w := range g.Writes {
			for _, c := range undoCandidates(w, served) {
				if key := c.Method + " " + c.Path; !own[key] && !seen[key] {
					seen[key] = true
					g.Candidates = append(g.Candidates, c)
				}
			}
		}
		if !takenBack(tasks, name, undoes, seen) {
			out = append(out, g)
		}
	}
	return out
}

// takenBack reports whether another tool undoes the tool called name: the
// model said so, or one of its steps calls a candidate operation.
func takenBack(tasks []workflow.Task, name string, undoes map[string]string, candidates map[string]bool) bool {
	for _, u := range tasks {
		id := u.Workflow.WorkflowID
		if id == name {
			continue
		}
		if undoes[id] == name {
			return true
		}
		for _, op := range u.Ops {
			if candidates[op.Method+" "+op.Path] {
				return true
			}
		}
	}
	return false
}

// declaredUndoes is the model's own statement of which tool each tool undoes.
func declaredUndoes(a answer) map[string]string {
	out := map[string]string{}
	for _, t := range a.Tools {
		if t.Undoes != "" {
			out[t.Name] = t.Undoes
		}
	}
	return out
}

// keptDocument is d without the workflows that do not bind or repeat a name,
// and without next, so it can be checked as a whole before the draft is final.
func keptDocument(d *workflow.Document, bad map[string]string) *workflow.Document {
	out := *d
	out.Workflows = nil
	names := map[string]bool{}
	for _, w := range d.Workflows {
		if _, isBad := bad[w.WorkflowID]; isBad || names[w.WorkflowID] {
			continue
		}
		names[w.WorkflowID] = true
		out.Workflows = append(out.Workflows, withoutNext(w))
	}
	return &out
}

// addUndos asks the model once for the undo of every write tool whose
// resource has an operation that could undo it and which no tool calls. What
// the model drafts joins the answer; it is checked with the rest. The text is
// empty when nothing was asked; an error asking is returned as text, because
// the gaps it leaves are reported anyway.
func addUndos(ctx context.Context, asker Asker, doc *spec.Document, a answer, d *workflow.Document, bad map[string]string) (answer, string) {
	var ask []gap
	for _, g := range gaps(keptDocument(d, bad), doc, declaredUndoes(a)) {
		if len(g.Candidates) > 0 {
			ask = append(ask, g)
		}
	}
	if len(ask) == 0 {
		return a, ""
	}
	text, err := asker.Ask(ctx, undoPrompt(doc, ask))
	if err != nil {
		return a, fmt.Sprintf("the model could not be asked for undo tools: %v", err)
	}
	extra, err := parseTools(text)
	if err != nil {
		return a, fmt.Sprintf("the model's undo tools could not be read: %v", err)
	}
	wanted := map[string]bool{}
	for _, g := range ask {
		wanted[g.Tool] = true
	}
	for _, t := range extra {
		if wanted[t.Undoes] {
			a.Tools = append(a.Tools, t)
		}
	}
	return a, ""
}

func undoPrompt(doc *spec.Document, ask []gap) string {
	var b strings.Builder
	b.WriteString(prompt(doc))
	b.WriteString("\nThese write tools were drafted, and no tool takes them back. Next to each are the operations on the same resource that might:\n")
	for _, g := range ask {
		fmt.Fprintf(&b, "- %s (%s): calls %s; may be undone by %s\n", g.Tool, oneLine(g.Summary), opList(g.Writes), opList(g.Candidates))
	}
	b.WriteString("\nFor each one, draft the tool that undoes it when one of those operations really restores what the write changed (the same rules as above; the tool is a write, its risk is never read, and its description says which tool it undoes). Set \"undoes\" to the write tool's name. Leave a write out when none of its operations restores it.\n" +
		"Answer with only {\"tools\": [...]}; {\"tools\": []} when there is none.\n")
	return b.String()
}

// parseTools reads the tools of an answer that may hold none.
func parseTools(text string) ([]tool, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: no JSON object in %q", ErrMalformedAnswer, clip(text))
	}
	var a answer
	if err := json.Unmarshal([]byte(text[start:end+1]), &a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedAnswer, err)
	}
	return a.Tools, nil
}

// markUnreversed flags the write tools of the finished draft that nothing
// takes back, with the question for the person who reviews them.
func markUnreversed(out *Draft, doc *spec.Document, undoes map[string]string, askNote string) {
	for _, g := range gaps(out.Document, doc, undoes) {
		note := g.note()
		if askNote != "" && len(g.Candidates) > 0 {
			note += " (" + askNote + ")"
		}
		for i := range out.Document.Workflows {
			w := &out.Document.Workflows[i]
			if w.WorkflowID != g.Tool {
				continue
			}
			if !w.Agent.NeedsReview {
				out.Review = append(out.Review, w.WorkflowID)
			}
			w.Agent.NeedsReview = true
			w.Agent.ReviewNote = note
		}
		out.Unreversed = append(out.Unreversed, g.Tool+": "+note)
	}
}
