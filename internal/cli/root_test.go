package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestEveryTrackHasASubcommand(t *testing.T) {
	want := []string{"init", "score", "enrich", "serve", "bench", "policy", "report"}
	root := newRootCmd()
	for _, name := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("subcommand %q missing", name)
		}
	}
}

func TestPolicyDraftTakesOnlyAnOpenAPIDocument(t *testing.T) {
	for _, args := range [][]string{{"policy", "draft"}, {"policy", "draft", "openapi.json", "answers.yaml"}, {"policy", "draft", "openapi.json", "--case", "cases/backoffice"}} {
		root := newRootCmd()
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Errorf("%v: accepted; the draft reads one OpenAPI document and nothing else", args)
		}
	}
}

func TestVersionFlagPrintsTheStampedVersion(t *testing.T) {
	old := version
	version = "v9.9.9"
	t.Cleanup(func() { version = old })

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "v9.9.9") {
		t.Fatalf("--version printed %q, want the stamped version", out.String())
	}
}

// One config proven at one place is pointed at another with these flags,
// not by editing the file (docs/getting-started.md, "接到另一個系統").
func TestServeCanRepointAConfig(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"upstream", "listen", "approvals-listen", "actor", "on-behalf-of"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("serve has no --%s", f)
		}
	}
}
