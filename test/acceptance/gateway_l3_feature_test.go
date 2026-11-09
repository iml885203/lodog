//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/approval"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/gateway"
)

func TestGatewayL3Feature(t *testing.T) {
	t.Parallel()
	cfg, err := casecfg.LoadConfig("../../cases/backoffice/gateway/L3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Approvals.InProcess = true // this test plays the operator through DecidePending
	cfg.Case = "../../" + cfg.Case
	cfg.Upstream, cfg.UpstreamInsecureTLS = "https://localhost:7144", true
	gw, err := gateway.New(cfg, getenv, gateway.Labels{}, "acceptance", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	mcpSrv := httptest.NewServer(gw.Handler())
	defer mcpSrv.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: mcpSrv.URL + gateway.MCPPath}, nil)
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
	// The log this test creates is marked so it can be told apart.
	remark := fmt.Sprintf("lodog-acceptance-%d", time.Now().UnixNano())
	window := map[string]any{
		"customerId": 910004,
		"from":       time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"to":         time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	hasLog := func(t *testing.T) bool {
		t.Helper()
		isErr, text := call(t, "list_crm_logs", window)
		if isErr {
			t.Fatalf("read crm logs: %s", text)
		}
		return strings.Contains(text, remark)
	}
	create := map[string]any{
		"customerId": 910004, "productType": 0, "issueType": 9, "channelType": 1,
		"language": 0, "issueDescription": "acceptance: approval flow", "response": "-", "remark": remark,
	}

	t.Run("L3 serves L2's task tools, and none reaches the GET that resets a password", func(t *testing.T) {
		n := 0
		for tool, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			n++
			if strings.Contains(tool.Description, "resetPassword") || strings.Contains(tool.Description, "TestDelete") {
				t.Fatalf("%s calls a hidden operation", tool.Name)
			}
		}
		if n != len(gw.Tasks()) || n == 0 || n > 25 {
			t.Fatalf("%d tools offered", n)
		}
	})

	t.Run("a read still goes straight through", func(t *testing.T) {
		isErr, text := call(t, "list_deposits", map[string]any{"search": "bench_alice", "pageSize": 5})
		if isErr || !strings.Contains(text, "BENCH-D-ALICE-07") {
			t.Fatalf("%v: %s", isErr, text)
		}
	})

	var id string
	t.Run("a write is held and does not reach Backoffice", func(t *testing.T) {
		isErr, text := call(t, "add_crm_log", create)
		if isErr || !strings.HasPrefix(text, "NOT EXECUTED") {
			t.Fatalf("want held, got %v: %s", isErr, text)
		}
		id = regexp.MustCompile(`apr_[0-9a-f]{16}`).FindString(text)
		if hasLog(t) {
			t.Fatal("the held write reached Backoffice")
		}
	})

	t.Run("once a human approves, the same call runs exactly once", func(t *testing.T) {
		decided, err := gw.DecidePending(func(r approval.Request) bool { return r.ID == id }, "user:acceptance")
		if err != nil || len(decided) != 1 || decided[0].Status != approval.Approved {
			t.Fatalf("approve: %v %+v", err, decided)
		}
		approved := map[string]any{"approvalId": id}
		for k, v := range create {
			approved[k] = v
		}
		if isErr, text := call(t, "add_crm_log", approved); isErr || !strings.Contains(text, `"status":"ok"`) {
			t.Fatalf("approved write: %v %s", isErr, text)
		}
		if !hasLog(t) {
			t.Fatal("the approved write did not land in Backoffice")
		}
		if isErr, _ := call(t, "add_crm_log", approved); !isErr {
			t.Fatal("the approval was spent twice")
		}
	})
}
