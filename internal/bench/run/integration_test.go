package run

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
)

const fakeClaudeEnv = "LODOG_TEST_FAKE_CLAUDE"

// TestFakeClaude is the agent the integration test runs: this test binary,
// started by a one-line script in place of `claude`. It answers like the real
// CLI does for --version and for the yes/no judge (--output-format json), and
// for an agent turn it connects to the gateway named in --mcp-config, calls
// the first tool, then prints a recorded stream-json transcript.
func TestFakeClaude(t *testing.T) {
	if os.Getenv(fakeClaudeEnv) == "" {
		t.Skip("runs only as the fake claude executable")
	}
	args := os.Args
	args = args[indexOf(args, "--")+1:]
	switch {
	case has(args, "--version"):
		_, _ = os.Stdout.WriteString("9.9.9 (Claude Code)\n")
	case has(args, "json") && !has(args, "stream-json"):
		_, _ = os.Stdout.WriteString(`{"type":"result","is_error":false,"result":"NO"}` + "\n")
	default:
		callFirstGatewayTool(t, valueOf(args, "--mcp-config"))
		stream, err := os.ReadFile(os.Getenv(fakeClaudeEnv)) //nolint:gosec // G703: the test names its own fixture
		if err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.Write(stream)
	}
	os.Exit(0)
}

func callFirstGatewayTool(t *testing.T, mcpConfig string) {
	t.Helper()
	data, err := os.ReadFile(mcpConfig) //nolint:gosec // G304: the path is the runner's own mcp.json
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct{ URL string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "fake-claude", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: cfg.MCPServers[ServerName].URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools = %v, %v", tools, err)
	}
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tools.Tools[0].Name}); err != nil {
		t.Fatal(err)
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func has(args []string, want string) bool { return indexOf(args, want) >= 0 }

func valueOf(args []string, flag string) string {
	if i := indexOf(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// fakeClaudeBin writes the executable the runner is told is `claude`.
func fakeClaudeBin(t *testing.T, stream string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	// The race detector sleeps a second at exit unless told not to.
	script := fmt.Sprintf("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec %q -test.run='^TestFakeClaude$' -- \"$@\"\n", self)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // G306: the test runs this file
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeEnv, stream)
	return bin
}

func freeTestAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

type legacy struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []http.Header
}

func (l *legacy) calls() []http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]http.Header(nil), l.got...)
}

