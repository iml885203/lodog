package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/spec"
)

// A generic ticket system, not Backoffice: it authenticates with an api key in
// X-Api-Key and scopes calls with an X-Tenant header.
const headerDoc = `{"openapi":"3.0.3","paths":{"/tickets/{id}":{"get":{"operationId":"getTicket","parameters":[
	{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
	{"name":"X-Api-Key","in":"header","required":true,"schema":{"type":"string"}},
	{"name":"X-Tenant","in":"header","required":true,"schema":{"type":"string"}},
	{"name":"X-Auth-Token","in":"header","schema":{"type":"string"}},
	{"name":"X-Trace","in":"header","schema":{"type":"array","items":{"type":"string"}}},
	{"name":"X-Lodog-Actor","in":"header","schema":{"type":"string"}},
	{"name":"locale","in":"cookie","schema":{"type":"string"}}]}}}}`

func headerGateway(t *testing.T, auth casecfg.AuthConfig, env map[string]string, legacy http.Handler) (*mcp.ClientSession, *Gateway) {
	t.Helper()
	upstream := httptest.NewServer(legacy)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(headerDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL, Auth: auth,
		Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	gw, err := New(cfg, func(k string) string { return env[k] }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, gw
}

var apiKeyAuth = casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Api-Key"}

func TestACredentialHeaderIsNotOfferedToTheAgent(t *testing.T) {
	_, gw := headerGateway(t, apiKeyAuth, map[string]string{"K": "real-key"}, &recorder{})
	tools, _ := gw.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	props := tools[0].Tool.InputSchema["properties"].(map[string]any)
	for _, hidden := range []string{"X-Api-Key", "X-Lodog-Actor"} {
		if _, ok := props[hidden]; ok {
			t.Errorf("%s is offered to the agent", hidden)
		}
	}
	for _, shown := range []string{"id", "X-Tenant", "X-Trace", "locale"} {
		if _, ok := props[shown]; !ok {
			t.Errorf("%s is missing from the tool schema", shown)
		}
	}
	required := strings.Join(tools[0].Tool.InputSchema["required"].([]string), ",")
	if strings.Contains(required, "X-Api-Key") || !strings.Contains(required, "X-Tenant") {
		t.Errorf("required = %s", required)
	}
}

func TestHeaderAndCookieArgumentsReachTheLegacySystemAndTheKeyIsTheGatewaysOwn(t *testing.T) {
	legacy := &recorder{}
	session, _ := headerGateway(t, apiKeyAuth, map[string]string{"K": "real-key"}, legacy)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "getTicket", Arguments: map[string]any{
		"id": "7", "X-Tenant": "acme", "X-Trace": []any{"a", "b"}, "locale": "zh-TW",
		// An agent trying to choose its own credential or identity.
		"X-Api-Key": "agent-key", "X-Lodog-Actor": "agent:evil",
	}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %v", err, res)
	}
	got := legacy.seen[0]
	if got.URL.Path != "/tickets/7" || got.Header.Get("X-Tenant") != "acme" || got.Header.Get("X-Trace") != "a,b" {
		t.Fatalf("path %q headers %v", got.URL.Path, got.Header)
	}
	if c, err := got.Cookie("locale"); err != nil || c.Value != "zh-TW" {
		t.Fatalf("locale cookie = %v, %v", c, err)
	}
	if got.Header.Get("X-Api-Key") != "real-key" {
		t.Fatalf("X-Api-Key = %q, want the gateway's key", got.Header.Get("X-Api-Key"))
	}
	if got.Header.Get("X-Lodog-Actor") != "agent:test" {
		t.Fatalf("X-Lodog-Actor = %q, want the configured identity", got.Header.Get("X-Lodog-Actor"))
	}
}

func TestAMissingRequiredHeaderIsRefusedBeforeForwarding(t *testing.T) {
	legacy := &recorder{}
	session, _ := headerGateway(t, apiKeyAuth, map[string]string{"K": "real-key"}, legacy)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "getTicket", Arguments: map[string]any{"id": "7"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, `"X-Tenant" is required`) || len(legacy.seen) != 0 {
		t.Fatalf("isError=%v text=%q forwarded=%d", res.IsError, text, len(legacy.seen))
	}
}

func TestALineBreakInAHeaderArgumentIsRefused(t *testing.T) {
	legacy := &recorder{}
	session, _ := headerGateway(t, apiKeyAuth, map[string]string{"K": "real-key"}, legacy)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "getTicket",
		Arguments: map[string]any{"id": "7", "X-Tenant": "a\r\nX-Evil: 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(legacy.seen) != 0 {
		t.Fatalf("isError=%v forwarded=%d", res.IsError, len(legacy.seen))
	}
}

// What is hidden is what the configured auth scheme declares, so a token sent
// in a custom header is as hidden as Authorization.
func TestEachAuthSchemeHidesWhatItDeclares(t *testing.T) {
	bearer := casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T", Header: "X-Auth-Token"}
	oauth := oauthConfig("/oauth/token")
	oauth.Header = "X-Auth-Token"
	login := casecfg.AuthConfig{Type: casecfg.AuthCookieSession, Login: "POST /login", UsernameEnv: "U", PasswordEnv: "P"}
	cases := []struct {
		name string
		auth casecfg.AuthConfig
		p    spec.Param
		want bool
	}{
		{"authorization header", casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, spec.Param{Name: "Authorization", In: "header"}, true},
		{"cookie header", apiKeyAuth, spec.Param{Name: "cookie", In: "header"}, true},
		{"trace header", apiKeyAuth, spec.Param{Name: "X-Lodog-Run", In: "header"}, true},
		{"api-key header, any case", apiKeyAuth, spec.Param{Name: "x-api-key", In: "header"}, true},
		{"api-key header under bearer", casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, spec.Param{Name: "X-Api-Key", In: "header"}, false},
		{"bearer in a custom header", bearer, spec.Param{Name: "x-auth-token", In: "header"}, true},
		{"oauth2 in a custom header", oauth, spec.Param{Name: "X-Auth-Token", In: "header"}, true},
		{"oauth2 default header is not another name", oauthConfig("/t"), spec.Param{Name: "X-Auth-Token", In: "header"}, false},
		{"api-key query", casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Query: "api_key"}, spec.Param{Name: "api_key", In: "query"}, true},
		{"same name under header auth", apiKeyAuth, spec.Param{Name: "api_key", In: "query"}, false},
		{"session cookie", login, spec.Param{Name: "SID", In: "cookie"}, true},
		{"cookie under bearer", bearer, spec.Param{Name: "SID", In: "cookie"}, false},
		{"tenant header", apiKeyAuth, spec.Param{Name: "X-Tenant", In: "header"}, false},
		{"path parameter", apiKeyAuth, spec.Param{Name: "X-Api-Key", In: "path"}, false},
	}
	for _, c := range cases {
		doc := &spec.Document{Operations: []spec.Operation{{Params: []spec.Param{c.p}}}}
		if err := casecfg.HideCredentialParams(doc, c.auth); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if hidden := len(doc.Operations[0].Params) == 0; hidden != c.want {
			t.Errorf("%s: hidden = %v, want %v", c.name, hidden, c.want)
		}
	}
}

