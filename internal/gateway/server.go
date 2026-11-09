// Package gateway is lodog's product: an MCP server that stands in front of a
// legacy API. L0 to L3 are one gateway with different configuration
// (docs/contracts.md §2); casecfg reads that configuration and this package
// serves it.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/iml885203/lodog/internal/approval"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/redact"
	"github.com/iml885203/lodog/internal/spec"
)

// MCPPath is where an agent connects.
const MCPPath = "/mcp"

// Gateway is one configured lodog gateway.
type Gateway struct {
	cfg       *casecfg.Config
	server    *mcp.Server
	tools     []serving.RawTool
	tasks     []serving.TaskTool
	skipped   []serving.Skipped
	approvals *approval.Store
	hub       *approval.Hub
	log       *slog.Logger
}

// New builds the gateway for cfg. getenv reads the secrets the auth block
// names (os.Getenv outside tests): they come from the environment, never from
// the config file. labels are the default benchmark labels for clients that
// cannot send _meta.
func New(cfg *casecfg.Config, getenv func(string) string, labels Labels, version string, log *slog.Logger) (*Gateway, error) {
	if err := cfg.Placeholders(); err != nil {
		return nil, err
	}
	doc, err := casecfg.LoadDocument(cfg)
	if err != nil {
		return nil, err
	}
	upstream, err := NewUpstreamFor(cfg, getenv)
	if err != nil {
		return nil, err
	}
	if n := upstream.Redaction().Short(); n > 0 {
		log.Warn("some upstream secrets are shorter than the masking minimum: they are masked in errors and error pages but not in successful responses, where they would match ordinary data; use a longer secret",
			"short_secrets", n, "minimum", redact.MinDataValueLen)
	}
	approvals := approval.NewStore()
	g := &Gateway{cfg: cfg, approvals: approvals, hub: approval.NewHub(approvals, log), log: log}
	fwd := forwarder{upstream: upstream, level: cfg.Level, identity: cfg.Identity, labels: labels, log: log}
	if cfg.Level.ServesTasks() {
		err = g.serveTasks(doc, fwd, version)
	} else {
		g.serveOperations(doc, fwd, version)
	}
	if err != nil {
		return nil, err
	}
	for _, s := range g.skipped {
		log.Warn("operation not served", "method", s.Op.Method, "path", s.Op.Path, "reason", s.Reason)
	}
	return g, nil
}

// serveOperations is L0 and L1: one tool per operation, nothing enforced.
func (g *Gateway) serveOperations(doc *spec.Document, fwd forwarder, version string) {
	g.server = mcp.NewServer(&mcp.Implementation{Name: "lodog", Version: version}, &mcp.ServerOptions{Logger: g.log})
	g.tools, g.skipped = serving.RawTools(doc)
	for _, t := range g.tools {
		g.server.AddTool(mcpTool(t.Tool), fwd.handler(t))
	}
}

// serveTasks is L2 and L3: the case's workflows as task tools, with the
// workflows' description as the server instructions. L3 adds the policy.
func (g *Gateway) serveTasks(doc *spec.Document, fwd forwarder, version string) error {
	p, err := serving.LoadPolicy(g.cfg)
	if err != nil {
		return err
	}
	if p != nil {
		for _, w := range p.Warnings() {
			g.log.Warn("policy " + g.cfg.PolicyPath() + ": " + w)
		}
	}
	plan, err := serving.LoadTasks(g.cfg, doc, p)
	if err != nil {
		return err
	}
	g.skipped = plan.Skipped
	if p != nil {
		fwd.enforcer = &enforcer{approvals: g.approvals, identity: g.cfg.Identity, hub: g.hub, log: g.log}
		if g.elicits() {
			fwd.enforcer.asker = approval.NewAsker(g.cfg.Approvals.ElicitationTimeoutOrDefault(), g.log)
		}
	}
	g.server = mcp.NewServer(&mcp.Implementation{Name: "lodog", Version: version}, &mcp.ServerOptions{
		Logger: g.log, Instructions: plan.Workflow.Info.Description,
	})
	for _, s := range plan.Served {
		t := governedTask{Served: s}
		if t.Decision.Action == policy.Approval {
			t.Tool = withApprovalArg(t.Tool, t.Decision.Risk)
		}
		g.server.AddTool(mcpTool(t.Tool), fwd.taskHandler(t, time.Now))
		g.tasks = append(g.tasks, t.TaskTool)
	}
	return nil
}

