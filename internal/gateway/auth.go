package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/redact"
)

// authenticator is one way of proving the gateway's identity to the legacy
// system. Secrets come from the environment and never leave this value.
type authenticator interface {
	// signIn obtains what the scheme needs before the first call. It is
	// called at most once per sign-in and never again after a refusal.
	signIn(ctx context.Context, u *Upstream) error
	// apply adds the credential to one outgoing request.
	apply(req *http.Request)
	// renewable reports whether a 401 may be answered by signing in again:
	// true for a session or token obtained by logging in, false for a fixed
	// token or key, where signing in again changes nothing.
	renewable() bool
	// expired reports whether the credential is known to be past its
	// lifetime, so it is renewed before a call is wasted on a 401. Schemes
	// that do not know their lifetime answer false.
	expired() bool
	// owns declares the request parameters this scheme sets itself, so the
	// gateway never offers them to an agent.
	owns() casecfg.Owned
	// secrets lists every secret value the scheme holds or has held (a
	// renewed token keeps its predecessors), so they can be masked by value.
	secrets() []string
}

// ownedNames is what must never be written down for this scheme (internal/redact).
func ownedNames(o casecfg.Owned) redact.Names {
	return redact.Names{Queries: o.Queries, Headers: o.Headers}
}

// newAuthenticator builds the scheme cfg describes, reading its secrets with
// getenv. A missing secret is an error that names the variable.
func newAuthenticator(cfg casecfg.AuthConfig, getenv func(string) string) (authenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	need := func(name, field string) (string, error) {
		if name == "" {
			return "", fmt.Errorf("auth.%s is required for auth.type %s", field, cfg.Type)
		}
		v := getenv(name)
		if v == "" {
			return "", fmt.Errorf("environment variable %s (auth.%s) is empty", name, field)
		}
		return v, nil
	}
	switch cfg.Type {
	case casecfg.AuthCookieSession:
		l, err := newPasswordLogin(cfg, need)
		if err != nil {
			return nil, err
		}
		return &cookieSession{login: l, success: cfg.Success, owned: cfg.Owned()}, nil
	case casecfg.AuthBearer:
		return newBearer(cfg, need)
	case casecfg.AuthAPIKey:
		return newAPIKey(cfg, need)
	case casecfg.AuthOAuth2:
		return newOAuth2(cfg, need)
	}
	return nil, fmt.Errorf("auth.type %q is not one of %s, %s, %s, %s", cfg.Type,
		casecfg.AuthCookieSession, casecfg.AuthBearer, casecfg.AuthAPIKey, casecfg.AuthOAuth2)
}

func newBearer(cfg casecfg.AuthConfig, need func(name, field string) (string, error)) (authenticator, error) {
	cred := newCredential(cfg, "Bearer ")
	if cfg.TokenEnv != "" {
		token, err := need(cfg.TokenEnv, "token_env")
		if err != nil {
			return nil, err
		}
		cred.set(token, time.Time{})
		return &fixedBearer{cred: cred}, nil
	}
	l, err := newPasswordLogin(cfg, need)
	if err != nil {
		return nil, err
	}
	return &loginBearer{login: l, tokenPath: strings.Split(cfg.TokenPath, "."), success: cfg.Success, cred: cred}, nil
}

func newAPIKey(cfg casecfg.AuthConfig, need func(name, field string) (string, error)) (authenticator, error) {
	key, err := need(cfg.KeyEnv, "key_env")
	if err != nil {
		return nil, err
	}
	prefix := ""
	if cfg.Prefix != nil {
		prefix = *cfg.Prefix
	}
	return &apiKey{key: key, header: cfg.Header, prefix: prefix, query: cfg.Query, owned: cfg.Owned()}, nil
}

// passwordLogin posts a username and password as JSON or as a form. The field
// names are configurable because every system spells them differently
// (Backoffice: LoginName, Password).
type passwordLogin struct {
	method, path string
	cfg          casecfg.AuthConfig
	user, pass   string
}

func newPasswordLogin(cfg casecfg.AuthConfig, need func(name, field string) (string, error)) (passwordLogin, error) {
	method, path, _ := strings.Cut(strings.TrimSpace(cfg.Login), " ")
	user, err := need(cfg.UsernameEnv, "username_env")
	if err != nil {
		return passwordLogin{}, err
	}
	pass, err := need(cfg.PasswordEnv, "password_env")
	if err != nil {
		return passwordLogin{}, err
	}
	return passwordLogin{method: strings.ToUpper(method), path: path, cfg: cfg, user: user, pass: pass}, nil
}

