package cli

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/gateway"
)

// Each override flag feeds its own field: five different values, so binding
// one flag to another's variable shows.
func TestServeFlagsFeedTheirOwnOverride(t *testing.T) {
	cmd := newServeCmd()
	err := cmd.ParseFlags([]string{
		"--upstream", "https://u.example", "--listen", "127.0.0.1:1", "--approvals-listen", "127.0.0.1:2",
		"--actor", "agent:a", "--on-behalf-of", "user:b",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := casecfg.Overrides{Upstream: "https://u.example", Listen: "127.0.0.1:1", ApprovalsListen: "127.0.0.1:2", Actor: "agent:a", OnBehalfOf: "user:b"}
	if got := overridesOf(cmd); got != want {
		t.Fatalf("overrides = %+v, want %+v", got, want)
	}
}

// serve runs with the overridden config: the identity the approver sees, the
// upstream and the ports are the flags' values, not the file's.
func TestServeBuildsTheGatewayFromTheOverriddenConfig(t *testing.T) {
	t.Chdir("../..")
	env := map[string]string{"LODOG_UPSTREAM_CLIENT_ID": "id-long-enough-1", "LODOG_UPSTREAM_CLIENT_SECRET": "test-secret-00000000"}
	over := casecfg.Overrides{Upstream: "https://stg.example/base", Listen: "127.0.0.1:17411", ApprovalsListen: "127.0.0.1:17412", Actor: "agent:demo", OnBehalfOf: "user:demo"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, gw, err := buildServeGateway("cases/keycloak/gateway/L0.yaml", over, func(k string) string { return env[k] }, gateway.Labels{}, log)
	if err != nil {
		t.Fatal(err)
	}
	if gw == nil || cfg.Identity.Actor != "agent:demo" || cfg.Identity.OnBehalfOf != "user:demo" ||
		cfg.Upstream != "https://stg.example/base" || cfg.Listen != "127.0.0.1:17411" {
		t.Fatalf("config the gateway runs with: %+v", cfg)
	}
}

func TestServeRefusesAnOverrideThatIsNotAURL(t *testing.T) {
	t.Chdir("../..")
	_, _, e := buildServeGateway("cases/keycloak/gateway/L0.yaml", casecfg.Overrides{Upstream: "notaurl"}, func(string) string { return "" }, gateway.Labels{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e == nil || !strings.Contains(e.Error(), "absolute http(s) URL") {
		t.Fatalf("err = %v", e)
	}
}
