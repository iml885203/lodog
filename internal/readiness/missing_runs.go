package readiness

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// minListScans is how often one attempt has to get an answer from the same
// list before it counts as scanning for a record; a retry after an error is
// not a scan.
const minListScans = 2

// RunCalls is what one benchmark run recorded at the legacy system. The
// caller converts the call records: the score never depends on the
// benchmark's packages, which read the answers.
type RunCalls struct {
	ID    string
	Calls []RunCall
}

// RunCall is one recorded call, as L4 reads it.
type RunCall struct {
	TaskID  string
	Attempt int
	Seq     int
	Method  string
	Path    string
	Status  int
	// DryRun is a write a read-only proxy answered without sending it: the
	// agent still saw a success.
	DryRun bool
}

// WithRuns returns l4 with what the runs show about endpoints the API lacks.
// A run only adds evidence to an endpoint the document confirms is missing;
// an agent that scans a list although a record GET exists made its own
// choice, which says nothing about the API. l4 itself is not changed.
func WithRuns(l4 *L4, v View, runs []RunCalls) *L4 {
	if l4 == nil || v.Doc == nil || len(runs) == 0 {
		return l4
	}
	var base []MissingEndpoint
	for _, it := range l4.Items {
		if it.ID == missingID {
			base = it.Endpoints
		}
	}
	set := newEndpointSet(base)
	ev := runEvidence{api: indexAPI(v.Doc), sig: v.Signals, ops: newMatcher(v.Doc), set: set}
	for _, r := range runs {
		for _, attempt := range attempts(r.Calls) {
			ev.attempt(r.ID, attempt)
		}
	}
	if !ev.added {
		return l4
	}
	out := *l4
	out.Items = withItem(l4.Items, missingL4Item(capRuns(set.list)))
	return &out
}

func withItem(items []L4Item, it L4Item) []L4Item {
	out := make([]L4Item, 0, len(items)+1)
	replaced := false
	for _, x := range items {
		if x.ID == it.ID {
			x, replaced = it, true
		}
		out = append(out, x)
	}
	if !replaced {
		out = append(out, it)
	}
	return out
}

func capRuns(list []MissingEndpoint) []MissingEndpoint {
	for i, e := range list {
		if len(e.Runs) > maxRunEvidence {
			list[i].Runs = slices.Concat(e.Runs[:maxRunEvidence], []string{fmt.Sprintf("…還有 %d 筆", len(e.Runs)-maxRunEvidence)})
		}
	}
	return list
}

type runEvidence struct {
	api   api
	sig   Signals
	ops   matcher
	set   *endpointSet
	added bool
}

// attempt reads one task attempt's calls in order: a write the API cannot
// read back, followed by no read at all or by a scan of the collection's
// list, and any list it called more than once.
func (ev *runEvidence) attempt(runID string, calls []RunCall) {
	label := fmt.Sprintf("%s %s-%d", runID, calls[0].TaskID, calls[0].Attempt)
	scans := map[string]*listScan{}
	for i, c := range calls {
		op, ok := ev.ops.match(c.Method, c.Path)
		if !ok {
			continue
		}
		if op.Method == "GET" {
			if !succeeded(c) {
				continue
			}
			if scans[name(op)] == nil {
				scans[name(op)] = &listScan{op: op}
			}
			scans[name(op)].calls++
			continue
		}
		if succeeded(c) {
			ev.write(label, op, calls[i+1:])
		}
	}
	ev.scans(label, scans)
}

func (ev *runEvidence) write(label string, op spec.Operation, after []RunCall) {
	path, list, missing := ev.api.gap(ev.sig, op)
	if !missing {
		return
	}
	ev.set.addWrite(path, list, name(op))
	e := ev.set.at("GET", path)
	read, scanned := false, false
	for _, c := range after {
		if !strings.EqualFold(c.Method, "GET") {
			continue
		}
		read = true
		if r, ok := ev.ops.match(c.Method, c.Path); ok && list != nil && name(r) == name(*list) {
			scanned = true
		}
	}
	switch {
	case scanned:
		ev.note(e, fmt.Sprintf("%s：%s 之後翻列表找回剛寫的那筆", label, name(op)))
	case !read:
		ev.note(e, fmt.Sprintf("%s：%s 之後沒有任何讀取，無法確認寫入結果", label, name(op)))
	}
}

// scans adds a list called repeatedly in one attempt, when the API has no
// record GET under it or beside it.
func (ev *runEvidence) scans(label string, scans map[string]*listScan) {
	names := make([]string, 0, len(scans))
	for n := range scans {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := scans[n]
		if s.calls < minListScans || len(PagingParams(s.op)) == 0 || ev.api.hasRecordGET(s.op) {
			continue
		}
		e := ev.set.at("GET", strings.TrimRight(s.op.Path, "/")+"/{id}")
		e.List = n
		ev.note(e, fmt.Sprintf("%s：同一題成功叫了 %d 次 %s", label, s.calls, n))
	}
}

type listScan struct {
	op    spec.Operation
	calls int
}

func (ev *runEvidence) note(e *MissingEndpoint, line string) {
	if !contains(e.Runs, line) {
		e.Runs = append(e.Runs, line)
		ev.added = true
	}
}

// hasRecordGET is true when one record of list can be read without it: a
// GET under it (/xs/{id}) or beside it (/xs/search next to /xs/{id}).
func (a api) hasRecordGET(list spec.Operation) bool {
	r := routeOf(list.Path)
	return a.hasGET(r.byID()) || len(r) > 1 && a.hasGET(r[:len(r)-1].byID())
}

func succeeded(c RunCall) bool {
	return c.DryRun || c.Status >= 200 && c.Status < 300
}

// attempts groups an agent's calls by task attempt, in call order. Calls the
// runner makes itself (_preflight, _state_check) are not the agent's.
func attempts(calls []RunCall) [][]RunCall {
	byRef := map[string][]RunCall{}
	var order []string
	for _, c := range calls {
		if c.TaskID == "" || strings.HasPrefix(c.TaskID, "_") {
			continue
		}
		key := fmt.Sprintf("%s-%d", c.TaskID, c.Attempt)
		if _, ok := byRef[key]; !ok {
			order = append(order, key)
		}
		byRef[key] = append(byRef[key], c)
	}
	out := make([][]RunCall, 0, len(order))
	for _, k := range order {
		a := byRef[k]
		sort.SliceStable(a, func(i, j int) bool { return a[i].Seq < a[j].Seq })
		out = append(out, a)
	}
	return out
}

// matcher finds the operation a recorded call hit. Where a literal segment
// and a parameter both fit (/content/list and /content/{id}), the template
// with more literal segments wins, as routers do.
type matcher struct {
	ops []spec.Operation
}

func newMatcher(doc *spec.Document) matcher {
	return matcher{ops: doc.Operations}
}

func (m matcher) match(method, path string) (spec.Operation, bool) {
	got := strings.Split(strings.Trim(path, "/"), "/")
	best, bestLiterals := spec.Operation{}, -1
	for _, op := range m.ops {
		if !strings.EqualFold(op.Method, method) {
			continue
		}
		if n, ok := literalMatch(op.Path, got); ok && n > bestLiterals {
			best, bestLiterals = op, n
		}
	}
	return best, bestLiterals >= 0
}

func literalMatch(template string, got []string) (int, bool) {
	want := strings.Split(strings.Trim(template, "/"), "/")
	if len(want) != len(got) {
		return 0, false
	}
	literals := 0
	for i, w := range want {
		if strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}") {
			continue
		}
		if !strings.EqualFold(w, got[i]) {
			return 0, false
		}
		literals++
	}
	return literals, true
}
