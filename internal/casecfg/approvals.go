// Package casecfg is the one place that says what a case is: where it keeps
// its files, what its gateway config says (level, upstream, auth, identity,
// approvals) and how its document is loaded as a level serves it. It depends
// on nothing of the gateway's runtime, so the drafting commands, the score and
// the benchmark read a case without linking the MCP server.
package casecfg

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ApprovalsConfig places the approval endpoint on its own listener, apart
// from the MCP endpoint the agent reaches.
type ApprovalsConfig struct {
	Listen string `yaml:"listen"`
	// Mode is who approves a held call and through what. Empty means
	// ModeWebhook when the webhook block is set, else ModeElicitation.
	Mode ApprovalMode `yaml:"mode"`
	// InProcess means the host of this gateway decides held calls itself (the
	// benchmark runner, through DecidePending): nobody is asked at the client
	// and no channel is needed. A Go caller sets it; a config file cannot.
	InProcess bool `yaml:"-"`
	// ElicitationTimeout is how long the user has to answer the prompt.
	// Zero means DefaultElicitationTimeout.
	ElicitationTimeout time.Duration `yaml:"elicitation_timeout"`
	// Webhook, when set, sends each held call to a Slack incoming webhook
	// with a one-time link to an approval page the gateway serves.
	Webhook *WebhookConfig `yaml:"webhook"`
}

// WebhookConfig is the incoming-webhook approval mode.
type WebhookConfig struct {
	// URLEnv names the variable holding the incoming webhook URL, which is a
	// secret. Default DefaultWebhookURLEnv.
	URLEnv string `yaml:"url_env"`
	// Listen is where the gateway serves the approval page.
	Listen string `yaml:"listen"`
	// BaseURL is the address the link in the message points at, as the
	// approvers reach it (http://localhost:7403 for a local demo).
	BaseURL string `yaml:"base_url"`
}

// ApprovalMode says how a held call gets its verdict.
type ApprovalMode string

// The approval modes. See docs/approvals.md for what each one proves.
const (
	// ModeElicitation asks the user at the MCP client (elicitation/create);
	// the person at the client is the approver.
	ModeElicitation ApprovalMode = "elicitation"
	// ModeWebhook sends a Slack message with a one-time link to an approval page.
	ModeWebhook ApprovalMode = "webhook"
)

// DefaultElicitationTimeout is how long the prompt waits for the user.
const DefaultElicitationTimeout = 3 * time.Minute

// Resolved is the mode in effect: the one named, else the channel whose
// block is set, else elicitation.
func (a ApprovalsConfig) Resolved() ApprovalMode {
	switch {
	case a.Mode != "":
		return a.Mode
	case a.Webhook != nil:
		return ModeWebhook
	}
	return ModeElicitation
}

// ElicitationTimeoutOrDefault is how long the user has to answer the prompt.
func (a ApprovalsConfig) ElicitationTimeoutOrDefault() time.Duration {
	if a.ElicitationTimeout > 0 {
		return a.ElicitationTimeout
	}
	return DefaultElicitationTimeout
}

// DefaultWebhookURLEnv is the variable holding the Slack webhook URL unless the config names another.
const DefaultWebhookURLEnv = "LODOG_SLACK_WEBHOOK_URL"

// strictApprovals refuses an approvals key the gateway does not know. A
// removed key (the old slack block, approvers) would otherwise be ignored and
// the gateway would start with another way to approve than the one written.
func strictApprovals(data []byte) error {
	var doc struct {
		Approvals yaml.Node `yaml:"approvals"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Approvals.Kind == 0 {
		return nil //nolint:nilerr // a document that does not parse fails in LoadConfig's own decode
	}
	raw, err := yaml.Marshal(&doc.Approvals)
	if err != nil {
		return fmt.Errorf("approvals: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var known ApprovalsConfig
	if err := dec.Decode(&known); err != nil {
		hint := ""
		if m := err.Error(); strings.Contains(m, "slack") || strings.Contains(m, "approvers") {
			hint = ": the Slack app mode (approvals.slack, approvals.approvers) no longer exists; use approvals.webhook or the default elicitation (docs/approvals.md)"
		}
		return fmt.Errorf("approvals has a setting the gateway does not know (%s)%s", strings.TrimPrefix(err.Error(), "yaml: "), hint)
	}
	return nil
}

func (a ApprovalsConfig) validate(mcpListen string) []error {
	var errs []error
	switch mode := a.Resolved(); mode {
	case ModeElicitation:
		if a.Webhook != nil {
			errs = append(errs, errors.New("approvals.mode elicitation does not use approvals.webhook: remove the block or set the mode to webhook"))
		}
	case ModeWebhook:
		if a.Webhook == nil {
			errs = append(errs, errors.New("approvals.mode webhook needs the approvals.webhook block"))
		} else {
			errs = append(errs, a.validateWebhook(mcpListen)...)
		}
	default:
		errs = append(errs, fmt.Errorf("approvals.mode %q is not one of elicitation, webhook", mode))
	}
	if a.ElicitationTimeout < 0 {
		errs = append(errs, errors.New("approvals.elicitation_timeout must not be negative"))
	}
	return errs
}

func (a ApprovalsConfig) validateWebhook(mcpListen string) []error {
	var errs []error
	if a.Webhook.Listen == "" {
		errs = append(errs, errors.New("approvals.webhook.listen is required: the approval page needs an address"))
	}
	if a.Webhook.Listen != "" && a.Webhook.Listen == a.Listen {
		errs = append(errs, errors.New("approvals.webhook.listen must differ from approvals.listen"))
	}
	if a.Webhook.Listen != "" && a.Webhook.Listen == mcpListen {
		errs = append(errs, errors.New("approvals.webhook.listen must differ from listen: the agent must not reach the approval page by the MCP address"))
	}
	u, err := url.Parse(a.Webhook.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("approvals.webhook.base_url %q must be an http(s) address", a.Webhook.BaseURL))
	}
	return errs
}
