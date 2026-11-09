package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/record"
)

// Every secret the gateway holds is a distinct sentinel, so a leak names its
// source. None may appear in any place the gateway or the proxy writes, or in
// anything an agent can read (CLAUDE.md: credentials never reach a record).
var sentinels = map[string]string{
	"USER": "bot-operator", "PASS": "test-pass-00000000", "TOKEN": "test-token-00000000",
	"KEY": "test-key-00000000", "CID": "test-id-00000000", "CSECRET": "test-secret-11111111",
	"ISSUED": "test-issued-00000000",
}

var authSchemes = map[string]casecfg.AuthConfig{
	"cookie-session": {
		Type: casecfg.AuthCookieSession, Login: "POST /login", UsernameEnv: "USER", PasswordEnv: "PASS",
		Success: casecfg.SuccessConfig{CookieName: "sid"},
	},
	"bearer": {Type: casecfg.AuthBearer, TokenEnv: "TOKEN"},
	"bearer-login": {
		Type: casecfg.AuthBearer, Login: "POST /login", UsernameEnv: "USER", PasswordEnv: "PASS", TokenPath: "token",
	},
	"api-key header": {Type: casecfg.AuthAPIKey, KeyEnv: "KEY", Header: "X-Api-Key"},
	"api-key query":  {Type: casecfg.AuthAPIKey, KeyEnv: "KEY", Query: "api_key"},
	"oauth2": {
		Type: casecfg.AuthOAuth2, TokenURL: "/token", ClientIDEnv: "CID", ClientSecretEnv: "CSECRET", ClientAuth: casecfg.ClientAuthBody,
	},
}

// echoLegacy is a legacy system that echoes the request back as a framework
// error page or a debug payload does, in the shape mode names, and issues
// sentinel sessions and tokens. Every token it issues is new, so a renewal
// is visible. It answers 401 to the first call of a renewable credential when
// expireFirst is set.
type echoLegacy struct {
	mode        string // text500 | json500 | json200
	expireFirst bool

	mu        sync.Mutex
	issued    int
	asked     int
	loginBody string
}

func (e *echoLegacy) token() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.issued++
	return fmt.Sprintf("SENTINEL-issued-%d", e.issued)
}

func (e *echoLegacy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login":
		raw, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.loginBody = string(raw)
		e.mu.Unlock()
		tok := e.token()
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: tok, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		_, _ = io.WriteString(w, `{"token":"`+tok+`"}`)
		return
	case "/token":
		_, _ = io.WriteString(w, `{"access_token":"`+e.token()+`","expires_in":3600}`)
		return
	}
	e.mu.Lock()
	e.asked++
	first := e.asked == 1
	e.mu.Unlock()
	if first && e.expireFirst {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	e.mu.Lock()
	login := e.loginBody
	e.mu.Unlock()
	auth := r.Header.Get("Authorization")
	cookie := r.Header.Get("Cookie")
	// A bare credential in prose: only a match on the value can find it.
	bare := strings.TrimPrefix(auth, "Bearer ")
	if bare == "" {
		bare = r.Header.Get("X-Api-Key")
	}
	if bare == "" {
		bare = r.URL.Query().Get("api_key")
	}
	// What a careless login handler logs: the password it was sent.
	bare += " after login " + login
	if e.mode == "text500" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, html.EscapeString(strings.Join([]string{
			"failed: " + r.URL.String(), "Authorization: " + auth, "X-Api-Key: " + r.Header.Get("X-Api-Key"),
			"Cookie: " + cookie, "rejected credential " + bare,
		}, "\n")))
		return
	}
	body, _ := json.Marshal(map[string]any{
		"url": r.URL.String(), "query": r.URL.Query(), "note": "rejected credential " + bare,
		"headers": map[string]string{"Authorization": auth, "X-Api-Key": r.Header.Get("X-Api-Key"), "Cookie": cookie},
	})
	w.Header().Set("Content-Type", "application/json")
	if e.mode == "json500" {
		w.WriteHeader(http.StatusInternalServerError)
	}
	_, _ = w.Write(body)
}

type captureSink struct {
	mu    sync.Mutex
	calls []record.Call
}

func (c *captureSink) Append(call record.Call) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return nil
}

