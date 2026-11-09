// Package enrich drafts an L1 overlay: it asks a model to describe every
// operation of a legacy OpenAPI document (what it is for, when to use it,
// what each parameter means, a first guess at its risk) and turns the answers
// into an OpenAPI Overlay. The swagger is required; the legacy source code is
// optional and, when given, lets the model confirm meanings such as enum
// values instead of guessing. The result is a draft for a person to review:
// operations the model was unsure about are marked needsReview.
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/policydraft"

	"github.com/iml885203/lodog/internal/overlay"
	"github.com/iml885203/lodog/internal/spec"
)

// Model answers one prompt. sourceDir is empty when there is no source to read.
type Model interface {
	Complete(ctx context.Context, prompt, sourceDir string) (string, error)
}

// Options shape a run.
type Options struct {
	SourceDir   string   // optional legacy source; empty = swagger only
	BatchSize   int      // operations per prompt
	Concurrency int      // prompts in flight
	Title       string   // overlay title
	Risks       []string // risk levels the model may propose; empty = DefaultRisks
}

// Described is what the model said about one operation.
type Described struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Summary      string            `json:"summary"`
	Description  string            `json:"description"`
	WhenToUse    string            `json:"whenToUse"`
	WhenNotToUse string            `json:"whenNotToUse"`
	Risk         string            `json:"risk"`
	Params       map[string]string `json:"params"`
	Errors       map[string]string `json:"errors"`
	Confidence   string            `json:"confidence"`
	Notes        string            `json:"notes"`
}

// Result is the drafted overlay and what the run could not cover.
type Result struct {
	Overlay *overlay.Overlay
	// Missing are operations no answer described.
	Missing []string
	// Review are operations the model marked low confidence.
	Review []string
}

// DefaultRisks is the risk vocabulary used when the case has no policy: the
// overlay-declarable risks of lodog's default risk set, the one `lodog policy
// draft` starts from.
func DefaultRisks() []string {
	return policydraft.DefaultRiskSet().OverlayRisks()
}

