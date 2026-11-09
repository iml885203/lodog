package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/iml885203/lodog/internal/redact"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Elicitor is the part of an MCP server session the gateway asks the user
// through. *mcp.ServerSession is one.
type Elicitor interface {
	Elicit(ctx context.Context, params *mcp.ElicitParams) (*mcp.ElicitResult, error)
	InitializeParams() *mcp.InitializeParams
}

// elicitIdentityPrefix marks a verdict the user gave at their MCP client.
const elicitIdentityPrefix = "elicitation:"

// AskVerdict is how asking the user came out.
type AskVerdict int

// The ways asking the user can come out.
const (
	Unanswered AskVerdict = iota // could not ask, cancelled, or timed out: stays pending
	Accepted
	Declined
)

// Asker puts a held call to the person at the MCP client. The model
// cannot answer: the question travels as a request from the server to the
// client's own interface, not as a tool result.
type Asker struct {
	timeout time.Duration
	log     *slog.Logger
}

// NewAsker asks the user at the MCP client, giving up after timeout.
func NewAsker(timeout time.Duration, log *slog.Logger) *Asker {
	return &Asker{timeout: timeout, log: log}
}

// Ask prompts once and says what came of it. why explains an unanswered
// prompt to the agent; by names the approver for an answered one.
func (u Asker) Ask(ctx context.Context, sess Elicitor, n Notice) (verdict AskVerdict, by, why string) {
	if sess == nil {
		return Unanswered, "", "this connection cannot carry a prompt to the user"
	}
	init := sess.InitializeParams()
	if init == nil || init.Capabilities == nil || init.Capabilities.Elicitation == nil {
		return Unanswered, "", "the MCP client does not support elicitation"
	}
	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()
	res, err := sess.Elicit(ctx, &mcp.ElicitParams{
		Mode:    "form",
		Message: elicitMessage(n),
		// Nothing to fill in: the user only confirms.
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return Unanswered, "", "the user did not answer in time"
	case err != nil:
		u.log.Info("the user could not be asked", "id", n.ID, "error", err.Error())
		return Unanswered, "", "the user could not be asked: " + err.Error()
	}
	switch res.Action {
	case "accept":
		return Accepted, elicitIdentity(init), ""
	case "decline":
		return Declined, elicitIdentity(init), ""
	}
	return Unanswered, "", "the user dismissed the prompt"
}

// elicitIdentity names the approver as far as the client says who it is. It
// is the person at that client, not a verified account.
func elicitIdentity(init *mcp.InitializeParams) string {
	name := "mcp-user"
	if init != nil && init.ClientInfo != nil {
		if n := cleanIdentity(init.ClientInfo.Name); n != "" {
			name = n
		}
	}
	return elicitIdentityPrefix + name
}

func cleanIdentity(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// elicitMessage is the question the user sees: one screen, plain text.
func elicitMessage(n Notice) string {
	what := n.What
	if what == "" {
		what = n.Method + " " + n.Target
	}
	who := n.OnBehalfOf
	if who == "" {
		who = n.Actor
	}
	shown := redact.PreviewJSON(n.Arguments, true)
	lines := []string{
		fmt.Sprintf("%s（%s）的 agent 想要：%s", who, n.Actor, what),
		fmt.Sprintf("對象：%s %s", n.Method, n.Target),
		fmt.Sprintf("風險：%s", n.Risk),
	}
	if n.Reason != "" {
		lines = append(lines, "agent 自述的理由（不是 gateway 查證過的）："+n.Reason)
	}
	lines = append(lines, "完整參數：\n"+shown, "核准後這個呼叫只執行一次。")
	return strings.Join(lines, "\n")
}
