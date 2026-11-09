package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"
)

// recorder is a legacy API that issues a bearer token at /login and records
// what each later request carried.
type recorder struct {
	mu        sync.Mutex
	logins    int
	loginBody map[string]string
	loginType string // Content-Type the last login carried
	seen      []*http.Request
	token     string // what /login hands out; empty means refuse
	// arrive runs when a non-login request reaches the legacy system, before
	// it answers. A test uses it to hold or drop the call.
	arrive func()
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.arrive != nil && req.URL.Path != "/login" {
		r.arrive()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path == "/login" {
		r.logins++
		r.loginType = req.Header.Get("Content-Type")
		r.loginBody = map[string]string{}
		if strings.HasPrefix(r.loginType, "application/x-www-form-urlencoded") {
			_ = req.ParseForm()
			for k := range req.PostForm {
				r.loginBody[k] = req.PostForm.Get(k)
			}
		} else {
			_ = json.NewDecoder(req.Body).Decode(&r.loginBody)
		}
		if r.token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"token":"`+r.token+`"}}`)
		return
	}
	r.seen = append(r.seen, req.Clone(req.Context()))
	_, _ = io.WriteString(w, `{}`)
}

func call(t *testing.T, auth casecfg.AuthConfig, env map[string]string, legacy http.Handler) (*Upstream, error) {
	t.Helper()
	srv := httptest.NewServer(legacy)
	t.Cleanup(srv.Close)
	a, err := newAuthenticator(auth, func(k string) string { return env[k] })
	if err != nil {
		return nil, err
	}
	u, err := NewUpstream(srv.URL, a, false)
	if err != nil {
		t.Fatal(err)
	}
	req, err := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.Do(context.Background(), req)
	return u, err
}

func TestABearerTokenIsReadFromTheLoginResponse(t *testing.T) {
	legacy := &recorder{token: "tok-123"}
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token",
		UsernameField: "user", PasswordField: "pass", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "pw"}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got := legacy.seen[0].Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", got)
	}
	if legacy.loginBody["user"] != "bot" || legacy.loginBody["pass"] != "pw" {
		t.Fatalf("login body = %v: the configured field names must be used", legacy.loginBody)
	}
}

func TestLoginFieldsDefaultToUsernameAndPassword(t *testing.T) {
	legacy := &recorder{token: "t"}
	if _, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "pw"}, legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.loginBody["username"] != "bot" || legacy.loginBody["password"] != "pw" {
		t.Fatalf("login body = %v", legacy.loginBody)
	}
}

func TestALoginResponseWithoutATokenIsARefusal(t *testing.T) {
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":{}}`) })
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "pw"}, legacy)
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v, want ErrLoginFailed", err)
	}
}

func TestARefusedBearerLoginIsNeverRetried(t *testing.T) {
	legacy := &recorder{}
	u, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "wrong"}, legacy)
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v", err)
	}
	req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	if _, err := u.Do(context.Background(), req); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("second call err = %v", err)
	}
	if legacy.logins != 1 {
		t.Fatalf("logins = %d, want 1", legacy.logins)
	}
}

func TestAFixedBearerTokenIsSentAsIs(t *testing.T) {
	legacy := &recorder{}
	if _, err := call(t, casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, map[string]string{"T": "fixed"}, legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.logins != 0 || legacy.seen[0].Header.Get("Authorization") != "Bearer fixed" {
		t.Fatalf("logins=%d auth=%q", legacy.logins, legacy.seen[0].Header.Get("Authorization"))
	}
}

func TestAnAPIKeyGoesInTheConfiguredHeaderOrQuery(t *testing.T) {
	legacy := &recorder{}
	if _, err := call(t, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Api-Key"}, map[string]string{"K": "k1"}, legacy); err != nil {
		t.Fatal(err)
	}
	if got := legacy.seen[0].Header.Get("X-Api-Key"); got != "k1" {
		t.Fatalf("header = %q", got)
	}
	legacy = &recorder{}
	if _, err := call(t, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Query: "api_key"}, map[string]string{"K": "k2"}, legacy); err != nil {
		t.Fatal(err)
	}
	if got := legacy.seen[0].URL.Query().Get("api_key"); got != "k2" {
		t.Fatalf("query = %q", got)
	}
}

