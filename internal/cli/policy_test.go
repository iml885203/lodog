package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every operation below is settled by heuristics, so no model is asked.
const settledDoc = `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{
	"/api/items":{"get":{}},
	"/api/items/{id}":{"delete":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]}}}}`

const ownRiskSet = `default: change
risks:
  - {name: destructive, action: approval, never_auto_approve: true, classes: [irreversible], means: gone once it runs}
  - {name: change, action: approval, classes: [write, money], means: edits records}
  - {name: secret, action: hide, classes: [credential, internal, test], means: not for agents}
  - {name: lookup, action: allow, classes: [read], means: looks things up}
`

func draftWith(t *testing.T, risks string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	doc, set := filepath.Join(dir, "openapi.json"), filepath.Join(dir, "risks.yaml")
	for path, body := range map[string]string{doc: settledDoc, set: risks} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"policy", "draft", doc, "--risks", set, "--claude", filepath.Join(dir, "no-claude")})
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestPolicyDraftUsesTheRiskSetItIsGiven(t *testing.T) {
	out, _, err := draftWith(t, ownRiskSet)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"destructive:", "never_auto_approve: true", "risk: destructive", "risk: lookup"} {
		if !strings.Contains(out, want) {
			t.Errorf("policy lacks %q:\n%s", want, out)
		}
	}
}

func TestPolicyDraftWarnsWhenNoRiskIsNeverAutoApproved(t *testing.T) {
	_, errOut, err := draftWith(t, strings.Replace(ownRiskSet, "never_auto_approve: true, ", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "warning: no risk has never_auto_approve") {
		t.Fatalf("stderr = %q, want the never_auto_approve warning", errOut)
	}
}

func TestPolicyDraftRefusesABrokenRiskSet(t *testing.T) {
	if _, _, err := draftWith(t, "default: x\nrisks: []\n"); err == nil || !strings.Contains(err.Error(), "goes to no risk") {
		t.Fatalf("err = %v, want the risk set refused", err)
	}
}
