package casecfg

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheLayoutIsRelativeToTheCaseDirectory(t *testing.T) {
	for got, want := range map[string]string{
		OpenAPI("c"):     "c/spec/openapi.json",
		Overlay("c"):     "c/spec/overlay.yaml",
		Policy("c"):      "c/spec/policy.yaml",
		DraftPolicy("c"): "c/spec/policy.auto.yaml",
		Workflows("c"):   "c/spec/workflows.arazzo.yaml",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

// Where a case keeps its files is said here and nowhere else: a second
// spelling in another package is a second place to forget.
func TestNoOtherPackageSpellsACaseFilePath(t *testing.T) {
	spellings := []string{`"spec", "openapi.json"`, `"spec", "overlay.yaml"`, `"spec", "policy.yaml"`, `"spec", "policy.auto.yaml"`, `"spec", "workflows.arazzo.yaml"`}
	for _, f := range []string{OpenAPIFile, OverlayFile, PolicyFile, DraftPolicyFile, WorkflowsFile} {
		spellings = append(spellings, `"`+f+`"`)
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "casecfg"+string(filepath.Separator)) {
			return err
		}
		src, err := os.ReadFile(path) //nolint:gosec // G304: walks this repository's own source
		if err != nil {
			return err
		}
		for _, s := range spellings {
			if strings.Contains(string(src), s) {
				t.Errorf("%s spells %s: use internal/casecfg", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
