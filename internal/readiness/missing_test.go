package readiness

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// paths builds an OpenAPI document from "METHOD /path" lines; a trailing
// " paged" gives the operation page and pageSize, a trailing " read" declares
// it read.
func paths(t *testing.T, ops ...string) *spec.Document {
	t.Helper()
	byPath := map[string][]string{}
	var order []string
	for _, o := range ops {
		method, rest, _ := strings.Cut(o, " ")
		path, flag, _ := strings.Cut(rest, " ")
		var params []string
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, "{") {
				params = append(params, fmt.Sprintf(`{"name":%q,"in":"path","required":true,"schema":{"type":"string"}}`, strings.Trim(seg, "{}")))
			}
		}
		if flag == "paged" {
			params = append(params, `{"name":"page","in":"query","schema":{"type":"integer"}}`, `{"name":"pageSize","in":"query","schema":{"type":"integer"}}`)
		}
		ext := ""
		if flag == "read" {
			ext = `,"x-agent":{"risk":"read"}`
		}
		if _, ok := byPath[path]; !ok {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], fmt.Sprintf(`%q:{"parameters":[%s],"responses":{"200":{"description":"ok"}}%s}`,
			strings.ToLower(method), strings.Join(params, ","), ext))
	}
	var b strings.Builder
	b.WriteString(`{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{`)
	for i, p := range order {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q:{%s}", p, strings.Join(byPath[p], ","))
	}
	b.WriteString("}}")
	return parse(t, b.String())
}

func missingOf(t *testing.T, doc *spec.Document) []MissingEndpoint {
	t.Helper()
	v := View{Doc: doc}
	tools, _ := visible(v)
	return missingEndpoints(v, tools)
}

func TestAWriteNoGETReadsBackAsksForTheRecordGETOfItsCollection(t *testing.T) {
	r := Score(View{Doc: paths(t,
		"POST /payment/transaction/withdraw/reject",
		"POST /payment/transaction/remark/save",
		"GET /payment/transactions paged",
	)})
	it, ok := item(r.L4, missingID)
	if !ok {
		t.Fatal("two writes nothing reads back, but no missing-endpoint item")
	}
	if it.Counted() || it.Gain != 0 || it.Check != "" {
		t.Errorf("no static check measures this, so it must not claim points: %+v", it)
	}
	if len(it.Endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want one GET for the transaction collection", it.Endpoints)
	}
	e := it.Endpoints[0]
	if e.Method+" "+e.Path != "GET /payment/transactions/{id}" || len(e.Writes) != 2 || e.List != "GET /payment/transactions" {
		t.Errorf("endpoint = %+v", e)
	}
	if !strings.Contains(it.Instruction.String(), "GET /payment/transactions/{id}") || strings.Contains(it.Instruction.String(), "run：") {
		t.Errorf("instruction must name the endpoint and carry no run evidence: %s", it.Instruction.String())
	}
}

func TestAWriteSomeGETReadsBackIsNotListed(t *testing.T) {
	for name, ops := range map[string][]string{
		"its record":                       {"POST /orders/{id}/approve", "GET /orders/{id}"},
		"its own path":                     {"PUT /settings", "GET /settings"},
		"an unpaged list above it":         {"PATCH /settings/enabled", "GET /settings"},
		"a record under the collection":    {"POST /orders", "GET /orders/{id}"},
		"a record when the id is in body":  {"POST /orders/approve", "GET /orders/{id}"},
		"the record above an action path":  {"POST /users/{id}/credentials/{cid}/moveAfter/{next}", "GET /users/{id}/credentials"},
		"a sub-resource of the record":     {"POST /users/{id}/roles", "GET /users/{id}/roles"},
		"a write declared read (a search)": {"POST /reports/run read"},
	} {
		if got := missingOf(t, paths(t, ops...)); len(got) != 0 {
			t.Errorf("%s: %v is listed as missing: %+v", name, ops, got)
		}
	}
}

func TestTheSuggestedGETFollowsTheAPIsOwnPaths(t *testing.T) {
	for want, ops := range map[string][]string{
		"GET /orders/{orderId}":             {"PATCH /orders/{orderId}"},
		"GET /orders/{id}":                  {"POST /orders/{id}/approve"},
		"GET /transfer/{id}":                {"POST /transfer/manual"},
		"GET /payment-method/{id}":          {"PATCH /payment-method"},
		"GET /units/{unitId}":               {"POST /units", "PATCH /units/{unitId}"},
		"GET /txs/{id}":                     {"POST /tx/approve", "GET /txs paged"},
		"GET /groups/{id}/members/{userId}": {"PUT /groups/{id}/members/{userId}", "GET /groups/{id}/members paged"},
		"GET /admin/sessions/{session}":     {"DELETE /admin/sessions/{session}"},
		"GET /campaign/vouchers/{id}":       {"DELETE /campaign/vouchers"},
		"GET /reports/{id}":                 {"POST /reports/run"},
	} {
		got := missingOf(t, paths(t, ops...))
		if len(got) != 1 || got[0].Method+" "+got[0].Path != want {
			t.Errorf("%v: got %+v, want %s", ops, got, want)
		}
	}
}

func TestOnePagedCollectionGroupsEveryWriteToIt(t *testing.T) {
	got := missingOf(t, paths(t,
		"POST /tx/deposit/complete", "POST /tx/withdraw/reject", "POST /transfer/manual", "GET /txs paged",
	))
	if len(got) != 2 || got[0].Path != "/txs/{id}" || len(got[0].Writes) != 2 || got[1].Path != "/transfer/{id}" {
		t.Fatalf("got %+v, want the collection with two writes first, then the transfer", got)
	}
}

func TestTheTaskToolsMakingTheWriteAreNamed(t *testing.T) {
	doc := paths(t, "POST /tx/reject", "GET /txs paged", "GET /players/{id}")
	op := func(m, p string) spec.Operation {
		for _, o := range doc.Operations {
			if o.Method == m && o.Path == p {
				return o
			}
		}
		t.Fatalf("no %s %s", m, p)
		return spec.Operation{}
	}
	task := func(id string, ops ...spec.Operation) serving.TaskTool {
		return serving.TaskTool{Task: workflow.Task{Workflow: workflow.Workflow{WorkflowID: id}, Ops: ops}}
	}
	v := View{Doc: doc, Tasks: []serving.TaskTool{
		task("reject_withdrawal", op("GET", "/txs"), op("POST", "/tx/reject")),
		task("read_player", op("GET", "/players/{id}")),
	}}
	got := missingEndpoints(v, []spec.Operation{op("POST", "/tx/reject")})
	if len(got) != 1 || strings.Join(got[0].Tasks, ",") != "reject_withdrawal" {
		t.Fatalf("got %+v, want the write's task tool only", got)
	}
}