// AdminHandler lists what is waiting, on the approvals listener. It cannot
// approve or deny (see approval.AdminHandler).
func (g *Gateway) AdminHandler() http.Handler { return approval.AdminHandler(g.approvals) }

// DecidePending gives every pending request a verdict, for a host that plays
// the operator in-process (see approval.Store.DecidePending).
func (g *Gateway) DecidePending(approve func(req approval.Request) bool, by string) ([]approval.Request, error) {
	return g.approvals.DecidePending(approve, by, g.log)
}

// Pending lists the held calls nobody has decided yet, for a host that plays
// the operator in-process and wants to record what the gateway stopped.
func (g *Gateway) Pending() []approval.Request { return g.approvals.Pending() }

// ApprovalHandler is what a channel's Listen is given: it applies a verdict
// the channel received.
func (g *Gateway) ApprovalHandler() func(context.Context, approval.Verdict) approval.Answer {
	return g.hub.Handle
}

// SetNotifier installs the channel humans approve through. Call it before Serve.
func (g *Gateway) SetNotifier(n approval.Notifier) { g.hub.Use(n) }

// elicits reports whether held calls are put to the user at the MCP client.
func (g *Gateway) elicits() bool {
	return g.cfg.Level == casecfg.L3 && !g.cfg.Approvals.InProcess && g.cfg.Approvals.Resolved() == casecfg.ModeElicitation
}

// Tools reports the one-to-one tools served at L0 and L1, and what was skipped.
func (g *Gateway) Tools() ([]serving.RawTool, []serving.Skipped) { return g.tools, g.skipped }

// Tasks reports the task tools served at L2 and L3.
func (g *Gateway) Tasks() []serving.TaskTool { return g.tasks }

// MCPServer exposes the server for in-process clients (tests, the bench runner).
func (g *Gateway) MCPServer() *mcp.Server { return g.server }

// Handler is the HTTP surface: Streamable HTTP MCP at MCPPath.
func (g *Gateway) Handler() http.Handler {
	// Asking the user is a request from the server to the client in the middle
	// of a tool call, which needs a session and a streamed response. Every
	// other mode keeps the sessionless JSON form.
	streaming := g.elicits()
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server },
		&mcp.StreamableHTTPOptions{Stateless: !streaming, JSONResponse: !streaming, Logger: g.log})
	mux := http.NewServeMux()
	mux.Handle(MCPPath, capProtocol(streamable))
	return mux
}

// Serve listens on cfg.Listen, and at L3 also on cfg.Approvals.Listen, until
// ctx is cancelled or either listener fails.
func (g *Gateway) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	run := func(addr string, h http.Handler, what string) error {
		lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			_ = srv.Shutdown(shutdown)
		}()
		go func() {
			if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", what, err)
				return
			}
			errs <- nil
		}()
		g.log.Info(what+" serving", "addr", lis.Addr().String())
		return nil
	}
	listeners := 1
	if err := run(g.cfg.Listen, g.Handler(), "mcp"); err != nil {
		return err
	}
	if g.cfg.Level == casecfg.L3 {
		listeners++
		if err := run(g.cfg.Approvals.Listen, g.AdminHandler(), "approvals"); err != nil {
			return err
		}
	}
	if n := g.hub.Channel(); n != nil {
		listeners++
		go func() {
			err := n.Listen(ctx, g.hub.Handle)
			if err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("approval channel: %w", err)
				return
			}
			errs <- nil
		}()
	}
	g.log.Info("gateway ready", "mcp", "http://"+g.cfg.Listen+MCPPath, "level", g.cfg.Level,
		"tools", len(g.tools)+len(g.tasks), "skipped", len(g.skipped))
	var first error
	for range listeners {
		if err := <-errs; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}
