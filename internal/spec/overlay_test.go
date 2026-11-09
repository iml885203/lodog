package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnOverlayReachesTheParsedOperation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	docPath := write("openapi.json", `{"openapi":"3.0.1","paths":{"/api/x":{"get":{"parameters":[{"name":"Type","in":"query","schema":{"type":"integer"}}]}}}}`)
	ovPath := write("overlay.yaml", `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: "$.paths['/api/x'].get"
    update:
      summary: List things
      x-agent: {risk: read, whenToUse: listing things, whenNotToUse: changing things}
  - target: "$.paths['/api/x'].get.parameters[?(@.name=='Type')]"
    update: {description: "1 = a, 2 = b"}
`)
	doc, err := LoadFileWithOverlay(docPath, ovPath)
	if err != nil {
		t.Fatal(err)
	}
	op := doc.Operations[0]
	if op.Summary != "List things" || op.AgentRisk != "read" || op.WhenToUse != "listing things" || op.WhenNotToUse != "changing things" {
		t.Fatalf("op = %+v", op)
	}
	if op.Params[0].Description != "1 = a, 2 = b" {
		t.Fatalf("param = %+v", op.Params[0])
	}
}