func (l passwordLogin) post(ctx context.Context, u *Upstream) (*Response, error) {
	contentType, body, err := l.cfg.LoginBody(l.user, l.pass)
	if err != nil {
		return nil, err
	}
	req, err := u.NewRequest(ctx, l.method, l.path, nil, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := u.send(req)
	if err != nil {
		return nil, fmt.Errorf("sign in: %w: %w", errUnreachable, err)
	}
	return resp, nil
}

// checkSuccess reports why resp does not show a sign-in, or nil when it does.
func checkSuccess(c casecfg.SuccessConfig, resp *Response) error {
	if resp.Status/100 != 2 {
		return fmt.Errorf("%w: status %d", ErrLoginFailed, resp.Status)
	}
	if c.CookieName == "" {
		return nil
	}
	for _, ck := range (&http.Response{Header: resp.Header}).Cookies() {
		if ck.Name == c.CookieName {
			return nil
		}
	}
	return fmt.Errorf("%w: status %d, no %s cookie issued", ErrLoginFailed, resp.Status, c.CookieName)
}

// cookieSession signs in once; the cookie jar carries the session after that.
type cookieSession struct {
	login   passwordLogin
	success casecfg.SuccessConfig
	owned   casecfg.Owned
}

func (c *cookieSession) signIn(ctx context.Context, u *Upstream) error {
	resp, err := c.login.post(ctx, u)
	if err != nil {
		return err
	}
	return checkSuccess(c.success, resp)
}

func (c *cookieSession) apply(*http.Request) {}
func (c *cookieSession) renewable() bool     { return true }
func (c *cookieSession) expired() bool       { return false }
func (c *cookieSession) owns() casecfg.Owned { return c.owned }
func (c *cookieSession) secrets() []string   { return []string{c.login.pass} }

// credential is a token and the header that carries it. It is shared by every
// token scheme so they cannot disagree on how a token is sent.
type credential struct {
	header, prefix string
	owned          casecfg.Owned

	mu      sync.RWMutex
	token   string
	seen    []string
	expires time.Time // zero: no known lifetime
}

// newCredential reads the header and prefix cfg asks for, with defaultPrefix
// when it names none.
func newCredential(cfg casecfg.AuthConfig, defaultPrefix string) *credential {
	c := &credential{header: cfg.TokenHeader(), prefix: defaultPrefix, owned: cfg.Owned()}
	if cfg.Prefix != nil {
		c.prefix = *cfg.Prefix
	}
	return c
}

func (c *credential) owns() casecfg.Owned { return c.owned }

func (c *credential) set(token string, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token, c.expires = token, expires
	if !slices.Contains(c.seen, token) {
		c.seen = append(c.seen, token)
		if len(c.seen) > maxSeenTokens {
			c.seen = slices.Clone(c.seen[len(c.seen)-maxSeenTokens:])
		}
	}
}

// maxSeenTokens bounds the token history: an old error mentions a recent
// token, and the list is scanned for every response.
const maxSeenTokens = 8

// secrets is the recent tokens this credential has carried: a token that has been
// replaced may still turn up in an old error.
func (c *credential) secrets() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.seen)
}

func (c *credential) apply(req *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	req.Header.Set(c.header, c.prefix+c.token)
}

func (c *credential) expiredAt(now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.expires.IsZero() && !now.Before(c.expires)
}

// loginBearer signs in and reads a token from the JSON response.
type loginBearer struct {
	login     passwordLogin
	tokenPath []string
	success   casecfg.SuccessConfig
	cred      *credential
}

func (b *loginBearer) signIn(ctx context.Context, u *Upstream) error {
	resp, err := b.login.post(ctx, u)
	if err != nil {
		return err
	}
	if err := checkSuccess(b.success, resp); err != nil {
		return err
	}
	token, ok := lookupString(resp.Body, b.tokenPath)
	if !ok {
		return fmt.Errorf("%w: no token at %s in the login response", ErrLoginFailed, strings.Join(b.tokenPath, "."))
	}
	b.cred.set(token, time.Time{})
	return nil
}

func (b *loginBearer) apply(req *http.Request) { b.cred.apply(req) }
func (b *loginBearer) renewable() bool         { return true }
func (b *loginBearer) expired() bool           { return false }
func (b *loginBearer) owns() casecfg.Owned     { return b.cred.owns() }
func (b *loginBearer) secrets() []string       { return append(b.cred.secrets(), b.login.pass) }

// fixedBearer sends a token the operator issued ahead of time.
type fixedBearer struct{ cred *credential }

func (b *fixedBearer) signIn(context.Context, *Upstream) error { return nil }
func (b *fixedBearer) apply(req *http.Request)                 { b.cred.apply(req) }
func (b *fixedBearer) renewable() bool                         { return false }
func (b *fixedBearer) expired() bool                           { return false }
func (b *fixedBearer) owns() casecfg.Owned                     { return b.cred.owns() }
func (b *fixedBearer) secrets() []string                       { return b.cred.secrets() }

