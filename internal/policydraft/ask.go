package policydraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// Asker sends a prompt to a model and returns its text.
type Asker interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

// BatchSize is how many operations one model call classifies: enough to keep
// the calls few, few enough that every operation still gets attention.
const BatchSize = 30

// ErrMalformedAnswer means the model's answer cannot be read as a
// classification of exactly the operations it was given. The draft stops
// rather than filling the gap with a default nobody chose.
var ErrMalformedAnswer = errors.New("model answer is not a classification of the batch")

const promptHead = `You classify the operations of a legacy HTTP API by risk, for a gateway that lets an AI agent call the API.
Every operation below may change state. For each one choose exactly one risk:
`

const promptTail = `When an operation fits more than one risk, choose the one listed first.
Set "confident" to false when the method, path, tag, summary and parameters do not settle the risk; a person reviews those.
If an operation looks like it only reads (a search or a report sent as POST), still choose %[1]s, set "confident" to false and say so in the reason: a person decides whether to allow it.
A GET that clearly only reads data (a list, a search, a report, a lookup) may be answered with risk %[3]q instead, and the reason must say why it only reads. Choose %[3]q for a GET only when you are sure; when in doubt choose a risk above. Never choose %[3]q for any other method.
Answer with only a JSON array, one object per operation:
[{"id": 1, "risk": %[2]q, "reason": "one line", "confident": true}]

Operations:
`

type question struct {
	ID      int      `json:"id"`
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Tag     string   `json:"tag,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Params  []string `json:"params,omitempty"`
	Signal  string   `json:"signal"`
}

type answer struct {
	ID        int    `json:"id"`
	Risk      string `json:"risk"`
	Reason    string `json:"reason"`
	Confident *bool  `json:"confident"`
}

// askModel classifies ops in batches of BatchSize.
func askModel(ctx context.Context, asker Asker, sig readiness.Signals, rs RiskSet, ops []spec.Operation) ([]Classification, error) {
	var out []Classification
	for start := 0; start < len(ops); start += BatchSize {
		batch := ops[start:min(start+BatchSize, len(ops))]
		p, err := prompt(sig, rs, batch)
		if err != nil {
			return nil, err
		}
		text, err := asker.Ask(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("ask model about operations %d-%d: %w", start+1, start+len(batch), err)
		}
		cs, err := parseAnswer(text, rs, batch)
		if err != nil {
			return nil, fmt.Errorf("operations %d-%d: %w", start+1, start+len(batch), err)
		}
		out = append(out, cs...)
	}
	return out, nil
}

func prompt(sig readiness.Signals, rs RiskSet, batch []spec.Operation) (string, error) {
	qs := make([]question, len(batch))
	for i, op := range batch {
		qs[i] = question{ID: i + 1, Method: op.Method, Path: op.Path, Tag: op.Tag, Summary: op.Summary, Params: paramNames(op), Signal: signal(sig, op)}
	}
	data, err := json.MarshalIndent(qs, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode operations for the model: %w", err)
	}
	var b strings.Builder
	b.WriteString(promptHead)
	for _, l := range rs.modelChoices() {
		fmt.Fprintf(&b, "- %s: %s\n", l.Name, l.Means)
	}
	write := rs.riskOf(ClassWrite)
	fmt.Fprintf(&b, promptTail, write, write, rs.riskOf(ClassRead))
	return b.String() + string(data), nil
}

func paramNames(op spec.Operation) []string {
	names := make([]string, 0, len(op.Params))
	for _, p := range op.Params {
		names = append(names, p.In+":"+p.Name)
	}
	return append(names, bodyFields(op.Body)...)
}

func bodyFields(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	fields := make([]string, 0, len(props))
	for name := range props {
		fields = append(fields, "body:"+name)
	}
	slices.Sort(fields)
	return fields
}

func signal(sig readiness.Signals, op spec.Operation) string {
	if word, ok := sig.SideEffectGET(op); ok {
		return fmt.Sprintf("a GET whose path names an action (%s)", strings.ToLower(word))
	}
	return op.Method + " changes state"
}

// parseAnswer reads the model's JSON array. It accepts the array wrapped in
// prose or a code fence, and nothing less than one valid answer per operation.
func parseAnswer(text string, rs RiskSet, batch []spec.Operation) ([]Classification, error) {
	start, end := strings.Index(text, "["), strings.LastIndex(text, "]")
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: no JSON array in %q", ErrMalformedAnswer, clip(text))
	}
	var answers []answer
	if err := json.Unmarshal([]byte(text[start:end+1]), &answers); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedAnswer, err)
	}
	byID := make(map[int]answer, len(answers))
	for _, a := range answers {
		if err := checkAnswer(a, rs, batch); err != nil {
			return nil, err
		}
		if _, dup := byID[a.ID]; dup {
			return nil, fmt.Errorf("%w: id %d answered twice", ErrMalformedAnswer, a.ID)
		}
		byID[a.ID] = a
	}
	out := make([]Classification, len(batch))
	for i, op := range batch {
		a, ok := byID[i+1]
		if !ok {
			return nil, fmt.Errorf("%w: no answer for id %d (%s %s)", ErrMalformedAnswer, i+1, op.Method, op.Path)
		}
		out[i] = fromAnswer(rs, op, a)
	}
	return out, nil
}

func checkAnswer(a answer, rs RiskSet, batch []spec.Operation) error {
	if a.ID < 1 || a.ID > len(batch) {
		return fmt.Errorf("%w: id %d is not in the batch", ErrMalformedAnswer, a.ID)
	}
	var choices []string
	for _, l := range rs.modelChoices() {
		choices = append(choices, l.Name)
	}
	// Only an operation whose method can read may be answered read.
	if !readiness.MethodMutates(batch[a.ID-1].Method) {
		choices = append(choices, rs.riskOf(ClassRead))
	}
	switch {
	case !slices.Contains(choices, a.Risk):
		return fmt.Errorf("%w: id %d: risk %q is not one of %s", ErrMalformedAnswer, a.ID, a.Risk, strings.Join(choices, ", "))
	case strings.TrimSpace(a.Reason) == "":
		return fmt.Errorf("%w: id %d gives no reason", ErrMalformedAnswer, a.ID)
	case a.Confident == nil:
		return fmt.Errorf("%w: id %d does not say whether it is confident", ErrMalformedAnswer, a.ID)
	}
	return nil
}

func fromAnswer(rs RiskSet, op spec.Operation, a answer) Classification {
	c := Classification{Method: op.Method, Path: op.Path, Risk: a.Risk, Source: ByModel, NeedsReview: !*a.Confident}
	reason := strings.Join(strings.Fields(a.Reason), " ")
	if a.Risk == rs.riskOf(ClassRead) && c.NeedsReview {
		// Fail closed: a read the model is not sure of is held for approval.
		c.Risk = rs.riskOf(ClassWrite)
		reason = "answered read without confidence: " + reason
	}
	if c.NeedsReview {
		c.Why = "needs review: model, not confident: " + reason
	} else {
		c.Why = "model: " + reason
	}
	return c
}

func clip(s string) string {
	const n = 200
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
