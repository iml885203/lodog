package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/spec"
)

const shopDoc = `{"openapi":"3.0.4","info":{"title":"shop","version":"1"},"paths":{
 "/players":{"get":{"parameters":[
   {"name":"username","in":"query","schema":{"type":"string"}},
   {"name":"pageSize","in":"query","required":true,"schema":{"type":"integer"}}],
   "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","properties":{
     "data":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"},"name":{"type":"string"}}}}}}}}}}}},
 "/players/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "responses":{"200":{"description":"ok"}}}},
 "/players/{id}/bonus":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"amount":{"type":"number"},"note":{"type":"string"}}}}}}}},
 "/twice/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}},{"name":"id","in":"query","schema":{"type":"integer"}}]}}
}}`

const arazzoShop = `arazzo: 1.0.1
info: {title: shop tools, version: "1", description: Start with find_player.}
sourceDescriptions: [{name: shop, url: openapi.json, type: openapi}]
workflows:
  - workflowId: find_player
    summary: Find a player by username and read the first match.
    inputs:
      type: object
      properties:
        username: {type: string}
        pageSize: {type: integer, default: 10, maximum: 50}
      required: [username]
    steps:
      - stepId: search
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players/get'
        parameters:
          - {name: username, in: query, value: $inputs.username}
          - {name: pageSize, in: query, value: $inputs.pageSize}
        outputs: {id: '$response.body#/data/0/id'}
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}/get'
        parameters:
          - {name: id, in: path, value: $steps.search.outputs.id}
        outputs: {player: $response.body, status: $statusCode}
    outputs: {player: $steps.read.outputs.player}
    x-agent: {risk: read, next: [give_bonus]}
  - workflowId: give_bonus
    summary: Give a player a bonus.
    inputs:
      type: object
      properties:
        id: {type: integer}
        amount: {type: number, minimum: 1}
        note: {type: string}
      required: [id, amount]
    steps:
      - stepId: give
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}~1bonus/post'
        parameters: [{name: id, value: $inputs.id}]
        requestBody: {payload: {amount: $inputs.amount, note: $inputs.note}}
    x-agent: {risk: money}
`

