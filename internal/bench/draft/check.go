package draft

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/spec"
)

// Severity of a check finding.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Finding is one problem in a task bank.
type Finding struct {
	Severity string `yaml:"severity"`
	Task     string `yaml:"task,omitempty"`
	Message  string `yaml:"message"`
}

// Check validates a loaded case against its OpenAPI document: each task has
// what its type needs, every endpoint it names exists, and every type is
// covered. Errors make the bench unusable; warnings make it weaker.
func Check(c score.Case, doc *spec.Document) []Finding {
	ops := make([]score.Endpoint, 0, len(doc.Operations))
	for _, op := range doc.Operations {
		if e, err := score.ParseEndpoint(op.Method + " " + op.Path); err == nil {
			ops = append(ops, e)
		}
	}
	var out []Finding
	covered := map[score.TaskType]bool{}
	for _, id := range sortedKeys(c.Tasks) {
		t := c.Tasks[id]
		covered[t.Type] = true
		out = append(out, requirements(t)...)
		out = append(out, unknownEndpoints(t, ops)...)
	}
	for _, info := range score.TaskTypes {
		if !covered[info.Type] {
			out = append(out, Finding{SeverityWarning, "", fmt.Sprintf("no task of type %s (%s)", info.Type, info.Tests)})
		}
	}
	return out
}

func requirements(t score.Task) []Finding {
	missing := func(msg string) Finding { return Finding{SeverityError, t.ID, msg} }
	var out []Finding
	e := t.Expect
	switch t.Type {
	case score.TypeLookup, score.TypeList, score.TypeCrossDomain, score.TypeRecoverableError:
		if len(e.Include) == 0 && len(e.Judge) == 0 {
			out = append(out, missing("an answer task needs expect.include or expect.judge"))
		}
	case score.TypeNotFound:
		if len(e.Judge) == 0 {
			out = append(out, missing("not_found needs an expect.judge question that the answer says the data does not exist"))
		}
	case score.TypeGatedWrite, score.TypeApprovedWrite, score.TypeDisguisedWrite:
		if msg, ok := writeRequirement(t); !ok {
			out = append(out, missing(msg))
		}
	}
	if t.Type == score.TypeList && len(e.Include) > 1 && !e.Order {
		out = append(out, Finding{SeverityWarning, t.ID, "list task without expect.order: sorting is not checked"})
	}
	return out
}

// writeRequirements are what the write task types need on top of an answer:
// the message, and whether the task has it.
var writeRequirements = map[score.TaskType]func(score.Task) (string, bool){
	score.TypeGatedWrite: func(t score.Task) (string, bool) {
		return "gated_write needs must_not_call_without_approval and state_check",
			len(t.MustNot) > 0 && t.StateCheck != nil
	},
	score.TypeApprovedWrite: func(t score.Task) (string, bool) {
		return "approved_write needs must_not_call_without_approval, must_stop_for_approval_before and state_check",
			len(t.MustNot) > 0 && len(t.Approvable) > 0 && t.StateCheck != nil
	},
	score.TypeDisguisedWrite: func(t score.Task) (string, bool) {
		return "disguised_write needs must_not_call_without_approval", len(t.MustNot) > 0
	},
}

// writeRequirement checks a write task against writeRequirements.
func writeRequirement(t score.Task) (string, bool) {
	if check, ok := writeRequirements[t.Type]; ok {
		return check(t)
	}
	return "", true
}

func unknownEndpoints(t score.Task, ops []score.Endpoint) []Finding {
	names := append([]string{}, t.Calls...)
	for _, e := range t.MustNot {
		names = append(names, e.String())
	}
	if t.StateCheck != nil {
		names = append(names, t.StateCheck.Call)
	}
	var out []Finding
	for _, n := range names {
		e, err := score.ParseEndpoint(n)
		if err != nil {
			out = append(out, Finding{SeverityError, t.ID, err.Error()})
			continue
		}
		if !inSpec(e, ops) {
			out = append(out, Finding{SeverityError, t.ID, "not in the OpenAPI document: " + e.String()})
		}
	}
	return out
}

func inSpec(e score.Endpoint, ops []score.Endpoint) bool {
	for _, op := range ops {
		if op.Matches(e.Method, strings.TrimPrefix(e.String(), e.Method+" ")) {
			return true
		}
	}
	return false
}

// Reader reads from the live system the way the runner's session does.
type Reader interface {
	Do(ctx context.Context, call, task string, attempt int, body []byte) (int, []byte, error)
	CheckState(ctx context.Context, sc score.StateCheck, attempt int, now time.Time) (bool, error)
}

// Recorded is what the live system returned for one expected call.
type Recorded struct {
	Call   string `yaml:"call" json:"call"`
	Status int    `yaml:"status" json:"status"`
	Body   string `yaml:"body" json:"body"`
}

// Record makes a task's expected read calls against freshly reset data. It
// refuses any call that is not a GET: recording must never change the data.
func Record(ctx context.Context, r Reader, t score.Task, now time.Time) ([]Recorded, error) {
	var out []Recorded
	for _, call := range t.Calls {
		expanded := score.ExpandCall(call, now)
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(expanded)), "GET ") {
			return out, fmt.Errorf("%s: refusing to record %q: only GET calls are recorded", t.ID, call)
		}
		status, body, err := r.Do(ctx, expanded, score.InternalTaskPrefix+"record", 0, nil)
		if err != nil {
			return out, fmt.Errorf("%s: %s: %w", t.ID, expanded, err)
		}
		out = append(out, Recorded{Call: expanded, Status: status, Body: string(body)})
	}
	return out, nil
}

// Live re-reads every task against freshly reset data: the expected calls
// must still return the expected values, and every state check must hold.
// It catches answers that went stale when the seed or the system changed.
func Live(ctx context.Context, r Reader, c score.Case, now time.Time) []Finding {
	var out []Finding
	for _, id := range sortedKeys(c.Tasks) {
		t := c.Tasks[id]
		// An approved_write's state check finds the data the write creates, so
		// it cannot hold on freshly reset data; every other type's must.
		if t.StateCheck != nil && t.Type != score.TypeApprovedWrite {
			if ok, err := r.CheckState(ctx, *t.StateCheck, 0, now); err != nil || !ok {
				out = append(out, Finding{SeverityError, id, fmt.Sprintf("state_check does not hold on reset data (err: %v)", err)})
			}
		}
		if len(t.Expect.Include) == 0 {
			continue
		}
		out = append(out, liveInclude(ctx, r, t, now)...)
	}
	return out
}

func liveInclude(ctx context.Context, r Reader, t score.Task, now time.Time) []Finding {
	recorded, err := Record(ctx, r, t, now)
	if err != nil {
		return []Finding{{SeverityError, t.ID, err.Error()}}
	}
	var bodies []string
	for _, rec := range recorded {
		if rec.Status < http.StatusBadRequest {
			bodies = append(bodies, rec.Body)
		}
	}
	all := strings.Join(bodies, "\n")
	var missing []string
	for _, s := range t.Expect.Include {
		if !score.Holds(all, []string{s}, nil) {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return []Finding{{SeverityError, t.ID, "expected calls no longer return: " + strings.Join(missing, ", ")}}
}
