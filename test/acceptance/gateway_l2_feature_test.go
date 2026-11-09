//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/gateway"
)

func TestGatewayL2Feature(t *testing.T) {
	t.Parallel()
	cfg, err := casecfg.LoadConfig("../../cases/backoffice/gateway/L2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Case = "../../" + cfg.Case
	cfg.Upstream, cfg.UpstreamInsecureTLS = "https://localhost:7144", true
	gw, err := gateway.New(cfg, getenv, gateway.Labels{}, "acceptance", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + gateway.MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	call := func(t *testing.T, name string, args map[string]any) (gateway.Envelope, string) {
		t.Helper()
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		var env gateway.Envelope
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			t.Fatalf("not an envelope: %s", text)
		}
		return env, text
	}

	t.Run("the agent gets about twenty task tools and a map", func(t *testing.T) {
		n := 0
		for _, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		if n < 15 || n > 25 || session.InitializeResult().Instructions == "" {
			t.Fatalf("%d tools, instructions %q", n, session.InitializeResult().Instructions)
		}
	})

	t.Run("T02: alice's latest five deposits come back newest first", func(t *testing.T) {
		env, text := call(t, "list_deposits", map[string]any{"search": "bench_alice", "pageSize": 5})
		if env.Status != gateway.StatusOK {
			t.Fatalf("%s", text)
		}
		last := -1
		for _, ref := range []string{"BENCH-D-ALICE-07", "BENCH-D-ALICE-06", "BENCH-D-ALICE-05", "BENCH-D-ALICE-04", "BENCH-D-ALICE-03"} {
			i := strings.Index(text, ref)
			if i <= last {
				t.Fatalf("%s missing or out of order in %s", ref, text)
			}
			last = i
		}
		if strings.Contains(text, "BENCH-D-ALICE-02") {
			t.Fatal("a sixth deposit leaked into a page of five")
		}
	})

	t.Run("T04: bob's bonuses come back without the agent choosing a date window", func(t *testing.T) {
		env, text := call(t, "list_player_bonus_history", map[string]any{"customerId": 910002})
		if env.Status != gateway.StatusOK {
			t.Fatalf("%s", text)
		}
		for _, code := range []string{"BENCH-WEEKEND", "BENCH-LOYALTY", "BENCH-WELCOME"} {
			if !strings.Contains(text, code) {
				t.Fatalf("%s missing: %s", code, text)
			}
		}
	})

	t.Run("T10: a player who does not exist comes back empty, not invented", func(t *testing.T) {
		env, text := call(t, "find_player", map[string]any{"username": "bench_eve"})
		if env.Status != gateway.StatusOK || strings.Contains(text, "bench_eve\"") || strings.Contains(text, "BENCH-") {
			t.Fatalf("%s", text)
		}
	})

	t.Run("a page size over the cap is refused before it reaches Backoffice", func(t *testing.T) {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_withdrawals", Arguments: map[string]any{"pageSize": 1000}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "at most") {
			t.Fatalf("got %v", res.Content[0])
		}
	})
}