func (c *captureSink) text(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.Marshal(c.calls)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var sentinelPattern = regexp.MustCompile(`SENTINEL-[a-z0-9-]+`)

// leaks reports every sentinel found in what, held or issued.
func leaks(what string) []string { return sentinelPattern.FindAllString(what, -1) }

func startGateway(t *testing.T, auth casecfg.AuthConfig, upstream string) (*mcp.ClientSession, *lockedBuffer) {
	t.Helper()
	return startGatewayEnv(t, auth, upstream, sentinels)
}

func startGatewayEnv(t *testing.T, auth casecfg.AuthConfig, upstream string, env map[string]string) (*mcp.ClientSession, *lockedBuffer) {
	t.Helper()
	return startGatewayDoc(t, auth, upstream, env, "")
}

func startGatewayDoc(t *testing.T, auth casecfg.AuthConfig, upstream string, env map[string]string, docFile string) (*mcp.ClientSession, *lockedBuffer) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if docFile == "" {
		if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(testDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(docFile, filepath.Join(dir, "spec", "openapi.json")); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream, Auth: auth,
		Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	gw, err := New(cfg, func(k string) string { return env[k] }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, logs
}

func callTransactions(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	text, isErr := callTransactionsResult(t, s)
	if !isErr {
		t.Fatalf("want a tool error, got %s", text)
	}
	return text
}

func callTransactionsResult(t *testing.T, s *mcp.ClientSession) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_api_payment_transactions", Arguments: map[string]any{"PageSize": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	return string(b), res.IsError
}

// closedURL is an address nothing listens on, so every call fails to connect.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

// The failure every outage produces: Go wraps the transport error in a
// *url.Error that spells out the whole URL, query credential included.
func TestNoAuthTypeLeaksItsSecretWhenTheLegacySystemCannotBeReached(t *testing.T) {
	for name, auth := range authSchemes {
		t.Run(name, func(t *testing.T) {
			s, logs := startGateway(t, auth, closedURL(t))
			result := callTransactions(t, s)
			if got := leaks(result); len(got) > 0 {
				t.Errorf("tool result leaks %v: %s", got, result)
			}
			if got := leaks(logs.String()); len(got) > 0 {
				t.Errorf("gateway log leaks %v:\n%s", got, logs.String())
			}
		})
	}
}

func TestTheAgentIsToldWhyAndNotWhere(t *testing.T) {
	s, _ := startGateway(t, authSchemes["api-key query"], closedURL(t))
	result := callTransactions(t, s)
	if strings.Contains(result, "http://") || strings.Contains(result, "api_key") {
		t.Errorf("tool result names the URL: %s", result)
	}
	if !strings.Contains(result, "connection refused") && !strings.Contains(result, "refused") {
		t.Errorf("tool result lost the reason: %s", result)
	}
}

// Through the recording proxy to a legacy system that echoes the request back,
// as text or as JSON, with an error status or with 200: the call record, the
// log and the agent's view stay clean.
func TestNoAuthTypeLeaksItsSecretThroughTheProxyAndAnEchoingLegacySystem(t *testing.T) {
	for name, auth := range authSchemes {
		for _, mode := range []string{"text500", "json500", "json200"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				legacy := httptest.NewServer(&echoLegacy{mode: mode})
				t.Cleanup(legacy.Close)
				target, _ := url.Parse(legacy.URL)
				sink := &captureSink{}
				px := httptest.NewServer(proxy.New(target, nil, sink, slog.New(slog.DiscardHandler)))
				t.Cleanup(px.Close)

				s, logs := startGateway(t, auth, px.URL)
				result, _ := callTransactionsResult(t, s)

				if got := leaks(result); len(got) > 0 {
					t.Errorf("tool result leaks %v: %s", got, result)
				}
				if got := leaks(logs.String()); len(got) > 0 {
					t.Errorf("gateway log leaks %v:\n%s", got, logs.String())
				}
				if got := leaks(sink.text(t)); len(got) > 0 {
					t.Errorf("calls.jsonl leaks %v: %s", got, sink.text(t))
				}
			})
		}
	}
}

// A renewable credential is replaced after a 401. The new token is one the
// gateway did not hold when it started, and must be masked all the same.
func TestARenewedTokenIsMaskedToo(t *testing.T) {
	for _, name := range []string{"oauth2", "bearer-login"} {
		t.Run(name, func(t *testing.T) {
			legacy := httptest.NewServer(&echoLegacy{mode: "json200", expireFirst: true})
			t.Cleanup(legacy.Close)
			s, logs := startGateway(t, authSchemes[name], legacy.URL)
			result, _ := callTransactionsResult(t, s)
			if !strings.Contains(result, "rejected credential") {
				t.Fatalf("the echo never arrived, so nothing was tested: %s", result)
			}
			if got := leaks(result); len(got) > 0 {
				t.Errorf("tool result leaks %v: %s", got, result)
			}
			if got := leaks(logs.String()); len(got) > 0 {
				t.Errorf("gateway log leaks %v", got)
			}
		})
	}
}

