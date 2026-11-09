// Package casecfg is the one place that says what a case is: where it keeps
// its files, what its gateway config says (level, upstream, auth, identity,
// approvals) and how its document is loaded as a level serves it. It depends
// on nothing of the gateway's runtime, so the drafting commands, the score and
// the benchmark read a case without linking the MCP server.
package casecfg

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Level is how agent-friendly the served tool set is. See docs/levels.md.
type Level string

// The levels a gateway can serve.
const (
	L0 Level = "L0"
	L1 Level = "L1"
	L2 Level = "L2"
	L3 Level = "L3"
)

// Levels lists the levels a gateway serves today, lowest first.
func Levels() []Level { return slices.Clone(stack) }

// Config is the gateway configuration file (docs/contracts.md §2).
type Config struct {
	Case     string         `yaml:"case"`
	Level    Level          `yaml:"level"`
	Listen   string         `yaml:"listen"`
	Upstream string         `yaml:"upstream"`
	Auth     AuthConfig     `yaml:"auth"`
	Identity IdentityConfig `yaml:"identity"`
	// UpstreamInsecureTLS accepts a self-signed upstream certificate. Only for
	// a local dev upstream such as Backoffice on https://localhost:7144.
	UpstreamInsecureTLS bool `yaml:"upstream_insecure_tls"`
	// Approvals is where humans approve held calls. Required at L3.
	Approvals ApprovalsConfig `yaml:"approvals"`
	// Policy is the L3 policy file, relative to Case. Empty means
	// DefaultPolicy, so one case can serve a drafted and a reviewed policy.
	Policy string `yaml:"policy"`
	// Workflows is the file of task tools L2 and L3 serve, relative to Case.
	// Empty means DefaultWorkflows.
	Workflows string `yaml:"workflows"`
}

// DefaultPolicy is the policy an L3 config reads when it names none.
const DefaultPolicy = PolicyFile

// DefaultWorkflows is the task tools an L2 or L3 config reads when it names none.
const DefaultWorkflows = WorkflowsFile

// IdentityConfig names who the gateway acts as and for whom.
type IdentityConfig struct {
	Actor      string `yaml:"actor"`
	OnBehalfOf string `yaml:"on_behalf_of"`
}

// LoadConfig reads a gateway configuration file. A relative case path is
// taken relative to the working directory, as every lodog command is run from
// the repository root.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own config file
	if err != nil {
		return nil, fmt.Errorf("read gateway config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse gateway config: %w", err)
	}
	if err := strictApprovals(data); err != nil {
		return nil, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if err := CheckChain(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("gateway config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	var errs []error
	if c.Case == "" {
		errs = append(errs, errors.New("case is required"))
	}
	switch c.Level {
	case L0, L1, L2:
		if c.Policy != "" {
			errs = append(errs, fmt.Errorf("policy is read only at L3: an %s gateway enforces nothing", c.Level))
		}
	case L3:
		switch c.Approvals.Listen {
		case "":
			errs = append(errs, errors.New("approvals.listen is required at L3: held calls need a place to be approved"))
		case c.Listen:
			errs = append(errs, errors.New("approvals.listen must differ from listen: the agent must not reach the approval endpoint"))
		}
	default:
		errs = append(errs, fmt.Errorf("level %q is not one of L0, L1, L2, L3", c.Level))
	}
	errs = append(errs, c.Approvals.validate(c.Listen)...)
	if c.Workflows != "" && !c.Level.ServesTasks() {
		errs = append(errs, fmt.Errorf("workflows are read only at L2 and L3: %s serves one tool per operation", c.Level))
	}
	if filepath.IsAbs(c.Workflows) {
		errs = append(errs, fmt.Errorf("workflows %q must be relative to the case", c.Workflows))
	}
	if filepath.IsAbs(c.Policy) {
		errs = append(errs, fmt.Errorf("policy %q must be relative to the case", c.Policy))
	}
	if c.Listen == "" {
		errs = append(errs, errors.New("listen is required"))
	}
	if err := checkUpstreamURL(c.Upstream); err != nil {
		errs = append(errs, err)
	}
	// The auth block is checked in full when the secrets are read
	// (newAuthenticator), which is where its rules depend on the type.
	if c.Auth.Type == "" {
		errs = append(errs, errors.New("auth.type is required"))
	}
	if c.Identity.Actor == "" {
		errs = append(errs, errors.New("identity.actor is required: every call is attributed"))
	}
	return errors.Join(errs...)
}

// Placeholder marks a value `lodog init` leaves for the operator to fill in
// (a login path, a token URL, who the gateway acts for). The gateway refuses to
// start while one is still there, so the mistake shows at startup and not at
// the first call.
const Placeholder = "TODO"

// Placeholders names every field that still holds the init placeholder.
func (c *Config) Placeholders() error {
	var errs []error
	for _, f := range []struct{ field, value string }{
		{"auth.login", c.Auth.Login},
		{"auth.token_url", c.Auth.TokenURL},
		{"identity.on_behalf_of", c.Identity.OnBehalfOf},
	} {
		if strings.Contains(f.value, Placeholder) {
			errs = append(errs, fmt.Errorf("%s is still the %s placeholder %q: fill in the real value in the gateway config", f.field, Placeholder, f.value))
		}
	}
	return errors.Join(errs...)
}

// OverlayPath is where the case keeps its L1 overlay.
func (c *Config) OverlayPath() string {
	return Overlay(c.Case)
}

// PolicyPath is the L3 policy this config reads.
func (c *Config) PolicyPath() string {
	if c.Policy == "" {
		return Policy(c.Case)
	}
	return filepath.Join(c.Case, c.Policy)
}

// WorkflowsPath is the task tools this config reads at L2 and L3.
func (c *Config) WorkflowsPath() string {
	if c.Workflows == "" {
		return Workflows(c.Case)
	}
	return filepath.Join(c.Case, c.Workflows)
}

// ServesTasks reports whether the level serves task tools (workflows) rather
// than one tool per operation. L3 serves the same tools as L2: the only
// difference between them is enforcement.
func (l Level) ServesTasks() bool { return l == L2 || l == L3 }

// OpenAPIPath is where the case keeps the legacy system's own document.
func (c *Config) OpenAPIPath() string {
	return OpenAPI(c.Case)
}

// Overrides are the settings that differ between where a config was
// proven and where it is pointed next (same case, same policy, another
// system and another person): `lodog serve --upstream/--listen/--approvals-listen`. An empty
// field keeps the config's own value.
type Overrides struct {
	Upstream        string
	Listen          string
	ApprovalsListen string
	Actor           string
	OnBehalfOf      string
}

// Override applies o and checks the result with the same rules as LoadConfig,
// so an override cannot produce a config LoadConfig would refuse.
func (c *Config) Override(o Overrides) error {
	if o.Upstream != "" {
		c.Upstream = o.Upstream
	}
	if o.Listen != "" {
		c.Listen = o.Listen
	}
	if o.ApprovalsListen != "" {
		c.Approvals.Listen = o.ApprovalsListen
	}
	if o.Actor != "" {
		c.Identity.Actor = o.Actor
	}
	if o.OnBehalfOf != "" {
		c.Identity.OnBehalfOf = o.OnBehalfOf
	}
	return c.validate()
}

// checkUpstreamURL refuses an upstream the gateway could not dial, at load
// time instead of at the first call.
func checkUpstreamURL(raw string) error {
	if raw == "" {
		return errors.New("upstream is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("upstream %q must be an absolute http(s) URL, e.g. https://host/base", raw)
	}
	return nil
}