func TestAMissingSecretNamesTheVariable(t *testing.T) {
	for name, cfg := range map[string]casecfg.AuthConfig{
		"cookie":  {Type: casecfg.AuthCookieSession, Login: "POST /login", UsernameEnv: "MY_USER", PasswordEnv: "MY_PASS"},
		"bearer":  {Type: casecfg.AuthBearer, TokenEnv: "MY_TOKEN"},
		"api-key": {Type: casecfg.AuthAPIKey, KeyEnv: "MY_KEY", Header: "X-Key"},
	} {
		_, err := newAuthenticator(cfg, func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), "MY_") {
			t.Errorf("%s: err = %v, want it to name the empty variable", name, err)
		}
	}
}

func TestAnAuthBlockThatCannotWorkIsRefused(t *testing.T) {
	env := func(string) string { return "x" }
	for name, cfg := range map[string]casecfg.AuthConfig{
		"unknown type":            {Type: "saml"},
		"bearer with nothing":     {Type: casecfg.AuthBearer},
		"api-key in two places":   {Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X", Query: "q"},
		"api-key in no place":     {Type: casecfg.AuthAPIKey, KeyEnv: "K"},
		"login without a path":    {Type: casecfg.AuthCookieSession, Login: "POST", UsernameEnv: "U", PasswordEnv: "P"},
		"cookie without password": {Type: casecfg.AuthCookieSession, Login: "POST /l", UsernameEnv: "U"},
	} {
		if _, err := newAuthenticator(cfg, env); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestA401WithAFixedCredentialIsReturnedNotRetried(t *testing.T) {
	var calls int
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	a, err := newAuthenticator(casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"}, func(string) string { return "k" })
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewUpstream(srv.URL, a, false)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	resp, err := u.Do(context.Background(), req)
	if err != nil || resp.Status != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("status=%v err=%v calls=%d: a fixed key cannot be renewed, so the 401 goes back to the agent", resp, err, calls)
	}
}

func TestAFormLoginSendsAFormBodyWithTheConfiguredFields(t *testing.T) {
	legacy := &recorder{token: "tok"}
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", LoginFormat: casecfg.LoginFormatForm, TokenPath: "data.token",
		UsernameField: "user", PasswordField: "pass", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot&co", "P": "p w=1"}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.loginType != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", legacy.loginType)
	}
	if legacy.loginBody["user"] != "bot&co" || legacy.loginBody["pass"] != "p w=1" || len(legacy.loginBody) != 2 {
		t.Fatalf("form = %v: values must be escaped and round-trip", legacy.loginBody)
	}
	if got := legacy.seen[0].Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q: the token from a form login must be used", got)
	}
}

func TestAJSONLoginIsTheDefault(t *testing.T) {
	legacy := &recorder{token: "tok"}
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "pw"}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.loginType != "application/json" {
		t.Fatalf("Content-Type = %q", legacy.loginType)
	}
}

func TestAFormLoginSessionCookieIsKept(t *testing.T) {
	var cookieSeen string
	var logins int
	legacy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins++
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.FormValue("u") != "bot" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			return
		}
		if c, err := r.Cookie("sid"); err == nil {
			cookieSeen = c.Value
		}
	})
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthCookieSession, Login: "POST /login", LoginFormat: casecfg.LoginFormatForm,
		UsernameField: "u", PasswordField: "p", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "pw"}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if cookieSeen != "abc" || logins != 1 {
		t.Fatalf("cookie = %q, logins = %d", cookieSeen, logins)
	}
}

func TestARefusedFormLoginIsNeverRetried(t *testing.T) {
	legacy := &recorder{}
	u, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", LoginFormat: casecfg.LoginFormatForm, TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
	}, map[string]string{"U": "bot", "P": "wrong"}, legacy)
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v", err)
	}
	req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	if _, err := u.Do(context.Background(), req); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("second call err = %v", err)
	}
	if legacy.logins != 1 {
		t.Fatalf("logins = %d, want 1", legacy.logins)
	}
}

