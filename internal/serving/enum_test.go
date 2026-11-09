package serving

import (
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// A help desk API that is not Backoffice: one code field that points at a shared
// schema naming its values (x-enumNames), one inline enum with no names, and a
// query parameter with an enum, in both document formats lodog reads.
const helpdeskOpenAPI3 = `{"openapi":"3.0.3","info":{"title":"helpdesk","version":"1"},"paths":{
 "/tickets":{"post":{"requestBody":{"required":true,"content":{"application/json":{"schema":{"$ref":"#/components/schemas/NewTicket"}}}},
   "responses":{"200":{"description":"ok"}}},
  "get":{"parameters":[{"name":"state","in":"query","description":"Ticket state","schema":{"type":"integer","enum":[0,1],"x-enumNames":["Open","Closed"]}}],
   "responses":{"200":{"description":"ok"}}}}},
 "components":{"schemas":{
  "Channel":{"type":"integer","enum":[1,2,3],"x-enumNames":["Email","LiveChat","PhoneCall"]},
  "NewTicket":{"type":"object","properties":{"channel":{"$ref":"#/components/schemas/Channel"},
   "priority":{"type":"string","enum":["low","high"]},"note":{"type":"string"}}}}}}`

const helpdeskSwagger2 = `{"swagger":"2.0","info":{"title":"helpdesk","version":"1"},"paths":{
 "/tickets":{"post":{"consumes":["application/json"],"parameters":[{"name":"body","in":"body","required":true,"schema":{"$ref":"#/definitions/NewTicket"}}],
   "responses":{"200":{"description":"ok"}}},
  "get":{"parameters":[{"name":"state","in":"query","description":"Ticket state","type":"integer","enum":[0,1]}],"responses":{"200":{"description":"ok"}}}}},
 "definitions":{
  "Channel":{"type":"integer","enum":[1,2,3],"x-enumNames":["Email","LiveChat","PhoneCall"]},
  "NewTicket":{"type":"object","properties":{"channel":{"$ref":"#/definitions/Channel"},
   "priority":{"type":"string","enum":["low","high"]},"note":{"type":"string"}}}}}`

// The input says nothing of the codes, the way a drafted workflow often does.
const helpdeskWorkflows = `arazzo: 1.0.1
info: {title: helpdesk task tools, version: "1", description: Open tickets.}
sourceDescriptions: [{name: helpdesk, url: openapi.json, type: openapi}]
workflows:
- workflowId: open_ticket
  summary: Open a ticket
  inputs:
    type: object
    required: [channel]
    properties:
      channel: {type: integer, description: How the customer reached us}
      priority: {type: string}
      note: {type: string}
  steps:
  - stepId: open
    operationPath: "{$sourceDescriptions.helpdesk.url}#/paths/~1tickets/post"
    requestBody:
      contentType: application/json
      payload: {channel: $inputs.channel, priority: $inputs.priority, note: $inputs.note}
`

func TestEveryEnumFieldTellsTheAgentItsValuesAndTheirNames(t *testing.T) {
	for name, source := range map[string]string{"openapi3": helpdeskOpenAPI3, "swagger2": helpdeskSwagger2} {
		t.Run(name, func(t *testing.T) {
			doc, err := spec.Parse([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			wf, err := workflow.Parse([]byte(helpdeskWorkflows))
			if err != nil {
				t.Fatal(err)
			}
			checkRawTools(t, name, doc)
			checkTaskTools(t, doc, wf)
		})
	}
}

// One tool per operation (L0 and L1): the body and the query parameter.
func checkRawTools(t *testing.T, format string, doc *spec.Document) {
	t.Helper()
	raw, _ := RawTools(doc)
	byMethod := map[string]RawTool{}
	for _, r := range raw {
		byMethod[r.Op.Method] = r
	}
	body := byMethod["POST"].Tool.InputSchema["properties"].(map[string]any)[BodyArg].(map[string]any)["properties"].(map[string]any)
	channel := body["channel"].(map[string]any)
	if got := channel["description"]; got != "Allowed values: 1 = Email, 2 = LiveChat, 3 = PhoneCall." {
		t.Errorf("channel description = %q", got)
	}
	if len(channel["enum"].([]any)) != 3 {
		t.Errorf("channel enum = %v: the JSON schema keeps the values", channel["enum"])
	}
	if got := body["priority"].(map[string]any)["description"]; got != "Allowed values: low, high." {
		t.Errorf("priority description = %q: an enum without names lists its values", got)
	}
	if _, has := body["note"].(map[string]any)["description"]; has {
		t.Error("a field without an enum got an enum sentence")
	}
	state := byMethod["GET"].Tool.InputSchema["properties"].(map[string]any)["state"].(map[string]any)
	want := "Ticket state. Allowed values: 0, 1."
	if format == "openapi3" {
		want = "Ticket state. Allowed values: 0 = Open, 1 = Closed."
	}
	if state["description"] != want {
		t.Errorf("state description = %q, want %q", state["description"], want)
	}
}

// One tool per workflow (L2 and L3): the inputs the model wrote say nothing of
// the codes, so the document's values are added to them.
func checkTaskTools(t *testing.T, doc *spec.Document, wf *workflow.Document) {
	t.Helper()
	tools, _, err := TaskTools(doc, wf)
	if err != nil {
		t.Fatal(err)
	}
	props := tools[0].Tool.InputSchema["properties"].(map[string]any)
	in := props["channel"].(map[string]any)
	if got := in["description"]; got != "How the customer reached us. Allowed values: 1 = Email, 2 = LiveChat, 3 = PhoneCall." {
		t.Errorf("task input description = %q", got)
	}
	if len(in["enum"].([]any)) != 3 {
		t.Errorf("task input enum = %v", in["enum"])
	}
	if got := props["priority"].(map[string]any)["description"]; got != "Allowed values: low, high." {
		t.Errorf("task input priority description = %q", got)
	}
	if _, has := props["note"].(map[string]any)["enum"]; has {
		t.Error("an input that fills a field without an enum got one")
	}
	// The enum is enforced, not only shown.
	if _, err := tools[0].Task.Prepare(map[string]any{"channel": float64(9)}, time.Now()); err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Errorf("a code outside the enum was accepted: %v", err)
	}
	if _, err := tools[0].Task.Prepare(map[string]any{"channel": float64(2), "priority": "high"}, time.Now()); err != nil {
		t.Errorf("a listed code was refused: %v", err)
	}
}