func TestACustomBearerHeaderIsTheGatewaysNotTheAgents(t *testing.T) {
	legacy := &recorder{}
	auth := casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T", Header: "X-Auth-Token"}
	session, gw := headerGateway(t, auth, map[string]string{"T": "real-token"}, legacy)
	assertHidden(t, gw, "X-Auth-Token")
	callWithForgedToken(t, session)
	if got := legacy.seen[0].Header.Get("X-Auth-Token"); got != "Bearer real-token" {
		t.Fatalf("X-Auth-Token = %q, want the gateway's token", got)
	}
}

func TestACustomOAuth2HeaderIsTheGatewaysNotTheAgents(t *testing.T) {
	legacy := &tokenServer{lifetime: "3600"}
	auth := oauthConfig("/oauth/token")
	auth.Header = "X-Auth-Token"
	session, gw := headerGateway(t, auth, oauthEnv, legacy)
	assertHidden(t, gw, "X-Auth-Token")
	callWithForgedToken(t, session)
	if got := legacy.apiSeen[0].Get("X-Auth-Token"); got != "Bearer tok-1" {
		t.Fatalf("X-Auth-Token = %q, want the gateway's token", got)
	}
}

func assertHidden(t *testing.T, gw *Gateway, name string) {
	t.Helper()
	tools, _ := gw.Tools()
	props := tools[0].Tool.InputSchema["properties"].(map[string]any)
	if _, ok := props[name]; ok {
		t.Fatalf("%s is offered to the agent", name)
	}
	if _, ok := props["X-Tenant"]; !ok {
		t.Fatal("an ordinary header disappeared with it")
	}
}

