package gateway

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

// The tool list is what agents read, so a document with no clashing parameter
// names must serialize exactly as it always has. The document is the public
// Petstore (not Backoffice); the golden file was generated before parameters got
// position-prefixed argument names. Regenerate only for a deliberate change to
// the tool shape: UPDATE_GOLDEN=1 go test ./internal/gateway -run Golden.
const (
	goldenDoc  = "../spec/testdata/petstore3-openapi3.json"
	goldenFile = "testdata/petstore3-tools.golden.json"
)

func TestGoldenToolListOfADocumentWithoutClashes(t *testing.T) {
	doc, err := spec.LoadFile(goldenDoc)
	if err != nil {
		t.Fatal(err)
	}
	// The image upload takes an octet-stream body, which the gateway does not
	// serve (and once served without its body); it is not part of this shape.
	kept := doc.Operations[:0]
	for _, op := range doc.Operations {
		if op.Path != "/pet/{petId}/uploadImage" {
			kept = append(kept, op)
		}
	}
	doc.Operations = kept
	tools, skipped := serving.RawTools(doc)
	if len(skipped) != 0 {
		t.Fatalf("skipped %d operations", len(skipped))
	}
	list := make([]any, len(tools))
	for i, tl := range tools {
		list[i] = mcpTool(tl.Tool)
	}
	got, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("the tool list changed; diff %s against the current output", goldenFile)
	}
}
