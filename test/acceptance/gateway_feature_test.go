//go:build acceptance

// Package acceptance runs lodog against the Backoffice benchmark env
// (bring your own compatible upstream). `make test-accept`.
package acceptance

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/gateway"
)

// getenv falls back to the seeded operator
// (cases/backoffice/env/seeds/sql-server/001-test-operator.sql).
func getenv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return map[string]string{"LODOG_UPSTREAM_USER": "dev-admin", "LODOG_UPSTREAM_PASSWORD": "password123"}[key]
}

func TestGatewayL0Feature(t *testing.T) {
	t.Parallel()
	cfg, err := casecfg.LoadConfig("../../cases/backoffice/gateway/L0.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Case = "../../" + cfg.Case
	// Straight to Backoffice: the acceptance test checks the gateway, not the proxy.
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

	call := func(t *testing.T, name string, args map[string]any) (bool, string) {
		t.Helper()
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.IsError, res.Content[0].(*mcp.TextContent).Text
	}

	t.Run("every operation the document declares is listed", func(t *testing.T) {
		doc, err := casecfg.LoadDocument(cfg)
		if err != nil {
			t.Fatal(err)
		}
		planned, skipped := serving.RawTools(doc)
		for _, s := range skipped {
			t.Errorf("%s %s skipped: %s", s.Op.Method, s.Op.Path, s.Reason)
		}
		want := make(map[string]bool, len(planned))
		for _, p := range planned {
			want[p.Tool.Name] = true
		}
		var n int
		for tool, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			if !want[tool.Name] {
				t.Errorf("listed %s, which the plan does not serve", tool.Name)
			}
			n++
		}
		if n != len(doc.Operations) || n != len(planned) {
			t.Fatalf("tools = %d, want one per operation (%d operations, %d planned)", n, len(doc.Operations), len(planned))
		}
	})

	t.Run("T02: alice's latest five deposits come back newest first", func(t *testing.T) {
		isErr, text := call(t, "get_api_payment_transactions", map[string]any{
			"TransactionType": 1, "FuzzySearch": "bench_alice", "PageNumber": 1, "PageSize": 5,
		})
		if isErr {
			t.Fatalf("tool error: %s", text)
		}
		want := []string{"BENCH-D-ALICE-07", "BENCH-D-ALICE-06", "BENCH-D-ALICE-05", "BENCH-D-ALICE-04", "BENCH-D-ALICE-03"}
		last := -1
		for _, ref := range want {
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

	t.Run("T12: a call without the required paging is refused by the gateway with a next step", func(t *testing.T) {
		isErr, text := call(t, "get_api_payment_transactions", map[string]any{"TransactionType": 2, "FuzzySearch": "bench_carol"})
		if !isErr || !strings.Contains(text, "PageSize") {
			t.Fatalf("want the gateway to refuse the missing required PageSize, got %v: %s", isErr, text)
		}
	})

	t.Run("a path parameter reaches the right customer", func(t *testing.T) {
		isErr, text := call(t, "get_api_customer_by_customerId", map[string]any{"customerId": 910001})
		if isErr || !strings.Contains(text, "bench_alice") {
			t.Fatalf("got %v: %s", isErr, text)
		}
	})
}
