package readiness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/docfetch"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

// FromSpec is the raw document in a file as an agent sees it served one to
// one. sig is the system's vocabulary (LoadSignals of its case, or
// DefaultSignals).
func FromSpec(path string, sig Signals) (View, error) {
	return FromSource(context.Background(), nil, path, sig)
}

// FromSource is FromSpec for a document that may also be an http(s) URL.
func FromSource(ctx context.Context, client *http.Client, src string, sig Signals) (View, error) {
	data, err := docfetch.Fetch(ctx, client, src)
	if err != nil {
		return View{}, err
	}
	doc, err := spec.Parse(data)
	if err != nil {
		return View{}, fmt.Errorf("%s is not a document lodog can read: %w", src, err)
	}
	return View{Source: src, Doc: doc, Signals: sig}, nil
}

// FromGateway is what a gateway level gives the agent. The policy and the
// agent's identity count only at L3, where the gateway enforces them; below
// L3 the identity labels only file calls for the benchmark.
func FromGateway(path string) (View, []string, error) {
	cfg, err := casecfg.LoadConfig(path)
	if err != nil {
		return View{}, nil, err
	}
	doc, err := casecfg.LoadDocument(cfg)
	if err != nil {
		return View{}, nil, err
	}
	sig, err := LoadSignals(cfg.Case)
	if err != nil {
		return View{}, nil, err
	}
	v := View{Source: path, Level: string(cfg.Level), Doc: doc, Signals: sig}
	p, err := serving.LoadPolicy(cfg)
	if err != nil {
		return View{}, nil, err
	}
	if p != nil {
		v.Policy = p
		v.Identity = cfg.Identity.Actor != "" && cfg.Identity.OnBehalfOf != ""
	}
	if !cfg.Level.ServesTasks() {
		return v, nil, nil
	}
	plan, err := serving.LoadTasks(cfg, doc, p)
	if err != nil {
		return View{}, nil, err
	}
	v.Tasks = make([]serving.TaskTool, len(plan.Served))
	for i, t := range plan.Served {
		v.Tasks[i] = t.TaskTool
	}
	v.TasksSkipped = plan.Skipped
	return v, []string{taskNote(v)}, nil
}

// CallChangesState reports whether a call recorded against this view changed
// state, for the report: the operation the call hit is judged by
// ScoreMutating, so a GET that only names an action in its path counts and a
// preview does not. A call that hits no operation of the document is judged by
// its method and path alone.
func (v View) CallChangesState(method, path string) bool {
	op := spec.Operation{Method: method, Path: path}
	if v.Doc != nil {
		if hit, ok := newMatcher(v.Doc).match(method, path); ok {
			op = hit
		}
	}
	return v.Signals.ScoreMutating(op)
}

// Reach says whether the level hands the agent a tool whose steps include the
// operation method and path hit, and, when it does not, whether the policy
// hides that operation. A level that serves one tool per operation reaches
// every operation of its document. A call that hits no operation of the
// document is not reached and not hidden.
func (v View) Reach(method, path string) (reached, hidden bool) {
	if v.Doc == nil {
		return false, false
	}
	op, ok := newMatcher(v.Doc).match(method, path)
	if !ok {
		return false, false
	}
	if v.Tasks == nil {
		return true, false
	}
	for _, t := range v.Tasks {
		for _, step := range t.Task.Ops {
			if strings.EqualFold(step.Method, op.Method) && step.Path == op.Path {
				return true, false
			}
		}
	}
	return false, v.Policy != nil && v.Policy.Decide(op.Method, op.Path).Action == policy.Hide
}

// Render writes the report for a person.
func Render(w io.Writer, r Report) error {
	var b strings.Builder
	level := ""
	if r.Level != "" {
		level = "，" + r.Level
	}
	fmt.Fprintf(&b, "靜態分數 %.2f / %.0f（%s%s，%d 個 tool）\n\n", r.Points, r.Max, r.Source, level, r.Tools)
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%5.2f / %.0f  %s：%s\n", c.Points, c.Max, c.Name, c.Detail)
		for _, e := range c.Examples {
			fmt.Fprintf(&b, "    - %s\n", e)
		}
	}
	renderSkipped(&b, r.Skipped)
	renderL4(&b, r.L4)
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\n注意：%s\n", n)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// renderSkipped lists every operation the agent is not given, so a team sees
// what is missing before it runs the gateway.
func renderSkipped(b *strings.Builder, skipped []Unserved) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "\n不會變成 tool 的 operation（%d 個，不在上面的分數裡）：\n", len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(b, "    - %s %s：%s\n", s.Method, s.Path, s.Reason)
	}
}
