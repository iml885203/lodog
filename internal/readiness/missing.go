package readiness

import (
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// missingID is the L4 item for endpoints the API lacks.
const missingID = "missing_endpoints"

// maxRunEvidence caps the run lines one missing endpoint lists.
const maxRunEvidence = 3

// MissingEndpoint is an endpoint the legacy API does not have but an agent
// needs: a GET that reads back what a write did, or that fetches one record
// of a paged list by id. Path is a suggestion in the API's own style.
type MissingEndpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Writes are the operations whose result no GET reads back.
	Writes []string `json:"writes,omitempty"`
	// List is the paged list an agent scans instead, when there is one.
	List string `json:"list,omitempty"`
	// Tasks are the task tools whose steps make those writes (L2, L3).
	Tasks []string `json:"tasks,omitempty"`
	// Runs are what benchmark runs show the agent doing without it.
	Runs []Text `json:"runs,omitempty"`
}

// route is a path as the checks compare it: lower case, a trailing s
// dropped so /transactions and /transaction are one collection, and every
// path parameter written {}.
type route []string

func routeOf(path string) route {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	out := make(route, len(segs))
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			out[i] = "{}"
			continue
		}
		out[i] = strings.TrimSuffix(strings.ToLower(s), "s")
	}
	return out
}

func (r route) key() string { return strings.Join(r, "/") }

func (r route) byID() string { return r.key() + "/{}" }

func (r route) lastParam() int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == "{}" {
			return i
		}
	}
	return -1
}

// api is the legacy API's GETs and record paths, served or not: a GET the
// gateway hides still exists, so it is not missing.
type api struct {
	gets map[string]spec.Operation
	// records maps the route of a path ending in a parameter, any method, to
	// the path as the document writes it.
	records map[string]string
}

func indexAPI(doc *spec.Document) api {
	a := api{gets: map[string]spec.Operation{}, records: map[string]string{}}
	for _, op := range doc.Operations {
		r := routeOf(op.Path)
		if op.Method == "GET" {
			a.gets[r.key()] = op
		}
		if r[len(r)-1] == "{}" {
			a.records[r.key()] = op.Path
		}
	}
	return a
}

// gap is the GET that would read back what w writes, when the API has none:
// the suggested path and the paged list an agent scans instead. A write
// declared read only queries, so it has nothing to read back.
func (a api) gap(sig Signals, w spec.Operation) (path string, list *spec.Operation, missing bool) {
	if !sig.ScoreMutating(w) || w.AgentRisk == "read" {
		return "", nil, false
	}
	list, ok := a.readBack(w)
	if ok {
		return "", nil, false
	}
	return a.suggestPath(w, list), list, true
}

// readBack finds how an agent reads back what w wrote. ok is true when a GET
// returns that record: one on w's own path, on the record's path, or a list
// of the collection with no paging (one call shows every record). list is the
// paged list of the collection an agent would have to scan instead.
func (a api) readBack(w spec.Operation) (list *spec.Operation, ok bool) {
	r := routeOf(w.Path)
	if a.hasGET(r.key()) {
		return nil, true
	}
	return a.readBackRoute(r)
}

// readBackRoute reads r from its record up. A collection with no GET at all
// is not a resource but part of an action (…/moveAfter/{id},
// …/activate/{type}/{id}), so the record above it is read instead.
func (a api) readBackRoute(r route) (*spec.Operation, bool) {
	last := r.lastParam()
	if last < 0 {
		return a.readBackByBody(r)
	}
	for i := len(r); i > last; i-- {
		if a.hasGET(r[:i].key()) || a.hasGET(r[:i].byID()) {
			return nil, true
		}
	}
	if !a.hasGET(r[:last].key()) {
		return a.readBackRoute(r[:last])
	}
	return a.collection(r[:last])
}

// readBackByBody is a write whose path names no record: the id travels in
// the body, so a record GET under any collection above it reads it back.
func (a api) readBackByBody(r route) (*spec.Operation, bool) {
	for i := len(r); i >= 1; i-- {
		if a.hasGET(r[:i].byID()) {
			return nil, true
		}
		if l, ok := a.collection(r[:i]); l != nil || ok {
			return l, ok
		}
	}
	return nil, false
}

func (a api) hasGET(key string) bool {
	_, ok := a.gets[key]
	return ok
}

func (a api) collection(r route) (*spec.Operation, bool) {
	l, exists := a.gets[r.key()]
	if !exists {
		return nil, false
	}
	if len(PagingParams(l)) == 0 {
		return nil, true
	}
	return &l, false
}

// suggestPath is where the missing GET belongs: the record's own path when w
// names it, one record under the collection's list, one record of w's own
// path when the API already addresses records there (POST /x beside
// PATCH /x/{id} creates), else one record under the resource w names (a
// POST's last segment is usually the action, so its parent).
func (a api) suggestPath(w spec.Operation, list *spec.Operation) string {
	segs := strings.Split(strings.Trim(w.Path, "/"), "/")
	r := routeOf(w.Path)
	if last := r.lastParam(); last >= 0 {
		return "/" + strings.Join(segs[:last+1], "/")
	}
	if list != nil {
		return strings.TrimRight(list.Path, "/") + "/{id}"
	}
	if p, ok := a.records[r.byID()]; ok {
		return p
	}
	if w.Method == "POST" && len(segs) > 1 {
		segs = segs[:len(segs)-1]
	}
	return "/" + strings.Join(segs, "/") + "/{id}"
}

