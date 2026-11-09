package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const secret = "s3cret-pass"

// fakeLegacy stands in for a cookie-session legacy API. It is a third-party
// port, so substituting it is allowed (CLAUDE.md, Tests).
type fakeLegacy struct {
	mu          sync.Mutex
	logins      int
	refuseLogin bool
	expireOnce  bool
	calls       []*http.Request
	// arrive runs when a non-login request reaches the legacy system, before
	// it answers. A test uses it to hold or drop the call.
	arrive func()
}

func (f *fakeLegacy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.arrive != nil && r.URL.Path != "/api/operator/login" {
		f.arrive()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/operator/login" {
		f.logins++
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.refuseLogin || body["Password"] != secret {
			_, _ = io.WriteString(w, `{"errorCode":2,"message":"PasswordIncorrect"}`)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: ".AspNetCore.Session", Value: "sess", Path: "/",
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		_, _ = io.WriteString(w, `{"errorCode":1}`)
		return
	}
	f.calls = append(f.calls, r.Clone(r.Context()))
	if c, err := r.Cookie(".AspNetCore.Session"); err != nil || c.Value != "sess" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.expireOnce {
		f.expireOnce = false
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("PageSize") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorCode":400,"message":"Bad Request"}`)
		return
	}
	_, _ = io.WriteString(w, `{"data":{"paginationData":[{"refNo":"BENCH-D-ALICE-07"}]}}`)
}

const testDoc = `{"openapi":"3.0.4","paths":{"/api/payment/transactions":{"get":{"parameters":[
	{"name":"PageSize","in":"query","schema":{"type":"integer"}},
	{"name":"FuzzySearch","in":"query","schema":{"type":"string"}}]}}}}`

type harness struct {
	legacy  *fakeLegacy
	session *mcp.ClientSession
	logs    *lockedBuffer
}

func newHarness(t *testing.T, legacy *fakeLegacy) *harness {
	t.Helper()
	return newHarnessWithLabels(t, legacy, Labels{})
}

func newHarnessWithLabels(t *testing.T, legacy *fakeLegacy, labels Labels) *harness {
	t.Helper()
	upstream := httptest.NewTLSServer(legacy)
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(testDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL, UpstreamInsecureTLS: true,
		Auth: casecfg.AuthConfig{
			Type: casecfg.AuthCookieSession, Login: "POST /api/operator/login",
			UsernameField: "LoginName", PasswordField: "Password", UsernameEnv: "U", PasswordEnv: "P",
			Success: casecfg.SuccessConfig{CookieName: ".AspNetCore.Session"},
		},
		Identity: casecfg.IdentityConfig{Actor: "agent:test", OnBehalfOf: "user:alice"},
	}
	logs := &lockedBuffer{}
	env := map[string]string{"U": "bot", "P": secret}
	gw, err := New(cfg, func(k string) string { return env[k] }, labels, "test", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(httpSrv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpSrv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &harness{legacy: legacy, session: session, logs: logs}
}

func (h *harness) call(t *testing.T, args map[string]any, meta mcp.Meta) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_api_payment_transactions", Arguments: args, Meta: meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, res.Content[0].(*mcp.TextContent).Text
}

func TestAToolCallSignsInOnceAndForwards(t *testing.T) {
	h := newHarness(t, &fakeLegacy{})
	for range 2 {
		res, text := h.call(t, map[string]any{"PageSize": 5, "FuzzySearch": "bench_alice"}, nil)
		if res.IsError || !strings.Contains(text, "BENCH-D-ALICE-07") {
			t.Fatalf("result %v: %s", res.IsError, text)
		}
	}
	if h.legacy.logins != 1 {
		t.Fatalf("logins = %d, want 1: the session is reused", h.legacy.logins)
	}
	if got := h.legacy.calls[0].URL.Query().Get("FuzzySearch"); got != "bench_alice" {
		t.Fatalf("FuzzySearch forwarded as %q", got)
	}
}

func TestEveryForwardedCallCarriesTheTraceHeaders(t *testing.T) {
	h := newHarness(t, &fakeLegacy{})
	h.call(t, map[string]any{"PageSize": 5}, mcp.Meta{"lodog/run": "r1", "lodog/task": "T02", "lodog/attempt": 3})
	got := h.legacy.calls[0].Header
	want := map[string]string{
		"X-Lodog-Level": "L0", "X-Lodog-Actor": "agent:test", "X-Lodog-On-Behalf-Of": "user:alice",
		"X-Lodog-Run": "r1", "X-Lodog-Task": "T02", "X-Lodog-Attempt": "3",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestARefusedLoginIsNeverRetried(t *testing.T) {
	h := newHarness(t, &fakeLegacy{refuseLogin: true})
	for range 3 {
		res, text := h.call(t, map[string]any{"PageSize": 5}, nil)
		if !res.IsError || !strings.Contains(text, "Next:") {
			t.Fatalf("want an actionable error, got %v: %s", res.IsError, text)
		}
	}
	if h.legacy.logins != 1 {
		t.Fatalf("logins = %d, want 1: Backoffice locks an operator after five refusals", h.legacy.logins)
	}
}

func TestAnExpiredSessionSignsInAgainOnce(t *testing.T) {
	h := newHarness(t, &fakeLegacy{expireOnce: true})
	res, text := h.call(t, map[string]any{"PageSize": 5}, nil)
	if res.IsError {
		t.Fatalf("want success after one re-login: %s", text)
	}
	if h.legacy.logins != 2 {
		t.Fatalf("logins = %d, want 2", h.legacy.logins)
	}
}

func TestALegacyErrorReachesTheAgentAsAToolError(t *testing.T) {
	h := newHarness(t, &fakeLegacy{})
	res, text := h.call(t, map[string]any{"FuzzySearch": "bench_carol"}, nil)
	if !res.IsError || !strings.HasPrefix(text, "HTTP 400") {
		t.Fatalf("want the 400 passed through as an error, got %v: %s", res.IsError, text)
	}
}

func TestTheUpstreamPasswordNeverReachesTheLog(t *testing.T) {
	h := newHarness(t, &fakeLegacy{})
	h.call(t, map[string]any{"PageSize": 5}, nil)
	if strings.Contains(h.logs.String(), secret) {
		t.Fatal("the gateway logged its upstream password")
	}
}

func TestDefaultLabelsFillInWhatMetaDoesNotSay(t *testing.T) {
	h := newHarnessWithLabels(t, &fakeLegacy{}, Labels{Run: "smoke", Task: "T02", Attempt: "1"})
	h.call(t, map[string]any{"PageSize": 5}, mcp.Meta{"lodog/task": "T05"})
	got := h.legacy.calls[0].Header
	if got.Get("X-Lodog-Run") != "smoke" || got.Get("X-Lodog-Attempt") != "1" {
		t.Errorf("defaults not applied: run=%q attempt=%q", got.Get("X-Lodog-Run"), got.Get("X-Lodog-Attempt"))
	}
	if got.Get("X-Lodog-Task") != "T05" {
		t.Errorf("task = %q: a label in _meta must win over the default", got.Get("X-Lodog-Task"))
	}
}

func TestAMissingCaseFolderSaysWhereItLooked(t *testing.T) {
	cfg := &casecfg.Config{Case: "no-such-case", Level: casecfg.L0}
	_, err := casecfg.LoadDocument(cfg)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "relative to the directory lodog runs in") {
		t.Fatalf("err = %v, want the not-exist error plus a hint about the working directory", err)
	}
}
