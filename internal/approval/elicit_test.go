package approval

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTheApproverIsNamedByTheClientAndDefaultsToMCPUser(t *testing.T) {
	for name, tc := range map[string]struct {
		init *mcp.InitializeParams
		want string
	}{
		"no params":      {nil, "elicitation:mcp-user"},
		"no client info": {&mcp.InitializeParams{}, "elicitation:mcp-user"},
		"blank name":     {&mcp.InitializeParams{ClientInfo: &mcp.Implementation{Name: "  "}}, "elicitation:mcp-user"},
		"claude code":    {&mcp.InitializeParams{ClientInfo: &mcp.Implementation{Name: "claude-code"}}, "elicitation:claude-code"},
		"odd characters": {&mcp.InitializeParams{ClientInfo: &mcp.Implementation{Name: "my client\nx"}}, "elicitation:my-client-x"},
		"long name":      {&mcp.InitializeParams{ClientInfo: &mcp.Implementation{Name: strings.Repeat("a", 200)}}, "elicitation:" + strings.Repeat("a", 64)},
	} {
		if got := elicitIdentity(tc.init); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
