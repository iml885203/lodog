package casecfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestL3NeedsItsOwnApprovalListener(t *testing.T) {
	base := Config{
		Case: "x", Level: L3, Listen: "127.0.0.1:7400", Upstream: "http://u",
		Auth: AuthConfig{Type: AuthAPIKey}, Identity: IdentityConfig{Actor: "a"},
	}
	if err := base.validate(); err == nil {
		t.Fatal("L3 without approvals.listen must be refused")
	}
	base.Approvals.Listen = base.Listen
	if err := base.validate(); err == nil {
		t.Fatal("approvals on the MCP listener must be refused")
	}
}

func TestAnL3ConfigReadsTheCasePolicyUnlessItNamesAnother(t *testing.T) {
	cfg := Config{
		Case: "cases/x", Level: L3, Listen: "127.0.0.1:7400", Upstream: "http://u",
		Auth: AuthConfig{Type: AuthAPIKey}, Identity: IdentityConfig{Actor: "a"},
		Approvals: ApprovalsConfig{Listen: "127.0.0.1:7402"},
	}
	if got := cfg.PolicyPath(); got != filepath.Join("cases", "x", "spec", "policy.yaml") {
		t.Fatalf("default policy path = %s", got)
	}
	cfg.Policy = "spec/policy.auto.yaml"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.PolicyPath(); got != filepath.Join("cases", "x", "spec", "policy.auto.yaml") {
		t.Fatalf("named policy path = %s", got)
	}
	cfg.Policy = "/etc/policy.yaml"
	if err := cfg.validate(); err == nil {
		t.Fatal("an absolute policy path must be refused: it is relative to the case")
	}
	cfg.Policy, cfg.Level = "spec/policy.auto.yaml", L0
	if err := cfg.validate(); err == nil {
		t.Fatal("a policy on an L0 config must be refused: L0 enforces nothing")
	}
}

func TestL2AndL3ReadWorkflowsAndOnlyL3ReadsAPolicy(t *testing.T) {
	base := func(level Level) Config {
		return Config{
			Case: "cases/x", Level: level, Listen: "127.0.0.1:7400", Upstream: "http://u",
			Auth: AuthConfig{Type: AuthAPIKey}, Identity: IdentityConfig{Actor: "a"},
		}
	}
	l2 := base(L2)
	if err := l2.validate(); err != nil {
		t.Fatalf("L2 refused: %v", err)
	}
	if got := l2.WorkflowsPath(); got != filepath.Join("cases", "x", "spec", "workflows.arazzo.yaml") {
		t.Fatalf("default workflows = %s", got)
	}
	l2.Workflows = "spec/workflows.reviewed.yaml"
	if err := l2.validate(); err != nil || l2.WorkflowsPath() != filepath.Join("cases", "x", "spec", "workflows.reviewed.yaml") {
		t.Fatalf("named workflows: %v %s", err, l2.WorkflowsPath())
	}
	for name, cfg := range map[string]Config{
		"policy at L2":            func() Config { c := base(L2); c.Policy = "spec/policy.yaml"; return c }(),
		"policy at L1":            func() Config { c := base(L1); c.Policy = "spec/policy.yaml"; return c }(),
		"workflows at L1":         func() Config { c := base(L1); c.Workflows = "spec/w.yaml"; return c }(),
		"absolute workflows path": func() Config { c := base(L2); c.Workflows = "/etc/w.yaml"; return c }(),
	} {
		if err := cfg.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAnApprovalsSettingTheGatewayDoesNotKnowIsAnError(t *testing.T) {
	const head = "case: c\nlevel: L0\nlisten: 127.0.0.1:7400\nupstream: http://u\nauth: {type: api-key}\nidentity: {actor: a}\n"
	for name, tc := range map[string]struct {
		approvals string
		want      string // empty: loads
	}{
		"old slack block":       {"approvals:\n  listen: 127.0.0.1:7402\n  slack: {channel: C1}\n", "no longer exists"},
		"old approvers":         {"approvals:\n  listen: 127.0.0.1:7402\n  approvers: [slack:U1]\n", "no longer exists"},
		"a typo":                {"approvals:\n  listen: 127.0.0.1:7402\n  moed: webhook\n", "moed"},
		"a typo in the webhook": {"approvals:\n  listen: 127.0.0.1:7402\n  webhook: {listen: 127.0.0.1:7403, base_url: 'http://h', url_nv: X}\n", "url_nv"},
		"known settings":        {"approvals:\n  listen: 127.0.0.1:7402\n  mode: elicitation\n  elicitation_timeout: 1m\n", ""},
		"webhook":               {"approvals:\n  listen: 127.0.0.1:7402\n  webhook: {listen: 127.0.0.1:7403, base_url: 'http://h'}\n", ""},
	} {
		path := filepath.Join(t.TempDir(), "gw.yaml")
		if err := os.WriteFile(path, []byte(head+tc.approvals), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want one mentioning %q", name, err, tc.want)
		}
	}
}

func TestTheWebhookConfigRefusesWhatCouldNeverWork(t *testing.T) {
	good := &WebhookConfig{Listen: "127.0.0.1:7403", BaseURL: "http://localhost:7403"}
	for name, tc := range map[string]struct {
		cfg  ApprovalsConfig
		want string
	}{
		"mode without block": {ApprovalsConfig{Listen: "x", Mode: ModeWebhook}, "needs the approvals.webhook block"},
		"no listen":          {ApprovalsConfig{Listen: "x", Webhook: &WebhookConfig{BaseURL: "http://h"}}, "webhook.listen is required"},
		"same as admin":      {ApprovalsConfig{Listen: "127.0.0.1:7403", Webhook: good}, "must differ"},
		"relative base":      {ApprovalsConfig{Listen: "x", Webhook: &WebhookConfig{Listen: "y", BaseURL: "localhost:7403"}}, "base_url"},
		"no base":            {ApprovalsConfig{Listen: "x", Webhook: &WebhookConfig{Listen: "y"}}, "base_url"},
	} {
		if errs := tc.cfg.validate("127.0.0.1:1"); len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), tc.want) {
			t.Errorf("%s: errors = %v, want one mentioning %q", name, errs, tc.want)
		}
	}
	if errs := (ApprovalsConfig{Listen: "x", Webhook: good}).validate("127.0.0.1:1"); len(errs) != 0 {
		t.Errorf("a complete webhook config was refused : %v", errs)
	}
	c := &Config{
		Case: "c", Level: L3, Listen: "127.0.0.1:7403", Upstream: "u", Auth: AuthConfig{Type: "x"}, Identity: IdentityConfig{Actor: "a"},
		Approvals: ApprovalsConfig{Listen: "127.0.0.1:7402", Webhook: good},
	}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "agent must not reach the approval page") {
		t.Errorf("a page on the MCP address was accepted: %v", err)
	}
}
