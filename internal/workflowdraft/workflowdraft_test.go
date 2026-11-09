package workflowdraft

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

const doc = `{"openapi":"3.0.4","info":{"title":"shop","version":"1"},"paths":{
 "/players":{"get":{"summary":"Search players","parameters":[
   {"name":"username","in":"query","description":"Login name","schema":{"type":"string"}},
   {"name":"pageSize","in":"query","required":true,"schema":{"type":"integer"}}],
   "x-agent":{"whenToUse":"You have a username."},
   "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","properties":{
     "data":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"},"name":{"type":"string"}}}}}}}}}}}},
 "/players/{id}":{"get":{"summary":"Read a player","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}},
 "/players/{id}/bonus":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"amount":{"type":"number"}}}}}}}},
 "/upload":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"note":{"type":"string"}}}}}}}},
 "/players/{id}/avatar":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "requestBody":{"content":{"application/octet-stream":{"schema":{"type":"string","format":"binary"}}}}}}
}}`

const goodFind = `{"name": "find_player", "summary": "Find a player.", "whenToUse": "You have a username.", "risk": "read", "confident": true,
  "inputs": {"username": {"type": "string", "description": "Login name", "required": true},
             "pageSize": {"type": "integer", "default": 10, "maximum": 50}},
  "steps": [{"id": "search", "operation": "GET /players", "parameters": {"username": "$inputs.username", "pageSize": "$inputs.pageSize"},
             "outputs": {"id": "$response.body#/data/0/id"}},
            {"id": "read", "operation": "GET /players/{id}", "parameters": {"id": "$steps.search.outputs.id"}}],
  "next": ["give_bonus"]}`

const goodBonus = `{"name": "give_bonus", "summary": "Give a bonus.", "risk": "money", "confident": false,
  "inputs": {"id": {"type": "integer", "required": true}, "amount": {"type": "number", "required": true}},
  "steps": [{"id": "give", "operation": "POST /players/{id}/bonus", "parameters": {"id": "$inputs.id"}, "body": {"amount": "$inputs.amount"}}]}`

// badBonus calls an operation the document does not have.
var badBonus = strings.Replace(goodBonus, "POST /players/{id}/bonus", "POST /players/{id}/gift", 1)

type asker struct {
	answers []string
	prompts []string
}

func (a *asker) Ask(_ context.Context, prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	if len(a.answers) == 0 {
		return "", errors.New("asked too often")
	}
	next := a.answers[0]
	a.answers = a.answers[1:]
	return next, nil
}