func newLegacy(t *testing.T) *legacy {
	t.Helper()
	l := &legacy{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ping" {
			http.NotFound(w, r)
			return
		}
		l.mu.Lock()
		l.got = append(l.got, r.Header.Clone())
		l.mu.Unlock()
		_, _ = w.Write([]byte(`{"pong":true}`))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func gatewayCase(t *testing.T, upstream string) *casecfg.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	doc := `{"openapi":"3.0.4","info":{"title":"legacy","version":"1"},"paths":{"/api/ping":{"get":{"summary":"Ping"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LODOG_TEST_KEY", "legacy-secret")
	return &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: freeTestAddr(t), Upstream: upstream,
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "LODOG_TEST_KEY", Header: "X-Api-Key"},
		Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
}

// The runner's own wiring, end to end: newDeps writes the MCP config and asks
// the agent binary for its version; for every attempt StartGateway serves a
// real gateway in front of a stand-in legacy system, the agent (a fake claude
// printing a recorded transcript) calls a tool through it, and the gateway is
// stopped so the next attempt can use the same port.
func TestARunDrivesTheAgentThroughARealGatewayToTheLegacySystem(t *testing.T) {
	system := newLegacy(t)
	cfg := gatewayCase(t, system.srv.URL)
	o := Options{
		ClaudeBin: fakeClaudeBin(t, filepath.Join("testdata", "keycloak-L3-T03.stream.jsonl")),
		RunsDir:   t.TempDir(), RunID: "it-1", NoReset: true, Attempts: 2, AttemptTimeout: time.Minute, Version: "test", Commit: "0123abc",
	}
	abs, err := filepath.Abs(os.Getenv(fakeClaudeEnv))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeEnv, abs)
	c := score.Case{
		Tasks:  map[string]score.Task{"T01": {ID: "T01", Prompt: "ping the system"}},
		Rubric: score.Rubric{RunsPerTask: 2, AgentModel: "m", QuestionJudgeModel: "j", Reply: "go on", MaxReplies: 1},
	}
	plan, err := newPlan(o, c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	deps, err := newDeps(t.Context(), o, &plan, cfg, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Header.ClaudeVersion != "9.9.9 (Claude Code)" {
		t.Errorf("claude version = %q, want what the binary printed", plan.Header.ClaudeVersion)
	}
	if err := Run(t.Context(), plan, deps); err != nil {
		t.Fatal(err)
	}

	assertRun(t, plan, cfg, system)
}

func assertRun(t *testing.T, plan Plan, cfg *casecfg.Config, system *legacy) {
	t.Helper()
	mcpJSON, err := os.ReadFile(filepath.Join(plan.RunDir, "mcp.json"))
	if err != nil || !strings.Contains(string(mcpJSON), `"url": "http://`+cfg.Listen+gateway.MCPPath+`"`) {
		t.Errorf("mcp.json = %s, %v: it must point the agent at the gateway", mcpJSON, err)
	}
	m := readMeta(t, plan.RunDir)
	if len(m.Attempts) != 2 || m.Attempts[0].Status != StatusDone || m.Attempts[1].Status != StatusDone {
		t.Fatalf("attempts = %+v", m.Attempts)
	}
	if m.LodogCommit != "0123abc" {
		t.Errorf("lodog commit = %q: meta.yaml must say which lodog made the run", m.LodogCommit)
	}
	if m.ClaudeVersion != "9.9.9 (Claude Code)" || m.Attempts[0].Usage.OutputTokens != 302 || m.Attempts[0].SessionID == "" {
		t.Errorf("meta = %+v: the recorded turn's usage and session are kept", m)
	}
	if m.Attempts[0].Replies != 0 || len(m.Attempts[0].Verdicts) != 1 {
		t.Errorf("attempt = %+v: the judge said the agent asked nothing", m.Attempts[0])
	}
	if _, err := os.Stat(filepath.Join(plan.RunDir, m.Attempts[0].Transcript)); err != nil {
		t.Errorf("transcript: %v", err)
	}

	assertLegacyCalls(t, cfg, system)
}

func assertLegacyCalls(t *testing.T, cfg *casecfg.Config, system *legacy) {
	t.Helper()
	calls := system.calls()
	if len(calls) != 2 {
		t.Fatalf("the legacy system saw %d calls, want one per attempt", len(calls))
	}
	for i, h := range calls {
		attempt := fmt.Sprint(i + 1)
		if h.Get("X-Api-Key") != "legacy-secret" || h.Get("X-Lodog-Run") != "it-1" || h.Get("X-Lodog-Task") != "T01" || h.Get("X-Lodog-Attempt") != attempt {
			t.Errorf("call %d headers = %v: the gateway signs in and labels the call with its attempt", i+1, h)
		}
	}
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", cfg.Listen); err == nil {
		_ = conn.Close()
		t.Error("the gateway still listens after the run")
	}
}

func TestStartGatewayRefusesAConfigItCannotServe(t *testing.T) {
	cfg := gatewayCase(t, "http://127.0.0.1:1")
	t.Setenv("LODOG_TEST_KEY", "")
	if _, err := StartGateway(t.Context(), cfg, gateway.Labels{}, "test", slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "LODOG_TEST_KEY") {
		t.Fatalf("err = %v, want the missing secret named", err)
	}
}

func TestNewDepsRefusesAnAgentBinaryThatCannotRun(t *testing.T) {
	p := testPlan(t)
	cfg := &casecfg.Config{Listen: "127.0.0.1:1"}
	_, err := newDeps(t.Context(), Options{ClaudeBin: "/nonexistent/claude"}, &p, cfg, nil, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("err = %v, want a preflight error", err)
	}
}