// RiskVocabulary is the single place the levels the model may propose come
// from: the case's policy when it has one, DefaultRisks otherwise. Levels the
// default set knows keep its order; the policy's own come after, sorted.
func RiskVocabulary(p *policy.Policy) []string {
	if p == nil || len(p.Risks) == 0 {
		return DefaultRisks()
	}
	var out []string
	for _, r := range DefaultRisks() {
		if _, ok := p.Risks[r]; ok {
			out = append(out, r)
		}
	}
	var rest []string
	for r := range p.Risks {
		if !slices.Contains(out, r) {
			rest = append(rest, r)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Run drafts the overlay for doc.
func Run(ctx context.Context, doc *spec.Document, m Model, o Options) (*Result, error) {
	if o.BatchSize <= 0 {
		o.BatchSize = 12
	}
	if len(o.Risks) == 0 {
		o.Risks = DefaultRisks()
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	all, err := describeBatches(ctx, m, Batches(doc.Operations, o.BatchSize), o)
	if err != nil {
		return nil, err
	}
	// A model sometimes drops an operation from an otherwise good answer;
	// ask again for just those, so coverage does not depend on one reply.
	for range coverageRounds {
		missing := undescribed(doc.Operations, all)
		if len(missing) == 0 {
			break
		}
		more, err := describeBatches(ctx, m, Batches(missing, o.BatchSize), o)
		if err != nil {
			return nil, err
		}
		all = append(all, more...)
	}
	return build(doc, all, o.Title, o.Risks), nil
}

// coverageRounds bounds how often operations missing from the answers are
// asked for again.
const coverageRounds = 2

func undescribed(ops []spec.Operation, described []Described) []spec.Operation {
	seen := map[string]bool{}
	for _, d := range described {
		seen[key(d.Method, d.Path)] = true
	}
	var out []spec.Operation
	for _, op := range ops {
		if !seen[key(op.Method, op.Path)] {
			out = append(out, op)
		}
	}
	return out
}

func describeBatches(ctx context.Context, m Model, batches [][]spec.Operation, o Options) ([]Described, error) {
	answers := make([][]Described, len(batches))
	errs := make([]error, len(batches))
	sem := make(chan struct{}, o.Concurrency)
	var wg sync.WaitGroup
	for i, b := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			answers[i], errs[i] = describe(ctx, m, b, o.SourceDir, o.Risks)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var all []Described
	for _, a := range answers {
		all = append(all, a...)
	}
	return all, nil
}

// attempts bounds how often one batch is asked again after a malformed
// answer; a model sometimes breaks its JSON once and gets it right next time.
const attempts = 2

func describe(ctx context.Context, m Model, b []spec.Operation, sourceDir string, risks []string) ([]Described, error) {
	var last error
	for range attempts {
		out, err := m.Complete(ctx, Prompt(b, sourceDir != "", risks), sourceDir)
		if err != nil {
			return nil, fmt.Errorf("batch %s: %w", b[0].Tag, err)
		}
		described, err := ParseAnswer(out)
		if err == nil {
			return described, nil
		}
		last = err
	}
	return nil, fmt.Errorf("batch %s: %w", b[0].Tag, last)
}

// Batches groups operations by tag, so one prompt sees a coherent domain, and
// splits a tag larger than size.
func Batches(ops []spec.Operation, size int) [][]spec.Operation {
	byTag := map[string][]spec.Operation{}
	var tags []string
	for _, op := range ops {
		if _, ok := byTag[op.Tag]; !ok {
			tags = append(tags, op.Tag)
		}
		byTag[op.Tag] = append(byTag[op.Tag], op)
	}
	sort.Strings(tags)
	var out [][]spec.Operation
	for _, tag := range tags {
		group := byTag[tag]
		for len(group) > size {
			out = append(out, group[:size])
			group = group[size:]
		}
		out = append(out, group)
	}
	return out
}

// ParseAnswer reads the model's JSON, tolerating a fenced code block or text
// around it.
func ParseAnswer(text string) ([]Described, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, errors.New("no JSON object in the answer")
	}
	var body struct {
		Operations []Described `json:"operations"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &body); err != nil {
		return nil, fmt.Errorf("answer is not the expected JSON: %w", err)
	}
	return body.Operations, nil
}

func key(method, path string) string { return strings.ToUpper(method) + " " + path }

func build(doc *spec.Document, described []Described, title string, risks []string) *Result {
	byKey := map[string]Described{}
	for _, d := range described {
		byKey[key(d.Method, d.Path)] = d
	}
	res := &Result{Overlay: &overlay.Overlay{Overlay: "1.0.0", Info: overlay.Info{Title: title, Version: "1"}}}
	for _, op := range doc.Operations {
		k := key(op.Method, op.Path)
		d, ok := byKey[k]
		if !ok {
			res.Missing = append(res.Missing, k)
			continue
		}
		agent := map[string]any{}
		setIf(agent, "whenToUse", d.WhenToUse)
		setIf(agent, "whenNotToUse", d.WhenNotToUse)
		if slices.Contains(risks, d.Risk) {
			agent["risk"] = d.Risk
		}
		if !strings.EqualFold(d.Confidence, "high") || unsure(d) {
			agent["needsReview"] = true
			res.Review = append(res.Review, k+reviewNote(d))
		}
		update := map[string]any{"x-agent": agent}
		setIf(update, "summary", d.Summary)
		setIf(update, "description", d.Description)
		if responses := errorResponses(d.Errors); len(responses) > 0 {
			update["responses"] = responses
		}
		res.Overlay.Actions = append(res.Overlay.Actions, overlay.Action{
			Target: overlay.OperationTarget(op.Method, op.Path), Update: update,
		})
		for _, p := range op.Params {
			if meaning := strings.TrimSpace(d.Params[p.ArgName()]); meaning != "" {
				target := overlay.ParamTarget(op.Method, op.Path, p.Name)
				if p.ArgName() != p.Name {
					target = overlay.ParamTargetIn(op.Method, op.Path, p.Name, p.In)
				}
				res.Overlay.Actions = append(res.Overlay.Actions, overlay.Action{
					Target: target,
					Update: map[string]any{"description": meaning},
				})
			}
		}
	}
	return res
}

// errorResponses keeps 4xx/5xx statuses the model described, as OpenAPI
// responses an agent can read to correct itself.
func errorResponses(errs map[string]string) map[string]any {
	out := map[string]any{}
	for code, meaning := range errs {
		meaning = strings.TrimSpace(meaning)
		if isErrorStatus(code) && meaning != "" {
			out[code] = map[string]any{"description": meaning}
		}
	}
	return out
}

func isErrorStatus(code string) bool {
	if len(code) != 3 || (code[0] != '4' && code[0] != '5') {
		return false
	}
	return code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9'
}

func reviewNote(d Described) string {
	if d.Notes == "" {
		return ""
	}
	return ": " + d.Notes
}

func setIf(m map[string]any, k, v string) {
	if v = strings.TrimSpace(v); v != "" {
		m[k] = v
	}
}

// unsure is true when the model claimed high confidence but its own text says
// it guessed somewhere; such an operation still goes to review.
func unsure(d Described) bool {
	texts := []string{d.Summary, d.Description, d.WhenToUse, d.WhenNotToUse}
	for _, m := range []map[string]string{d.Params, d.Errors} {
		for _, v := range m {
			texts = append(texts, v)
		}
	}
	return overlay.HasUncertainty(strings.Join(texts, "\n"))
}