func callWithForgedToken(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "getTicket",
		Arguments: map[string]any{"id": "7", "X-Tenant": "acme", "X-Api-Key": "k", "X-Auth-Token": "forged"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A name used in two places becomes two arguments, prefixed with their place;
// each value is sent where the document put it, and a name used once keeps
// its plain name.
func TestSameNamedParametersAreSeparateArgumentsSentToTheirOwnPlaces(t *testing.T) {
	doc := `{"openapi":"3.0.3","paths":{"/t/{id}":{"get":{"operationId":"getT","parameters":[
		{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
		{"name":"id","in":"header","required":true,"schema":{"type":"string"}},
		{"name":"id","in":"query","schema":{"type":"string"}},
		{"name":"limit","in":"query","schema":{"type":"integer"}}]}}}}`
	legacy := &recorder{}
	upstream := httptest.NewServer(legacy)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL,
		Auth: casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	gw, err := New(cfg, func(string) string { return "tok" }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := gw.Tools()
	props := tools[0].Tool.InputSchema["properties"].(map[string]any)
	for _, arg := range []string{"path.id", "header.id", "query.id", "limit"} {
		if _, ok := props[arg]; !ok {
			t.Errorf("argument %q missing; have %v", arg, props)
		}
	}
	if _, ok := props["id"]; ok {
		t.Error("the ambiguous plain name must not be offered")
	}

	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "getT", Arguments: map[string]any{
		"path.id": "7", "header.id": "tenant-1", "query.id": "q9", "limit": 5,
	}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %v", err, res)
	}
	got := legacy.seen[0]
	if got.URL.Path != "/t/7" || got.Header.Get("id") != "tenant-1" || got.URL.Query().Get("id") != "q9" || got.URL.Query().Get("limit") != "5" {
		t.Fatalf("url %s header id %q", got.URL, got.Header.Get("id"))
	}
}

// An init placeholder left in the config is refused at startup, naming the
// field, and every placeholder is named at once.
func TestAConfigStillHoldingInitPlaceholdersIsRefusedAtStartup(t *testing.T) {
	cfg := &casecfg.Config{
		Case: t.TempDir(), Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: "http://localhost:1",
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthOAuth2, TokenURL: "/" + casecfg.Placeholder + "/oauth/token", ClientIDEnv: "C", ClientSecretEnv: "S"},
		Identity: casecfg.IdentityConfig{Actor: "agent:x", OnBehalfOf: "user:" + casecfg.Placeholder},
	}
	_, err := New(cfg, func(string) string { return "v" }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "auth.token_url") || !strings.Contains(err.Error(), "identity.on_behalf_of") {
		t.Fatalf("err = %v, want both fields named", err)
	}
	cfg.Auth.Login = "POST /" + casecfg.Placeholder + "/login"
	if _, err := New(cfg, func(string) string { return "v" }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil ||
		!strings.Contains(err.Error(), "auth.login") {
		t.Fatalf("err = %v, want auth.login named", err)
	}
}

// An operation whose body is in a form the gateway does not forward is not
// served at all: serving it without the body would send a call that cannot
// work. Form bodies are forwarded when their schema has fields to send.
func TestAnOperationWithAnUnsupportedBodyIsSkippedNotServedWithoutIt(t *testing.T) {
	doc, err := spec.Parse([]byte(`{"openapi":"3.0.3","paths":{
	  "/xml":{"post":{"requestBody":{"required":true,"content":{"application/xml":{"schema":{"type":"object"}}}}}},
	  "/form":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object"}}}}}},
	  "/up":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object"}}}}}},
	  "/text":{"post":{"requestBody":{"content":{"text/plain":{"schema":{"type":"string"}}}}}},
	  "/flat":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"string"}}}}}},
	  "/json":{"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}}}},
	  "/none":{"get":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tools, skipped := serving.RawTools(doc)
	var served, refused []string
	for _, tl := range tools {
		served = append(served, tl.Op.Path)
	}
	for _, s := range skipped {
		refused = append(refused, s.Op.Path+" ("+s.Reason+")")
	}
	if strings.Join(served, ",") != "/form,/json,/none,/up" || len(refused) != 3 {
		t.Fatalf("served %v, skipped %v", served, refused)
	}
}

// Once the gateway hides one side of a clash, the other no longer needs its
// place prefix.
func TestAClashWithAHiddenParameterLeavesThePlainName(t *testing.T) {
	doc, err := spec.Parse([]byte(`{"openapi":"3.0.3","paths":{"/x":{"get":{"parameters":[
		{"name":"X-Api-Key","in":"header","schema":{"type":"string"}},
		{"name":"X-Api-Key","in":"query","schema":{"type":"string"}},
		{"name":"id","in":"header","schema":{"type":"string"}},
		{"name":"id","in":"query","schema":{"type":"string"}}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := casecfg.HideCredentialParams(doc, apiKeyAuth); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{}
	for _, p := range doc.Operations[0].Params {
		args[p.In+":"+p.Name] = p.ArgName()
	}
	want := map[string]string{"query:X-Api-Key": "X-Api-Key", "header:id": "header.id", "query:id": "query.id"}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("%s = %q, want %q", k, args[k], v)
		}
	}
}

// A JSON-flavoured body (+json suffix, charset parameter) is served, and goes
// out in the type the document declared.
func TestAJSONFlavouredBodyIsServedAndSentInItsDeclaredType(t *testing.T) {
	doc := `{"openapi":"3.0.3","paths":{"/v":{"post":{"operationId":"postV","requestBody":{"required":true,"content":{
		"application/vnd.api+json":{"schema":{"type":"object"}}}}}}}}`
	legacy := &recorder{}
	upstream := httptest.NewServer(legacy)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL,
		Auth: casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	gw, err := New(cfg, func(string) string { return "tok" }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if tools, skipped := gw.Tools(); len(tools) != 1 || len(skipped) != 0 {
		t.Fatalf("served %d skipped %v", len(tools), skipped)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "postV",
		Arguments: map[string]any{"requestBody": map[string]any{"a": 1}},
	})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %v", err, res)
	}
	if got := legacy.seen[0].Header.Get("Content-Type"); got != "application/vnd.api+json" {
		t.Fatalf("Content-Type = %q", got)
	}
}
