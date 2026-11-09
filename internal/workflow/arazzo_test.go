package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadFileReadsAWorkflowsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.yaml")
	if err := os.WriteFile(path, []byte(arazzoShop), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Workflows) != 2 || d.Workflows[0].WorkflowID != "find_player" || d.Workflows[0].Agent.Risk != "read" {
		t.Fatalf("workflows = %+v", d.Workflows)
	}
}

func TestLoadFileSaysWhichFileCouldNotBeRead(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "read workflows") {
		t.Fatalf("missing file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("workflows: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("bad YAML: %v, want the path named", err)
	}
}

func TestAWorkflowsDocumentSurvivesWritingAndReading(t *testing.T) {
	d, err := Parse([]byte(arazzoShop))
	if err != nil {
		t.Fatal(err)
	}
	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, again) {
		t.Fatalf("round trip changed the document:\n%s", data)
	}
	if !strings.Contains(string(data), "x-agent:") {
		t.Fatalf("the x-agent extension is lost:\n%s", data)
	}
}
