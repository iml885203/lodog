package readiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
)

const taskCaseDoc = `{"openapi":"3.0.4","info":{"title":"shop","version":"1"},"paths":{
 "/players":{"get":{"summary":"Search","parameters":[{"name":"pageSize","in":"query","required":true,"schema":{"type":"integer"}}],
   "responses":{"200":{"description":"ok"},"400":{"description":"bad"}}}},
 "/players/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],"responses":{"200":{"description":"ok"}}}},
 "/players/{id}/bonus":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],
   "responses":{"200":{"description":"ok"},"409":{"description":"taken"}}}},
 "/players/{id}/resetPassword":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}}
}}`

const taskCaseWorkflows = `arazzo: 1.0.1
info: {title: t, version: "1", description: Start with list_players.}
sourceDescriptions: [{name: shop}]
workflows:
  - workflowId: list_players
    summary: List players.
    inputs: {type: object, properties: {pageSize: {type: integer, maximum: 50, description: Rows.}}}
    steps:
      - stepId: list
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players/get'
        parameters: [{name: pageSize, value: $inputs.pageSize}]
  - workflowId: read_and_reward
    summary: Read a player, then give a bonus.
    inputs: {type: object, properties: {id: {type: integer}}, required: [id]}
    steps:
      - {stepId: read, operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}/get', parameters: [{name: id, value: $inputs.id}]}
      - {stepId: give, operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}~1bonus/post', parameters: [{name: id, value: $inputs.id}]}
    x-agent: {risk: money}
  - workflowId: reset_password
    summary: Reset a password.
    inputs: {type: object, properties: {id: {type: integer, description: Player.}}, required: [id]}
    steps:
      - {stepId: reset, operationPath: '{$sourceDescriptions.shop.url}#/paths/~1players~1{id}~1resetPassword/get', parameters: [{name: id, value: $inputs.id}]}
`

func taskCase(t *testing.T, level string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"spec", "gateway"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"spec/openapi.json":          taskCaseDoc,
		"spec/overlay.yaml":          "overlay: 1.0.0\ninfo: {title: t, version: \"1\"}\nactions: []\n",
		"spec/workflows.arazzo.yaml": taskCaseWorkflows,
		"spec/policy.yaml":           "risks: {read: allow, money: approval, credential: hide}\ndefault: money\nrules:\n  - {match: \"GET /players/*/resetPassword\", risk: credential}\n  - {match: \"POST /**\", risk: money}\n  - {match: \"GET /**\", risk: read}\n",
	}
	// Levels are not skipped: a config for a level comes with every one below it.
	for _, l := range casecfg.Levels() {
		files["gateway/"+string(l)+".yaml"] = "case: " + dir + "\nlevel: " + string(l) + "\nlisten: 127.0.0.1:1\nupstream: http://u\nauth: {type: api-key, key_env: K, header: X-Key}\n" +
			"identity: {actor: agent:t, on_behalf_of: user:a}\napprovals: {listen: 127.0.0.1:2}\n"
		if string(l) == level {
			break
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "gateway", level+".yaml")
}

func TestAnL2ConfigIsScoredOnItsTaskTools(t *testing.T) {
	v, notes, err := FromGateway(taskCase(t, "L2"))
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if r.Tools != 3 || len(notes) != 1 || !strings.Contains(notes[0], "3 個任務 tool") {
		t.Fatalf("tools = %d, notes = %v", r.Tools, notes)
	}
	if c := check(r, "tool_count"); c.Points != c.Max {
		t.Errorf("tool count %v of %v: three tools is well under twenty", c.Points, c.Max)
	}
	// reset_password's input is described; read_and_reward's is not.
	if c := check(r, "descriptions"); !strings.Contains(c.Detail, "3/3 個 tool 有說明，2/3 個參數有說明") {
		t.Errorf("descriptions = %s", c.Detail)
	}
	// read_and_reward declares money; reset_password names an action on GET and declares nothing.
	if c := check(r, "risk_declared"); !strings.Contains(c.Detail, "1/2") {
		t.Errorf("risk = %s", c.Detail)
	}
	if c := check(r, "side_effect_gets"); c.Points != 0 {
		t.Errorf("an undeclared password-resetting task at L2 scored %v", c.Points)
	}
	// list_players' one step documents an error; read_and_reward's read step does not.
	if c := check(r, "error_responses"); !strings.Contains(c.Detail, "1/3") {
		t.Errorf("errors = %s: a task documents its errors only when every step does", c.Detail)
	}
	if c := check(r, "agent_identity"); c.Points != 0 {
		t.Error("identity counted below L3")
	}
}

func TestAnL3ConfigDropsTheTaskToolsItsPolicyHides(t *testing.T) {
	v, _, err := FromGateway(taskCase(t, "L3"))
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if r.Tools != 2 {
		t.Fatalf("tools = %d: reset_password has a hidden step", r.Tools)
	}
	if c := check(r, "side_effect_gets"); c.Points != c.Max {
		t.Errorf("the trap is hidden at L3 but scored %v", c.Points)
	}
	if c := check(r, "agent_identity"); c.Points != c.Max {
		t.Error("identity not counted at L3")
	}
	// L4 is about the legacy code, so it names the operations behind the tools.
	it, ok := item(r.L4, "error_responses")
	if !ok || !strings.Contains(strings.Join(it.Examples, " "), "GET /players/{id}") {
		t.Fatalf("L4 error item = %+v", it)
	}
}

// A workflow that calls an operation the gateway does not serve is not a tool
// and the report names it, the way it names an operation that is skipped.
func TestAWorkflowUsingAnUnservedOperationIsReportedNotScoredOrFailed(t *testing.T) {
	cfgPath := taskCase(t, "L2")
	dir := filepath.Dir(filepath.Dir(cfgPath))
	doc := strings.Replace(taskCaseDoc, `"paths":{`, `"paths":{
 "/raw":{"post":{"requestBody":{"content":{"application/octet-stream":{"schema":{"type":"string","format":"binary"}}}}}},`, 1)
	wf := taskCaseWorkflows + `  - workflowId: upload_raw
    summary: Upload raw bytes.
    steps:
      - {stepId: push, operationPath: '{$sourceDescriptions.shop.url}#/paths/~1raw/post'}
`
	for name, body := range map[string]string{"openapi.json": doc, "workflows.arazzo.yaml": wf} {
		if err := os.WriteFile(filepath.Join(dir, "spec", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v, _, err := FromGateway(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	r := Score(v)
	if r.Tools != 3 || len(r.Skipped) != 1 || r.Skipped[0].Path != "/raw" ||
		!strings.Contains(r.Skipped[0].Reason, "workflow upload_raw skipped: step push") {
		t.Fatalf("tools %d, skipped %+v", r.Tools, r.Skipped)
	}
}
