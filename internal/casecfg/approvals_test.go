package casecfg

import (
	"strings"
	"testing"
	"time"
)

func TestTheApprovalModeIsResolvedAndChecked(t *testing.T) {
	web := &WebhookConfig{Listen: "y", BaseURL: "http://h"}
	for name, tc := range map[string]struct {
		cfg  ApprovalsConfig
		want ApprovalMode
	}{
		"default":       {ApprovalsConfig{}, ModeElicitation},
		"webhook block": {ApprovalsConfig{Webhook: web}, ModeWebhook},
		"named wins":    {ApprovalsConfig{Mode: ModeElicitation}, ModeElicitation},
	} {
		if got := tc.cfg.Resolved(); got != tc.want {
			t.Errorf("%s: resolved %s, want %s", name, got, tc.want)
		}
	}
	for name, tc := range map[string]struct {
		cfg  ApprovalsConfig
		want string
	}{
		"unknown":                {ApprovalsConfig{Mode: "carrier-pigeon"}, "not one of elicitation, webhook"},
		"slack is gone":          {ApprovalsConfig{Mode: "slack"}, "not one of elicitation, webhook"},
		"none is not a mode":     {ApprovalsConfig{Mode: "none"}, "not one of elicitation, webhook"},
		"webhook without block":  {ApprovalsConfig{Mode: ModeWebhook}, "needs the approvals.webhook block"},
		"elicitation with block": {ApprovalsConfig{Mode: ModeElicitation, Webhook: web}, "does not use"},
		"negative timeout":       {ApprovalsConfig{ElicitationTimeout: -time.Second}, "negative"},
	} {
		if errs := tc.cfg.validate("m"); len(errs) == 0 || !strings.Contains(errorsText(errs), tc.want) {
			t.Errorf("%s: errors = %v, want one mentioning %q", name, errs, tc.want)
		}
	}
	if errs := (ApprovalsConfig{}).validate("m"); len(errs) != 0 {
		t.Errorf("the default mode was refused: %v", errs)
	}
}

func errorsText(errs []error) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