func TestAnUnknownLoginFormatIsRefused(t *testing.T) {
	_, err := newAuthenticator(casecfg.AuthConfig{
		Type: casecfg.AuthCookieSession, Login: "POST /login", LoginFormat: "xml", UsernameEnv: "U", PasswordEnv: "P",
	}, func(string) string { return "x" })
	if err == nil || !strings.Contains(err.Error(), "login_format") {
		t.Fatalf("err = %v", err)
	}
}

// tokenServer is an OAuth2 token endpoint plus an API that accepts only the
// token it last issued.
type tokenServer struct {
	mu        sync.Mutex
	issued    int
	form      url.Values
	basicUser string
	basicPass string
	hasBasic  bool
	lifetime  string // JSON for expires_in; empty omits it
	refuse    bool
	apiSeen   []http.Header
}

func (s *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/oauth/token" {
		s.issued++
		_ = r.ParseForm()
		s.form = r.PostForm
		s.basicUser, s.basicPass, s.hasBasic = r.BasicAuth()
		if s.refuse {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		exp := ""
		if s.lifetime != "" {
			exp = `,"expires_in":` + s.lifetime
		}
		_, _ = io.WriteString(w, `{"access_token":"tok-`+strconv.Itoa(s.issued)+`","token_type":"Bearer"`+exp+`}`)
		return
	}
	s.apiSeen = append(s.apiSeen, r.Header.Clone())
	if r.Header.Get("Authorization") != "Bearer tok-"+strconv.Itoa(s.issued) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = io.WriteString(w, `{}`)
}

func oauthConfig(tokenURL string) casecfg.AuthConfig {
	return casecfg.AuthConfig{
		Type: casecfg.AuthOAuth2, TokenURL: tokenURL, ClientIDEnv: "CID", ClientSecretEnv: "CSEC", Scope: "read write",
	}
}

var oauthEnv = map[string]string{"CID": "my client", "CSEC": "s&cret"}