// endpointSet groups writes by the GET they lack, in the order first seen.
type endpointSet struct {
	byKey map[string]int
	list  []MissingEndpoint
}

func newEndpointSet(from []MissingEndpoint) *endpointSet {
	s := &endpointSet{byKey: map[string]int{}}
	for _, e := range from {
		s.at(e.Method, e.Path)
		s.list[len(s.list)-1] = cloneEndpoint(e)
	}
	return s
}

func (s *endpointSet) at(method, path string) *MissingEndpoint {
	key := method + " " + routeOf(path).key()
	if i, ok := s.byKey[key]; ok {
		return &s.list[i]
	}
	s.byKey[key] = len(s.list)
	s.list = append(s.list, MissingEndpoint{Method: method, Path: path})
	return &s.list[len(s.list)-1]
}

func (s *endpointSet) addWrite(path string, list *spec.Operation, w string) {
	e := s.at("GET", path)
	if !contains(e.Writes, w) {
		e.Writes = append(e.Writes, w)
	}
	if list != nil {
		e.List = name(*list)
	}
}

func cloneEndpoint(e MissingEndpoint) MissingEndpoint {
	e.Writes = append([]string(nil), e.Writes...)
	e.Tasks = append([]string(nil), e.Tasks...)
	e.Runs = append([]Text(nil), e.Runs...)
	return e
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// missingEndpoints lists, for the writes the agent is given, the GETs that
// would read their result back, grouped by suggested endpoint.
func missingEndpoints(v View, tools []spec.Operation) []MissingEndpoint {
	a := indexAPI(v.Doc)
	set := newEndpointSet(nil)
	for _, w := range tools {
		if path, list, missing := a.gap(v.Signals, w); missing {
			set.addWrite(path, list, name(w))
		}
	}
	for i := range set.list {
		set.list[i].Tasks = tasksWriting(v, set.list[i].Writes)
	}
	out := set.list
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Writes) > len(out[j].Writes) })
	return out
}

func tasksWriting(v View, writes []string) []string {
	want := map[string]bool{}
	for _, w := range writes {
		want[w] = true
	}
	var out []string
	for _, t := range v.Tasks {
		for _, op := range t.Task.Ops {
			if want[name(op)] {
				out = append(out, t.Task.Workflow.WorkflowID)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func missingItem(v View, tools []spec.Operation) (L4Item, bool) {
	missing := missingEndpoints(v, tools)
	if len(missing) == 0 {
		return L4Item{}, false
	}
	return missingL4Item(missing), true
}

// missingL4Item writes the item from its endpoints, so evidence added later
// (WithRuns) shows up in the same text.
func missingL4Item(missing []MissingEndpoint) L4Item {
	var writes, lists, seen int
	lines := make([]Text, len(missing))
	asks := make([]Text, len(missing))
	for i, e := range missing {
		writes += len(e.Writes)
		if e.List != "" {
			lists++
		}
		if len(e.Runs) > 0 {
			seen++
		}
		lines[i] = e.line(true)
		asks[i] = e.line(false)
	}
	detail := say("rd.l4.missing.detail", len(missing), writes, lists)
	measured := say("rd.l4.missing.measured")
	if seen > 0 {
		detail = say("rd.l4.missing.detail.runs", detail, seen)
		measured = say("rd.l4.missing.measured.runs")
	}
	return withFix(L4Item{
		ID: missingID, Name: say("rd.l4.missing.name"),
		Measured: measured, Detail: detail, Examples: examples(lines), Endpoints: missing,
	}, say("rd.l4.missing.lead"), "rd.l4.missing.fix", bullets(examples(asks)))
}

// line is the endpoint with its evidence; runs only for a person, since a
// coding agent needs what to build, not how the benchmark went.
func (e MissingEndpoint) line(runs bool) Text {
	var parts []Text
	switch n := len(e.Writes); {
	case n == 1:
		parts = append(parts, say("rd.missing.write", e.Writes[0]))
	case n > 1:
		parts = append(parts, say("rd.missing.writes", n, e.Writes[0]))
	}
	if e.List != "" {
		parts = append(parts, say("rd.missing.list", e.List))
	}
	if len(e.Tasks) > 0 {
		parts = append(parts, say("rd.missing.tasks", joined("rd.join", lits(e.Tasks))))
	}
	if runs && len(e.Runs) > 0 {
		parts = append(parts, say("rd.missing.runs", joined("rd.join.semi", e.Runs)))
	}
	return say("rd.missing.line", e.Method+" "+e.Path, joined("rd.join.semi", parts))
}