func shop(t *testing.T) *spec.Document {
	t.Helper()
	d, err := spec.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func answerWith(tools ...string) string {
	return "Here you go:\n```json\n{\"instructions\": \"Start with find_player.\", \"tools\": [" + strings.Join(tools, ",") + "]}\n```"
}

var opts = Options{Title: "shop task tools", Source: "shop", URL: "openapi.json"}

func TestADraftIsAnArazzoDocumentThatBinds(t *testing.T) {
	a := &asker{answers: []string{answerWith(goodFind, goodBonus)}}
	d, err := Run(context.Background(), shop(t), a, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.prompts) != 1 || len(d.Dropped) != 0 {
		t.Fatalf("prompts %d, dropped %v", len(a.prompts), d.Dropped)
	}
	data, err := d.Document.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := workflow.Bind(back, shop(t))
	if err != nil {
		t.Fatalf("the written draft does not load: %v\n%s", err, data)
	}
	if len(tasks) != 2 || back.Info.Description != "Start with find_player." {
		t.Fatalf("tasks %d, instructions %q", len(tasks), back.Info.Description)
	}
	step := back.Workflows[0].Steps[0]
	if step.OperationPath != "{$sourceDescriptions.shop.url}#/paths/~1players/get" || step.Parameters[0].In != "query" {
		t.Fatalf("step = %+v: lodog writes the reference and parameter places, not the model", step)
	}
	if len(d.Review) != 1 || d.Review[0] != "give_bonus" || !back.Workflows[1].Agent.NeedsReview {
		t.Fatalf("review = %v: a tool the model is unsure of is marked for review", d.Review)
	}
}

func TestATaskThatDoesNotBindIsSentBackOnceWithTheErrors(t *testing.T) {
	a := &asker{answers: []string{answerWith(goodFind, badBonus), `{"tools": [` + goodBonus + `]}`}}
	d, err := Run(context.Background(), shop(t), a, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.prompts) != 2 || !strings.Contains(a.prompts[1], "give_bonus: ") || !strings.Contains(a.prompts[1], "POST /players/{id}/gift") {
		t.Fatalf("retry prompt does not name the broken tool and why:\n%s", a.prompts[len(a.prompts)-1])
	}
	if len(d.Document.Workflows) != 2 || len(d.Dropped) != 0 || d.Document.Info.Description != "Start with find_player." {
		t.Fatalf("workflows %d, dropped %v", len(d.Document.Workflows), d.Dropped)
	}
}

func TestATaskStillBrokenAfterTheRetryIsDroppedAndNoLongerNamed(t *testing.T) {
	a := &asker{answers: []string{answerWith(goodFind, badBonus), `{"tools": [` + badBonus + `]}`}}
	d, err := Run(context.Background(), shop(t), a, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Document.Workflows) != 1 || len(d.Dropped) != 1 || !strings.HasPrefix(d.Dropped[0], "give_bonus: ") {
		t.Fatalf("workflows %d, dropped %v", len(d.Document.Workflows), d.Dropped)
	}
	if next := d.Document.Workflows[0].Agent.Next; len(next) != 0 {
		t.Fatalf("next = %v: a dropped tool is still suggested", next)
	}
}

func TestAnAnswerThatIsNotTheShapeAskedForIsAnError(t *testing.T) {
	for _, text := range []string{"I cannot help with that.", `{"tools": []}`, `{"tools": [`} {
		a := &asker{answers: []string{text}}
		if _, err := Run(context.Background(), shop(t), a, opts); !errors.Is(err, ErrMalformedAnswer) {
			t.Errorf("%q: err = %v", text, err)
		}
	}
	noMap := `{"instructions": "", "tools": [` + goodFind + `]}`
	a := &asker{answers: []string{noMap, noMap}}
	if _, err := Run(context.Background(), shop(t), a, opts); !errors.Is(err, ErrMalformedAnswer) || len(a.prompts) != 2 {
		t.Errorf("empty instructions after a retry: %v after %d prompts", err, len(a.prompts))
	}
}

func TestThePromptShowsWhatTheOverlaySaysAndTheResultShape(t *testing.T) {
	p := prompt(shop(t))
	for _, want := range []string{
		"GET /players | Search players | You have a username. | username:string (Login name), pageSize*:integer |  | {data:[{id,name}]}",
		"POST /players/{id}/bonus |", "| amount |",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "/avatar") {
		t.Error("an operation the gateway does not serve is offered to the model")
	}
	if !strings.Contains(p, "POST /upload |") {
		t.Error("an operation the gateway serves (multipart) is kept from the model")
	}
}

// avatar names an operation that exists but that the gateway does not serve.
const avatar = `{"name": "set_avatar", "summary": "Set an avatar.", "risk": "write", "confident": true,
  "inputs": {"id": {"type": "integer", "required": true}},
  "steps": [{"id": "put", "operation": "POST /players/{id}/avatar", "parameters": {"id": "$inputs.id"}}]}`

func TestATaskUsingAnOperationTheGatewayDoesNotServeIsRetriedThenDropped(t *testing.T) {
	a := &asker{answers: []string{answerWith(goodFind, goodBonus, avatar), `{"tools": [` + avatar + `]}`}}
	d, err := Run(context.Background(), shop(t), a, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.prompts) != 2 || !strings.Contains(a.prompts[1], "set_avatar: ") || !strings.Contains(a.prompts[1], "not forwarded") {
		t.Fatalf("retry prompt does not say why set_avatar cannot be served:\n%s", a.prompts[len(a.prompts)-1])
	}
	if len(d.Document.Workflows) != 2 || len(d.Dropped) != 1 || !strings.HasPrefix(d.Dropped[0], "set_avatar: ") {
		t.Fatalf("workflows %d, dropped %v", len(d.Document.Workflows), d.Dropped)
	}
}

func TestThePromptGivesTheModelTheCodesAndTheirNames(t *testing.T) {
	d, err := spec.Parse([]byte(`{"openapi":"3.0.4","info":{"title":"t","version":"1"},"components":{"schemas":{
 "Channel":{"type":"integer","enum":[1,2],"x-enumNames":["Email","LiveChat"]}}},"paths":{
 "/logs":{"post":{"parameters":[{"name":"kind","in":"query","schema":{"$ref":"#/components/schemas/Channel"}}],
  "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{
   "channel":{"$ref":"#/components/schemas/Channel"},"mode":{"type":"string","enum":["a","b"]}}}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := prompt(d)
	for _, want := range []string{"channel{1 = Email, 2 = LiveChat}", "mode{a, b}", "kind:integer {1 = Email, 2 = LiveChat}"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestThePromptMarksTheBodyFieldsTheDocumentRequires(t *testing.T) {
	d, err := spec.Parse([]byte(`{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{
 "/invoices":{"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object","required":["memo","currency"],
  "properties":{"memo":{"type":"string"},"currency":{"type":"string","enum":["USD","EUR"]},"note":{"type":"string"}}}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := prompt(d)
	if !strings.Contains(p, "currency*{USD, EUR},memo*,note") {
		t.Errorf("required body fields are not starred:\n%s", p)
	}
	if !strings.Contains(p, "A body field marked * is required") {
		t.Error("the prompt does not tell the model to mark inputs that fill a starred field as required")
	}
}