// oauthUpstream returns an Upstream for the oauth2 config with a controllable clock.
func oauthUpstream(t *testing.T, cfg casecfg.AuthConfig, base string, clock *time.Time) *Upstream {
	t.Helper()
	a, err := newAuthenticator(cfg, func(k string) string { return oauthEnv[k] })
	if err != nil {
		t.Fatal(err)
	}
	if clock != nil {
		a.(*oauth2ClientCredentials).now = func() time.Time { return *clock }
	}
	u, err := NewUpstream(base, a, false)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func get(t *testing.T, u *Upstream) *Response {
	t.Helper()
	req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	resp, err := u.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestOAuth2TradesClientCredentialsForABearerToken(t *testing.T) {
	legacy := &tokenServer{lifetime: "3600"}
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	// An absolute token_url: the token endpoint need not live on the upstream.
	u := oauthUpstream(t, oauthConfig(srv.URL+"/oauth/token"), srv.URL, nil)
	if resp := get(t, u); resp.Status != http.StatusOK {
		t.Fatalf("status = %d", resp.Status)
	}
	if legacy.form.Get("grant_type") != "client_credentials" || legacy.form.Get("scope") != "read write" {
		t.Fatalf("form = %v", legacy.form)
	}
	if !legacy.hasBasic || legacy.basicUser != "my+client" || legacy.basicPass != "s%26cret" {
		t.Fatalf("basic = %v %q %q: RFC 6749 form-encodes id and secret before Basic", legacy.hasBasic, legacy.basicUser, legacy.basicPass)
	}
	if legacy.form.Has("client_secret") {
		t.Fatal("the secret must not travel in the body when client_auth is basic")
	}
	get(t, u)
	if legacy.issued != 1 {
		t.Fatalf("tokens issued = %d, want 1: the token is reused until it expires", legacy.issued)
	}
}

func TestOAuth2CanSendTheClientCredentialsInTheBody(t *testing.T) {
	legacy := &tokenServer{}
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	cfg := oauthConfig("/oauth/token") // a path on the upstream
	cfg.ClientAuth = casecfg.ClientAuthBody
	u := oauthUpstream(t, cfg, srv.URL, nil)
	get(t, u)
	if legacy.form.Get("client_id") != "my client" || legacy.form.Get("client_secret") != "s&cret" || legacy.hasBasic {
		t.Fatalf("form = %v basic = %v", legacy.form, legacy.hasBasic)
	}
}

func TestOAuth2RenewsBeforeTheTokenExpires(t *testing.T) {
	legacy := &tokenServer{lifetime: "100"}
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	now := time.Unix(1_000_000, 0)
	u := oauthUpstream(t, oauthConfig("/oauth/token"), srv.URL, &now)

	get(t, u)
	now = now.Add(60 * time.Second) // 40s left, still more than the renew lead
	get(t, u)
	if legacy.issued != 1 {
		t.Fatalf("issued = %d, want 1", legacy.issued)
	}
	now = now.Add(15 * time.Second) // 25s left, inside the 30s lead
	resp := get(t, u)
	if legacy.issued != 2 || resp.Status != http.StatusOK {
		t.Fatalf("issued = %d status = %d: a token about to expire must be replaced before it is used", legacy.issued, resp.Status)
	}
	if got := legacy.apiSeen[len(legacy.apiSeen)-1].Get("Authorization"); got != "Bearer tok-2" {
		t.Fatalf("Authorization = %q, the call must carry the new token", got)
	}
}

func TestOAuth2WithoutALifetimeRenewsOnA401(t *testing.T) {
	legacy := &tokenServer{}
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	u := oauthUpstream(t, oauthConfig("/oauth/token"), srv.URL, nil)
	get(t, u)
	legacy.mu.Lock()
	legacy.issued++ // the server forgets the token: the next call is a 401
	legacy.mu.Unlock()
	if resp := get(t, u); resp.Status != http.StatusOK || legacy.issued != 3 {
		t.Fatalf("status = %d issued = %d: a 401 is answered by one new token", resp.Status, legacy.issued)
	}
}

func TestARefusedTokenRequestIsNeverRetried(t *testing.T) {
	legacy := &tokenServer{refuse: true}
	srv := httptest.NewServer(legacy)
	defer srv.Close()
	u := oauthUpstream(t, oauthConfig("/oauth/token"), srv.URL, nil)
	for range 3 {
		req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
		if _, err := u.Do(context.Background(), req); !errors.Is(err, ErrLoginFailed) {
			t.Fatalf("err = %v, want ErrLoginFailed", err)
		}
	}
	if legacy.issued != 1 || len(legacy.apiSeen) != 0 {
		t.Fatalf("token requests = %d, api calls = %d: refused credentials must not be sent again", legacy.issued, len(legacy.apiSeen))
	}
}

func TestAnUnreachableSignInIsRetriedOnTheNextCall(t *testing.T) {
	legacy := &tokenServer{}
	var drop atomic.Bool
	drop.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop.Load() { // the network is down: the request gets no answer
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		legacy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	u := oauthUpstream(t, oauthConfig("/oauth/token"), srv.URL, nil)
	req, _ := u.NewRequest(context.Background(), "GET", "/api/x", nil, nil)
	_, err := u.Do(context.Background(), req)
	if err == nil || errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v: an unanswered request is not a refusal", err)
	}
	drop.Store(false)
	if resp := get(t, u); resp.Status != http.StatusOK {
		t.Fatalf("status = %d: the gateway must recover once the network does", resp.Status)
	}
}

func TestBearerAndAPIKeyHeaderAndPrefixAreConfigurable(t *testing.T) {
	custom := func(s string) *string { return &s }
	for name, tc := range map[string]struct {
		cfg          casecfg.AuthConfig
		header, want string
	}{
		"bearer default":    {casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, "Authorization", "Bearer tok"},
		"bearer scheme":     {casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T", Prefix: custom("Token ")}, "Authorization", "Token tok"},
		"bearer header":     {casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T", Header: "X-Auth", Prefix: custom("")}, "X-Auth", "tok"},
		"api-key plain":     {casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "T", Header: "X-Key"}, "X-Key", "tok"},
		"api-key prefix":    {casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "T", Header: "Authorization", Prefix: custom("ApiKey ")}, "Authorization", "ApiKey tok"},
		"oauth2 own header": {casecfg.AuthConfig{Type: casecfg.AuthOAuth2, TokenURL: "/t", ClientIDEnv: "T", ClientSecretEnv: "T", Header: "X-Token", Prefix: custom("")}, "X-Token", ""},
	} {
		a, err := newAuthenticator(tc.cfg, func(string) string { return "tok" })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if o, ok := a.(*oauth2ClientCredentials); ok {
			o.cred.set("tok", time.Time{})
			tc.want = "tok"
		}
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://x/", nil)
		a.apply(req)
		if got := req.Header.Get(tc.header); got != tc.want {
			t.Errorf("%s: %s = %q, want %q", name, tc.header, got, tc.want)
		}
	}
}

func TestAPrefixOnAQueryKeyIsRefused(t *testing.T) {
	p := "x "
	_, err := newAuthenticator(casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Query: "k", Prefix: &p}, func(string) string { return "v" })
	if err == nil {
		t.Fatal("want an error: a prefix cannot apply to a query parameter")
	}
}

func TestSignInSuccessDefaultsTo2xxAndCanRequireACookie(t *testing.T) {
	login := func(withCookie bool, status int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/login" {
				return
			}
			if withCookie {
				http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			w.WriteHeader(status)
		})
	}
	cfg := func(cookie string) casecfg.AuthConfig {
		return casecfg.AuthConfig{
			Type: casecfg.AuthCookieSession, Login: "POST /login", UsernameEnv: "U", PasswordEnv: "P",
			Success: casecfg.SuccessConfig{CookieName: cookie},
		}
	}
	env := map[string]string{"U": "u", "P": "p"}
	for name, tc := range map[string]struct {
		cfg     casecfg.AuthConfig
		handler http.Handler
		refused bool
	}{
		"default accepts a 2xx without a cookie": {cfg(""), login(false, 200), false},
		"default refuses a 401":                  {cfg(""), login(false, 401), true},
		"named cookie present":                   {cfg("sid"), login(true, 200), false},
		"named cookie missing":                   {cfg("sid"), login(false, 200), true},
		"other cookie is not the named one":      {cfg("session"), login(true, 200), true},
	} {
		_, err := call(t, tc.cfg, env, tc.handler)
		if refused := errors.Is(err, ErrLoginFailed); refused != tc.refused || (err != nil) != tc.refused {
			t.Errorf("%s: err = %v, want refused=%v", name, err, tc.refused)
		}
	}
}