// The proxy records what it was told about, so an unreachable legacy system
// behind it must not put the credential into the record's error either.
func TestNoAuthTypeLeaksItsSecretWhenTheProxyCannotReachTheLegacySystem(t *testing.T) {
	for name, auth := range authSchemes {
		t.Run(name, func(t *testing.T) {
			target, _ := url.Parse(closedURL(t))
			sink := &captureSink{}
			px := httptest.NewServer(proxy.New(target, nil, sink, slog.New(slog.DiscardHandler)))
			t.Cleanup(px.Close)

			s, logs := startGateway(t, auth, px.URL)
			result := callTransactions(t, s)

			for what, text := range map[string]string{"tool result": result, "gateway log": logs.String(), "calls.jsonl": sink.text(t)} {
				if got := leaks(text); len(got) > 0 {
					t.Errorf("%s leaks %v: %s", what, got, text)
				}
			}
		})
	}
}

// The gateway cannot know a secret the legacy system makes up or rotates, so
// an error page is also masked by name: the names its own scheme owns, and the
// identity headers every scheme shares.
func TestAnErrorPageIsMaskedByNameForSecretsTheGatewayDoesNotHold(t *testing.T) {
	page := `{"api_key":"test-other-00000001","Authorization":"Bearer SENTINEL-other-2","X-Api-Key":"test-other-00000003"}` +
		"\nlink: /x?api_key=SENTINEL-other-4&page=1"
	for name, want := range map[string][]string{
		"api-key query":  {"test-other-00000001", "SENTINEL-other-4", "SENTINEL-other-2"},
		"api-key header": {"test-other-00000003", "SENTINEL-other-2"},
		"bearer":         {"SENTINEL-other-2"},
	} {
		t.Run(name, func(t *testing.T) {
			legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, page)
			}))
			t.Cleanup(legacy.Close)
			s, _ := startGateway(t, authSchemes[name], legacy.URL)
			result := callTransactions(t, s)
			for _, secret := range want {
				if strings.Contains(result, secret) {
					t.Errorf("tool result leaks %s: %s", secret, result)
				}
			}
		})
	}
}

// A weak secret ("1234") must not turn real data into REDACTED: the gateway
// says so at startup and leaves a successful response alone.
func TestAShortSecretLeavesSuccessfulDataAloneAndTheOperatorIsWarned(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":1234,"status":"active"}`)
	}))
	t.Cleanup(legacy.Close)
	s, logs := startGatewayEnv(t, authSchemes["api-key query"], legacy.URL, map[string]string{"KEY": "1234"})
	text, isErr := callTransactionsResult(t, s)
	if isErr || !strings.Contains(text, `1234`) || !strings.Contains(text, "status") {
		t.Errorf("data was changed: %s", text)
	}
	if len(linesWith(logs.String(), "level=WARN", "shorter than the masking minimum")) != 1 {
		t.Errorf("want one startup warning:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "1234") {
		t.Errorf("the warning must not print the secret:\n%s", logs.String())
	}
}

func TestTheTokenHistoryIsBounded(t *testing.T) {
	c := &credential{}
	for i := range 200 {
		c.set(fmt.Sprintf("token-%03d", i), time.Time{})
	}
	got := c.secrets()
	if len(got) != maxSeenTokens || got[len(got)-1] != "token-199" || got[0] != "token-"+fmt.Sprint(200-maxSeenTokens) {
		t.Errorf("history = %v", got)
	}
}

// Form and multipart bodies take their own forwarding path; its answers and
// failures are masked like any other.
func TestFormAndMultipartOperationsLeakNothingEither(t *testing.T) {
	doc, err := filepath.Abs(formDoc)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]any{
		"updatePetWithForm": {"petId": 7, "requestBody": map[string]any{"name": "rex"}},
		"uploadFile":        {"petId": 7, "requestBody": map[string]any{"file": map[string]any{"contentBase64": "aGk=", "filename": "a.txt"}}},
	}
	for _, mode := range []string{"text500", "json200", "down"} {
		for tool, args := range calls {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				upstream := closedURL(t)
				if mode != "down" {
					legacy := httptest.NewServer(&echoLegacy{mode: mode})
					t.Cleanup(legacy.Close)
					upstream = legacy.URL
				}
				s, logs := startGatewayDoc(t, authSchemes["api-key query"], upstream, sentinels, doc)
				res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(res)
				if got := leaks(string(b) + logs.String()); len(got) > 0 {
					t.Errorf("leaks %v: %s\n%s", got, b, logs.String())
				}
				if !strings.Contains(string(b), "HTTP") && !strings.Contains(string(b), "could not be reached") {
					t.Errorf("the call never reached the forwarding path, so nothing was tested: %s", b)
				}
			})
		}
	}
}

// Short secrets are skipped in data but must still be masked in an error page.
func TestAShortSecretIsMaskedInAnErrorPage(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "rejected credential 1234")
	}))
	t.Cleanup(legacy.Close)
	s, _ := startGatewayEnv(t, authSchemes["api-key query"], legacy.URL, map[string]string{"KEY": "1234"})
	text := callTransactions(t, s)
	if strings.Contains(text, "1234") || !strings.Contains(text, "rejected credential") {
		t.Errorf("tool result = %s", text)
	}
}
