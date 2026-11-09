package serving

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// What a case is and what a level serves must be readable without the MCP
// server: lodog score and lodog init link none of the gateway, and a package
// that grows an import of it brings the transport back into every command that
// only reads documents.
func TestPackagesThatReadACaseDoNotImportTheGateway(t *testing.T) {
	for _, pkg := range []string{"casecfg", "serving", "approval", "readiness", "initcase", "workflow", "workflowdraft", "policy", "policydraft", "review", "enrich", "spec"} {
		files, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("package %s: files %v, err %v", pkg, files, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				// approval speaks MCP elicitation to the user's client; nothing else here may.
				if strings.HasSuffix(path, "/internal/gateway") || (pkg != "approval" && strings.Contains(path, "modelcontextprotocol")) {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}