func TestTheSuccessConditionAlsoGuardsABearerLogin(t *testing.T) {
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":{"token":"t"}}`) })
	_, err := call(t, casecfg.AuthConfig{
		Type: casecfg.AuthBearer, Login: "POST /login", TokenPath: "data.token", UsernameEnv: "U", PasswordEnv: "P",
		Success: casecfg.SuccessConfig{CookieName: "sid"},
	}, map[string]string{"U": "u", "P": "p"}, legacy)
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v, want ErrLoginFailed: the token alone is not proof when a cookie is demanded", err)
	}
}

func TestAnOAuth2BlockThatCannotWorkIsRefused(t *testing.T) {
	env := func(string) string { return "x" }
	base := oauthConfig("https://idp.example/token")
	for name, mut := range map[string]func(*casecfg.AuthConfig){
		"no token_url":        func(c *casecfg.AuthConfig) { c.TokenURL = "" },
		"relative token_url":  func(c *casecfg.AuthConfig) { c.TokenURL = "oauth/token" },
		"no client id env":    func(c *casecfg.AuthConfig) { c.ClientIDEnv = "" },
		"no client secret":    func(c *casecfg.AuthConfig) { c.ClientSecretEnv = "" },
		"unknown client_auth": func(c *casecfg.AuthConfig) { c.ClientAuth = "jwt" },
	} {
		cfg := base
		mut(&cfg)
		if _, err := newAuthenticator(cfg, env); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := newAuthenticator(base, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "CID") {
		t.Errorf("empty secret: err = %v, want it to name the variable", err)
	}
}