// apiKey sends a key in a header or a query parameter.
type apiKey struct {
	key, header, prefix, query string
	owned                      casecfg.Owned
}

func (k *apiKey) signIn(context.Context, *Upstream) error { return nil }
func (k *apiKey) secrets() []string                       { return []string{k.key} }
func (k *apiKey) renewable() bool                         { return false }
func (k *apiKey) expired() bool                           { return false }

func (k *apiKey) owns() casecfg.Owned { return k.owned }

func (k *apiKey) apply(req *http.Request) {
	if k.header != "" {
		req.Header.Set(k.header, k.prefix+k.key)
		return
	}
	q := req.URL.Query()
	q.Set(k.query, k.key)
	req.URL.RawQuery = q.Encode()
}

// maxRenewLead is how long before expiry a token is replaced; a token that
// lives less than twice that is replaced halfway through its life instead.
const maxRenewLead = 30 * time.Second

// oauth2ClientCredentials is a machine identity: it trades a client id and
// secret for an access token at the token endpoint and renews it before it
// expires. A refused request for a token is sticky like any refused sign-in.
type oauth2ClientCredentials struct {
	tokenURL   string
	id, secret string
	scope      string
	clientAuth string
	cred       *credential
	now        func() time.Time
}

func newOAuth2(cfg casecfg.AuthConfig, need func(name, field string) (string, error)) (*oauth2ClientCredentials, error) {
	id, err := need(cfg.ClientIDEnv, "client_id_env")
	if err != nil {
		return nil, err
	}
	secret, err := need(cfg.ClientSecretEnv, "client_secret_env")
	if err != nil {
		return nil, err
	}
	return &oauth2ClientCredentials{
		tokenURL: cfg.TokenURL, id: id, secret: secret, scope: cfg.Scope, clientAuth: cfg.ClientAuth,
		cred: newCredential(cfg, "Bearer "), now: time.Now,
	}, nil
}

func (o *oauth2ClientCredentials) signIn(ctx context.Context, u *Upstream) error {
	form := url.Values{"grant_type": {"client_credentials"}}
	if o.scope != "" {
		form.Set("scope", o.scope)
	}
	if o.clientAuth == casecfg.ClientAuthBody {
		form.Set("client_id", o.id)
		form.Set("client_secret", o.secret)
	}
	body := []byte(form.Encode())
	var req *http.Request
	var err error
	if strings.HasPrefix(o.tokenURL, "/") {
		req, err = u.NewRequest(ctx, http.MethodPost, o.tokenURL, nil, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, bytes.NewReader(body))
	}
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if o.clientAuth != casecfg.ClientAuthBody {
		// RFC 6749 §2.3.1: the id and secret are form-encoded before Basic.
		req.SetBasicAuth(url.QueryEscape(o.id), url.QueryEscape(o.secret))
	}
	resp, err := u.send(req)
	if err != nil {
		return fmt.Errorf("%w: request token: %w", errUnreachable, err)
	}
	if resp.Status/100 != 2 {
		return fmt.Errorf("%w: token endpoint answered status %d", ErrLoginFailed, resp.Status)
	}
	var tok struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("%w: no access_token in the token response", ErrLoginFailed)
	}
	var expires time.Time
	var seconds float64
	// Some servers send expires_in as a string; accept both.
	if json.Unmarshal(bytes.Trim(tok.ExpiresIn, `"`), &seconds) == nil && seconds > 0 {
		life := time.Duration(seconds * float64(time.Second))
		expires = o.now().Add(life - min(maxRenewLead, life/2))
	}
	o.cred.set(tok.AccessToken, expires)
	return nil
}

func (o *oauth2ClientCredentials) apply(req *http.Request) { o.cred.apply(req) }
func (o *oauth2ClientCredentials) renewable() bool         { return true }
func (o *oauth2ClientCredentials) expired() bool           { return o.cred.expiredAt(o.now()) }
func (o *oauth2ClientCredentials) owns() casecfg.Owned     { return o.cred.owns() }
func (o *oauth2ClientCredentials) secrets() []string {
	return append(o.cred.secrets(), o.secret)
}

// lookupString follows a dot path through a JSON object, e.g. data.token.
func lookupString(body []byte, path []string) (string, bool) {
	var node any
	if err := json.Unmarshal(body, &node); err != nil {
		return "", false
	}
	for _, key := range path {
		obj, ok := node.(map[string]any)
		if !ok {
			return "", false
		}
		node = obj[key]
	}
	s, ok := node.(string)
	return s, ok && s != ""
}
