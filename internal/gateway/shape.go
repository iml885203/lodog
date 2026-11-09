package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/workflow"
)

// maxTaskResult bounds the data one task tool call hands the agent. A page of
// a list fits; a response beyond it would crowd out the agent's context, so
// its longest lists are cut and the cut is stated.
const maxTaskResult = 32 << 10

// Envelope is what a task tool returns, as structured content and as JSON
// text for clients that only read text. Status is ok or error: an ok result
// carries data and no reason; an error always carries a reason and a next
// action the agent can follow.
type Envelope struct {
	Status    string       `json:"status"`
	Data      any          `json:"data,omitempty"`
	Truncated []Truncation `json:"truncated,omitempty"`
	Next      []string     `json:"next,omitempty"`
	Error     *TaskError   `json:"error,omitempty"`
}

// The statuses of an Envelope.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Truncation says which list was cut to fit the result, and how much of it
// the agent sees.
type Truncation struct {
	Path  string `json:"path"`
	Kept  int    `json:"kept"`
	Total int    `json:"total"`
}

// TaskError is why a task stopped and what the agent should do next.
type TaskError struct {
	Step       string `json:"step"`
	Call       string `json:"call"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Reason     string `json:"reason"`
	NextAction string `json:"nextAction"`
	Detail     any    `json:"detail,omitempty"`
}

// shape turns a run into the envelope the agent reads.
func shape(w workflow.Workflow, r workflow.Result) *mcp.CallToolResult {
	if f := r.Failure; f != nil {
		env := Envelope{Status: StatusError, Error: failure(f)}
		if env.Error.Detail != nil {
			detail, cuts := fit(env.Error.Detail, maxTaskResult)
			env.Error.Detail, env.Truncated = detail, cuts
		}
		return envelopeResult(env, true)
	}
	data, cuts := fit(r.Data, maxTaskResult)
	return envelopeResult(Envelope{Status: StatusOK, Data: data, Truncated: cuts, Next: w.Agent.Next}, false)
}

func failure(f *workflow.Failure) *TaskError {
	e := &TaskError{Step: f.StepID, Call: f.Op.Method + " " + f.Op.Path, HTTPStatus: f.Status, Detail: f.Body}
	if f.Missing != "" {
		e.Reason = fmt.Sprintf("Step %s needs %s, which an earlier step did not return.", f.StepID, f.Missing)
		e.NextAction = "The earlier lookup found nothing to continue with. Check the arguments (a name or id may not exist) and say so; do not guess a value."
		return e
	}
	e.Reason = fmt.Sprintf("The legacy system refused step %s with HTTP %d %s.", f.StepID, f.Status, http.StatusText(f.Status))
	e.NextAction = nextForStatus(f.Status)
	return e
}

// nextForStatus is the agent's next move for a legacy refusal.
func nextForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "Stop and tell the operator: the gateway's account may not do this."
	case status == http.StatusNotFound:
		return "The record does not exist. Check the id, or find it with a search tool first; do not invent one."
	case status == http.StatusConflict:
		return "The record is not in a state that allows this. Read it again before deciding whether to retry."
	case status >= 500:
		return "Retry once later; if it fails again, tell the operator."
	default:
		return "Read the detail, correct the arguments and call again."
	}
}

func envelopeResult(env Envelope, isError bool) *mcp.CallToolResult {
	text, err := json.Marshal(env)
	if err != nil {
		res, _ := toolError("The gateway could not encode the result: "+err.Error(), "Retry once; if it fails again, tell the operator.")
		return res
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
		StructuredContent: env,
		IsError:           isError,
	}
}

// fit cuts the longest list in v until v encodes within limit bytes, and
// says what it cut. A value with no list left to cut is replaced by the start
// of its JSON text.
func fit(v any, limit int) (any, []Truncation) {
	totals := map[string]int{}
	for {
		encoded, err := json.Marshal(v)
		if err != nil || len(encoded) <= limit {
			return v, truncations(v, totals)
		}
		path, list := longestList(v, "")
		if list == nil {
			cut := string(encoded[:limit])
			return map[string]any{"partialJSON": cut}, append(truncations(v, totals), Truncation{Path: "", Kept: limit, Total: len(encoded)})
		}
		if _, seen := totals[path]; !seen {
			totals[path] = len(list)
		}
		v = replaceAt(v, splitPath(path), list[:len(list)/2])
	}
}

func truncations(v any, totals map[string]int) []Truncation {
	var out []Truncation
	for path, total := range totals {
		if list, ok := at(v, splitPath(path)).([]any); ok {
			out = append(out, Truncation{Path: path, Kept: len(list), Total: total})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// longestList finds the list of two or more items with the most encoded
// bytes, as a JSON pointer.
func longestList(v any, path string) (string, []any) {
	bestPath, best, bestSize := "", []any(nil), -1
	consider := func(p string, l []any) {
		if len(l) < 2 {
			return
		}
		encoded, _ := json.Marshal(l)
		if len(encoded) > bestSize {
			bestPath, best, bestSize = p, l, len(encoded)
		}
	}
	switch x := v.(type) {
	case []any:
		consider(path, x)
		for i, child := range x {
			if p, l := longestList(child, path+"/"+strconv.Itoa(i)); l != nil {
				consider(p, l)
			}
		}
	case map[string]any:
		for k, child := range x {
			if p, l := longestList(child, path+"/"+escapePointer(k)); l != nil {
				consider(p, l)
			}
		}
	}
	return bestPath, best
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return segs
}

func at(v any, segs []string) any {
	for _, s := range segs {
		switch x := v.(type) {
		case map[string]any:
			v = x[s]
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

// replaceAt returns v with the value at segs replaced, copying the
// containers on the way instead of changing v.
func replaceAt(v any, segs []string, value any) any {
	if len(segs) == 0 {
		return value
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = child
		}
		out[segs[0]] = replaceAt(x[segs[0]], segs[1:], value)
		return out
	case []any:
		out := append([]any(nil), x...)
		i, _ := strconv.Atoi(segs[0])
		out[i] = replaceAt(x[i], segs[1:], value)
		return out
	}
	return v
}
