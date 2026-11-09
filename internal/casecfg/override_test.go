package casecfg

import (
	"strings"
	"testing"
)

func overrideBase() *Config {
	return &Config{
		Case: "cases/x", Level: L3, Listen: "127.0.0.1:7400", Upstream: "http://127.0.0.1:7401",
		Approvals: ApprovalsConfig{Listen: "127.0.0.1:7402"},
		Auth:      AuthConfig{Type: "cookie-session"},
		Identity:  IdentityConfig{Actor: "agent:x"},
	}
}

func TestOverrideReplacesOnlyWhatIsGiven(t *testing.T) {
	c := overrideBase()
	if err := c.Override(Overrides{Upstream: "https://stg.example"}); err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://stg.example" || c.Listen != "127.0.0.1:7400" || c.Approvals.Listen != "127.0.0.1:7402" {
		t.Fatalf("got upstream=%s listen=%s approvals=%s", c.Upstream, c.Listen, c.Approvals.Listen)
	}
	if err := c.Override(Overrides{Listen: "127.0.0.1:7460", ApprovalsListen: "127.0.0.1:7462"}); err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:7460" || c.Approvals.Listen != "127.0.0.1:7462" {
		t.Fatalf("listen=%s approvals=%s", c.Listen, c.Approvals.Listen)
	}
}

func TestOverrideCannotBreakTheListenRule(t *testing.T) {
	c := overrideBase()
	err := c.Override(Overrides{Listen: "127.0.0.1:7402"})
	if err == nil || !strings.Contains(err.Error(), "approvals.listen must differ") {
		t.Fatalf("listen equal to approvals.listen accepted: %v", err)
	}
}

func TestOverrideReplacesTheIdentity(t *testing.T) {
	c := overrideBase()
	c.Identity.OnBehalfOf = "user:alice"
	if err := c.Override(Overrides{OnBehalfOf: "user:demo"}); err != nil {
		t.Fatal(err)
	}
	if c.Identity.Actor != "agent:x" || c.Identity.OnBehalfOf != "user:demo" {
		t.Fatalf("identity = %+v", c.Identity)
	}
	if err := c.Override(Overrides{Actor: "agent:demo"}); err != nil {
		t.Fatal(err)
	}
	if c.Identity.Actor != "agent:demo" || c.Identity.OnBehalfOf != "user:demo" {
		t.Fatalf("identity = %+v", c.Identity)
	}
}

func TestUpstreamMustBeAnAbsoluteHTTPURL(t *testing.T) {
	for _, bad := range []string{"notaurl", "www.example.com", "ftp://x.example", "http://", "/relative"} {
		c := overrideBase()
		err := c.Override(Overrides{Upstream: bad})
		if err == nil || !strings.Contains(err.Error(), "absolute http(s) URL") {
			t.Errorf("upstream %q accepted: %v", bad, err)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:7401", "https://stg.example/base"} {
		c := overrideBase()
		if err := c.Override(Overrides{Upstream: ok}); err != nil {
			t.Errorf("upstream %q refused: %v", ok, err)
		}
	}
}