func shop(t *testing.T) *spec.Document {
	t.Helper()
	doc, err := spec.Parse([]byte(shopDoc))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func bind(t *testing.T, workflows string) ([]Task, error) {
	t.Helper()
	d, err := Parse([]byte(workflows))
	if err != nil {
		t.Fatal(err)
	}
	return Bind(d, shop(t))
}

func shopTasks(t *testing.T) map[string]Task {
	t.Helper()
	tasks, err := bind(t, arazzoShop)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Task{}
	for _, task := range tasks {
		out[task.Workflow.WorkflowID] = task
	}
	return out
}

func TestAWorkflowBindsToTheOperationsItsStepsName(t *testing.T) {
	tasks := shopTasks(t)
	find := tasks["find_player"]
	if len(find.Ops) != 2 || find.Ops[0].Path != "/players" || find.Ops[1].Path != "/players/{id}" || find.Ops[1].Method != "GET" {
		t.Fatalf("ops = %+v", find.Ops)
	}
	if got := OperationPath("shop", find.Ops[1]); got != find.Workflow.Steps[1].OperationPath {
		t.Errorf("OperationPath = %s, want the document's own %s", got, find.Workflow.Steps[1].OperationPath)
	}
}

// Each broken document must be refused with a message that names the problem,
// so a drafted file is fixed rather than served half-working.
func TestABrokenWorkflowIsRefusedWithTheReason(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"unknown operation":       {"~1players~1{id}/get", "~1nobody/get", "has no GET /nobody"},
		"missing required param":  {"          - {name: pageSize, in: query, value: $inputs.pageSize}\n", "", `requires parameter "pageSize"`},
		"unknown param":           {"{name: username, in: query,", "{name: nickname, in: query,", "has no such parameter"},
		"undeclared input":        {"value: $inputs.username}", "value: $inputs.nickname}", `no input "nickname"`},
		"unused input":            {"        note: {type: string}\n", "        note: {type: string}\n        spare: {type: string}\n", `input "spare" is never used`},
		"pointer not in response": {"'$response.body#/data/0/id'", "'$response.body#/data/0/uid'", `no "uid"`},
		"output of a later step":  {"value: $steps.search.outputs.id", "value: $steps.read.outputs.player", `no earlier step "read"`},
		"missing workflow output": {"player: $steps.read.outputs.player}", "player: $steps.read.outputs.nothing}", `no step "read" with output "nothing"`},
		"unknown next":            {"next: [give_bonus]", "next: [give_gift]", `"give_gift", which is not a workflow`},
		"uncapped page size":      {"pageSize: {type: integer, default: 10, maximum: 50}", "pageSize: {type: integer, default: 10}", "needs a maximum"},
		"duplicate workflow":      {"workflowId: give_bonus", "workflowId: find_player", "defined twice"},
		"unsupported feature":     {"        outputs: {player: $response.body, status: $statusCode}\n", "        onFailure: [{name: retry, type: retry}]\n        outputs: {player: $response.body, status: $statusCode}\n", "not supported"},
		"embedded expression":     {"note: $inputs.note}", "note: 'gift {$inputs.note}'}", "embeds a runtime expression"},
		"no instructions":         {"description: Start with find_player.", "description: ''", "info.description is required"},
		"body field not in body":  {"note: $inputs.note}", "memo: $inputs.note}", `no body field "memo"`},
		"bad tool name":           {"workflowId: give_bonus", "workflowId: give-bonus", "workflowId must be"},
		"wrong source":            {"'{$sourceDescriptions.shop.url}#/paths/~1players/get'", "'{$sourceDescriptions.other.url}#/paths/~1players/get'", "must start with {$sourceDescriptions.shop.url}#"},
		"no summary":              {"summary: Give a player a bonus.", "summary: ''", "summary is required"},
		"required input missing":  {"required: [id, amount]", "required: [id, amount, reason]", `required input "reason" is not a property`},
		"version":                 {"arazzo: 1.0.1", "arazzo: 2.0.0", "lodog reads Arazzo 1.0.x"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(arazzoShop, c.from) {
				t.Fatalf("fixture has no %q", c.from)
			}
			_, err := bind(t, strings.Replace(arazzoShop, c.from, c.to, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}

func TestAParameterNameUsedTwiceNeedsItsPlace(t *testing.T) {
	twice := `arazzo: 1.0.1
info: {title: t, version: "1", description: d}
sourceDescriptions: [{name: shop}]
workflows:
  - workflowId: twice
    summary: s
    inputs: {type: object, properties: {id: {type: integer}}}
    steps:
      - stepId: s
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1twice~1{id}/get'
        parameters: [{name: id, value: $inputs.id}]
`
	if _, err := bind(t, twice); err == nil || !strings.Contains(err.Error(), "say which with in") {
		t.Fatalf("err = %v", err)
	}
	tasks, err := bind(t, strings.Replace(twice, "{name: id, value", "{name: id, in: path, value", 1))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := tasks[0].Preview(0, map[string]any{"id": 7.0})
	if !ok || c.Args["path.id"] != 7.0 {
		t.Fatalf("args = %v: a clashing name travels under its place-prefixed argument", c.Args)
	}
}

func TestPrepareChecksArgumentsAndFillsDefaults(t *testing.T) {
	find := shopTasks(t)["find_player"]
	got, err := find.Prepare(map[string]any{"username": "bob"}, time.Now())
	if err != nil || got["pageSize"] != 10 {
		t.Fatalf("got %v, %v: the default page size fills in", got, err)
	}
	for name, args := range map[string]map[string]any{
		"unknown argument":  {"username": "bob", "nickname": "b"},
		"missing required":  {"pageSize": 5.0},
		"above the maximum": {"username": "bob", "pageSize": 500.0},
		"wrong type":        {"username": 7.0},
		"not whole":         {"username": "bob", "pageSize": 2.5},
	} {
		var inputErr *InputError
		if _, err := find.Prepare(args, time.Now()); !errors.As(err, &inputErr) {
			t.Errorf("%s: err = %v, want an InputError the agent can act on", name, err)
		}
	}
}

func TestARelativeDefaultBoundsADateRange(t *testing.T) {
	task := Task{Workflow: Workflow{WorkflowID: "w", Inputs: map[string]any{"type": "object", "properties": map[string]any{
		"from":  map[string]any{"type": "string", "format": "date-time", "x-default": "now-30d"},
		"to":    map[string]any{"type": "string", "format": "date-time", "x-default": "now"},
		"day":   map[string]any{"type": "string", "format": "date", "x-default": "now+12h"},
		"other": map[string]any{"type": "string"},
	}}}}
	now := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)
	got, err := task.Prepare(nil, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"from": "2026-09-15T20:00:00Z", "to": "2026-10-15T20:00:00Z", "day": "2026-10-16"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, set := got["other"]; set {
		t.Error("an input with no default was invented")
	}
	if given, _ := task.Prepare(map[string]any{"from": "2026-01-01T00:00:00Z"}, now); given["from"] != "2026-01-01T00:00:00Z" {
		t.Error("a given value was replaced by the default")
	}
	desc := task.InputSchema()["properties"].(map[string]any)["from"].(map[string]any)
	if _, leaked := desc["x-default"]; leaked || !strings.Contains(desc["description"].(string), "30 days before the call") {
		t.Errorf("schema = %v: the relative default is told in words, not as an extension", desc)
	}
	for _, bad := range []string{"yesterday", "now-3w", "now-d"} {
		if _, err := parseRelative(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

type scripted struct {
	replies map[string]Reply
	calls   []Call
}

func (s *scripted) call(_ context.Context, c Call) (Reply, error) {
	s.calls = append(s.calls, c)
	return s.replies[c.StepID], nil
}

func body(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestALaterStepGetsWhatAnEarlierStepReturned(t *testing.T) {
	find := shopTasks(t)["find_player"]
	s := &scripted{replies: map[string]Reply{
		"search": {Status: 200, Body: body(t, map[string]any{"data": []any{map[string]any{"id": 42, "name": "bob"}}})},
		"read":   {Status: 200, Body: body(t, map[string]any{"id": 42, "vip": 3})},
	}}
	inputs, _ := find.Prepare(map[string]any{"username": "bob"}, time.Now())
	res, err := find.Run(context.Background(), inputs, s.call)
	if err != nil || res.Failure != nil {
		t.Fatalf("%v %+v", err, res.Failure)
	}
	if len(s.calls) != 2 || s.calls[1].Args["id"] != 42.0 || s.calls[0].Args["pageSize"] != 10 {
		t.Fatalf("calls = %+v", s.calls)
	}
	player := res.Data.(map[string]any)["player"].(map[string]any)
	if player["vip"] != 3.0 {
		t.Fatalf("data = %v: the workflow returns its declared outputs", res.Data)
	}
}

func TestAnEmptySearchStopsTheRunWithWhatIsMissing(t *testing.T) {
	find := shopTasks(t)["find_player"]
	s := &scripted{replies: map[string]Reply{"search": {Status: 200, Body: []byte(`{"data":[]}`)}}}
	inputs, _ := find.Prepare(map[string]any{"username": "eve"}, time.Now())
	res, err := find.Run(context.Background(), inputs, s.call)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 {
		t.Fatalf("%d calls: the step that lacks its id must not run", len(s.calls))
	}
	if res.Failure == nil || res.Failure.StepID != "read" || !strings.Contains(res.Failure.Missing, "$steps.search.outputs.id") {
		t.Fatalf("failure = %+v", res.Failure)
	}
}

func TestALegacyRefusalStopsTheRun(t *testing.T) {
	find := shopTasks(t)["find_player"]
	s := &scripted{replies: map[string]Reply{"search": {Status: 400, Body: []byte(`{"error":"bad"}`)}}}
	inputs, _ := find.Prepare(map[string]any{"username": "bob"}, time.Now())
	res, _ := find.Run(context.Background(), inputs, s.call)
	if len(s.calls) != 1 || res.Failure == nil || res.Failure.Status != 400 || res.Failure.Body.(map[string]any)["error"] != "bad" {
		t.Fatalf("calls %d, failure %+v", len(s.calls), res.Failure)
	}
}

func TestATransportErrorIsReturnedAsIs(t *testing.T) {
	find := shopTasks(t)["find_player"]
	boom := errors.New("connection reset")
	inputs, _ := find.Prepare(map[string]any{"username": "bob"}, time.Now())
	_, err := find.Run(context.Background(), inputs, func(context.Context, Call) (Reply, error) { return Reply{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnAbsentOptionalValueIsLeftOutOfTheBody(t *testing.T) {
	give := shopTasks(t)["give_bonus"]
	s := &scripted{replies: map[string]Reply{"give": {Status: 200}}}
	inputs, _ := give.Prepare(map[string]any{"id": 7.0, "amount": 100.0}, time.Now())
	res, err := give.Run(context.Background(), inputs, s.call)
	if err != nil || res.Failure != nil {
		t.Fatalf("%v %+v", err, res.Failure)
	}
	sent := s.calls[0]
	b := sent.Body.(map[string]any)
	if _, has := b["note"]; has || b["amount"] != 100.0 || !sent.HasBody || sent.Args["id"] != 7.0 {
		t.Fatalf("call = %+v", sent)
	}
	if res.Data != nil {
		t.Errorf("data = %v: an empty answer stays empty", res.Data)
	}
}

func TestPreviewShowsAStepAsFarAsTheInputsSettleIt(t *testing.T) {
	tasks := shopTasks(t)
	if c, ok := tasks["give_bonus"].Preview(0, map[string]any{"id": 7.0, "amount": 5.0}); !ok || c.Args["id"] != 7.0 {
		t.Fatalf("preview = %+v %v", c, ok)
	}
	if _, ok := tasks["find_player"].Preview(1, map[string]any{"username": "bob"}); ok {
		t.Fatal("a step that needs an earlier step's output cannot be previewed")
	}
}
