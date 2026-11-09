// Package casecfg is the one place that says what a case is: where it keeps
// its files, what its gateway config says (level, upstream, auth, identity,
// approvals) and how its document is loaded as a level serves it. It depends
// on nothing of the gateway's runtime, so the drafting commands, the score and
// the benchmark read a case without linking the MCP server.
package casecfg

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The auth types a gateway can use against a legacy system.
const (
	AuthCookieSession = "cookie-session"
	AuthBearer        = "bearer"
	AuthAPIKey        = "api-key"
	AuthOAuth2        = "oauth2-client-credentials"
)

// The places OAuth2 client credentials can travel.
const (
	ClientAuthBasic = "basic"
	ClientAuthBody  = "body"
)

// AuthConfig says how the gateway signs in to the legacy system. Secrets are
// named by environment variable, never written here. See docs/contracts.md §2.
type AuthConfig struct {
	Type string `yaml:"type"` // cookie-session | bearer | api-key | oauth2-client-credentials

	// cookie-session, and bearer obtained by logging in.
	Login         string `yaml:"login"`          // "POST /api/operator/login"
	LoginFormat   string `yaml:"login_format"`   // json (default) | form
	UsernameField string `yaml:"username_field"` // body field for the username; default "username"
	PasswordField string `yaml:"password_field"` // default "password"
	UsernameEnv   string `yaml:"username_env"`
	PasswordEnv   string `yaml:"password_env"`
	// Success is what a sign-in response must show beyond a 2xx status.
	Success SuccessConfig `yaml:"success"`

	// bearer: either a fixed token, or a token read from the login response.
	TokenEnv  string `yaml:"token_env"`
	TokenPath string `yaml:"token_path"` // dot path in the login response, e.g. data.token

	// api-key: the key goes in exactly one of header or query.
	KeyEnv string `yaml:"key_env"`
	Header string `yaml:"header"`
	Query  string `yaml:"query"`

	// bearer and oauth2-client-credentials send the token in Header (default
	// Authorization); api-key sends the key in Header. Prefix goes in front
	// of the value exactly as written, trailing space included: default
	// "Bearer " for tokens, none for api-key. A pointer tells "unset" from
	// "empty".
	Prefix *string `yaml:"prefix"`

	// oauth2-client-credentials: TokenURL is absolute, or a path on the
	// upstream. ClientAuth says where the client credentials travel: basic
	// (HTTP Basic, default) or body (form fields).
	TokenURL        string `yaml:"token_url"`
	ClientIDEnv     string `yaml:"client_id_env"`
	ClientSecretEnv string `yaml:"client_secret_env"`
	Scope           string `yaml:"scope"`
	ClientAuth      string `yaml:"client_auth"`
}

// SuccessConfig sets what makes a sign-in count as successful. The default is
// any 2xx; systems that answer a wrong password with 200 name the evidence of
// a real sign-in.
type SuccessConfig struct {
	// CookieName requires the response to issue a cookie with this name.
	CookieName string `yaml:"cookie_name"`
}

// The encodings a login body can use.
const (
	LoginFormatJSON = "json"
	LoginFormatForm = "form"
)

// LoginBody encodes the login credentials the way auth.login_format says and
// returns the body with its Content-Type. The gateway and the benchmark
// runner both sign in through it, so they cannot disagree on the encoding.
func (a AuthConfig) LoginBody(user, pass string) (contentType string, body []byte, err error) {
	userField, passField := a.UsernameField, a.PasswordField
	if userField == "" {
		userField = "username"
	}
	if passField == "" {
		passField = "password"
	}
	switch a.LoginFormat {
	case "", LoginFormatJSON:
		body, err = json.Marshal(map[string]string{userField: user, passField: pass})
		if err != nil {
			return "", nil, fmt.Errorf("encode login: %w", err)
		}
		return "application/json", body, nil
	case LoginFormatForm:
		form := url.Values{userField: {user}}
		form.Set(passField, pass)
		return "application/x-www-form-urlencoded", []byte(form.Encode()), nil
	default:
		return "", nil, fmt.Errorf("auth.login_format %q is not one of %s, %s", a.LoginFormat, LoginFormatJSON, LoginFormatForm)
	}
}

// Owned is the request parameters an auth scheme sets itself, so an agent is
// never shown, asked for or told them. Names are matched case-insensitively
// for headers and cookies.
type Owned struct {
	Headers []string
	Queries []string
	// Cookies is true when the scheme sends cookies of its own (a session):
	// every cookie parameter is then the gateway's.
	Cookies bool
}

// Owned declares what the scheme sets itself. It reads the config alone, so
// the gateway's authenticators and the document a level serves agree.
func (a AuthConfig) Owned() Owned {
	switch a.Type {
	case AuthCookieSession:
		return Owned{Cookies: true}
	case AuthAPIKey:
		if a.Header != "" {
			return Owned{Headers: []string{a.Header}}
		}
		return Owned{Queries: []string{a.Query}}
	}
	return Owned{Headers: []string{a.TokenHeader()}}
}

// TokenHeader is the header a bearer or OAuth2 token travels in.
func (a AuthConfig) TokenHeader() string {
	if a.Header == "" {
		return "Authorization"
	}
	return a.Header
}

// Validate refuses an auth block that cannot work whatever the secrets are:
// an unknown type, a missing field the type needs, a malformed value. What
// the environment holds is checked where the secrets are read.
func (a AuthConfig) Validate() error {
	switch a.Type {
	case AuthCookieSession:
		return a.validateLogin()
	case AuthBearer:
		return a.validateBearer()
	case AuthAPIKey:
		return a.validateAPIKey()
	case AuthOAuth2:
		return a.validateOAuth2()
	}
	return fmt.Errorf("auth.type %q is not one of %s, %s, %s, %s", a.Type,
		AuthCookieSession, AuthBearer, AuthAPIKey, AuthOAuth2)
}

func (a AuthConfig) need(name, field string) error {
	if name == "" {
		return fmt.Errorf("auth.%s is required for auth.type %s", field, a.Type)
	}
	return nil
}

func (a AuthConfig) validateBearer() error {
	if a.TokenEnv != "" {
		return nil
	}
	if a.TokenPath == "" {
		return fmt.Errorf("auth.type bearer needs either token_env (a fixed token) or login + token_path")
	}
	return a.validateLogin()
}

func (a AuthConfig) validateAPIKey() error {
	if err := a.need(a.KeyEnv, "key_env"); err != nil {
		return err
	}
	if (a.Header == "") == (a.Query == "") {
		return fmt.Errorf("auth.type api-key needs exactly one of header or query")
	}
	if a.Query != "" && a.Prefix != nil {
		return fmt.Errorf("auth.prefix applies to a header, not to a query parameter")
	}
	return nil
}

func (a AuthConfig) validateOAuth2() error {
	if !strings.HasPrefix(a.TokenURL, "/") {
		u, err := url.Parse(a.TokenURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("auth.token_url %q must be an absolute URL or a path starting with /", a.TokenURL)
		}
	}
	if err := a.need(a.ClientIDEnv, "client_id_env"); err != nil {
		return err
	}
	if err := a.need(a.ClientSecretEnv, "client_secret_env"); err != nil {
		return err
	}
	switch a.ClientAuth {
	case "", ClientAuthBasic, ClientAuthBody:
		return nil
	}
	return fmt.Errorf("auth.client_auth %q is not one of %s, %s", a.ClientAuth, ClientAuthBasic, ClientAuthBody)
}

func (a AuthConfig) validateLogin() error {
	_, path, ok := strings.Cut(strings.TrimSpace(a.Login), " ")
	if !ok || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("auth.login %q must look like \"POST /path\"", a.Login)
	}
	if err := a.need(a.UsernameEnv, "username_env"); err != nil {
		return err
	}
	if err := a.need(a.PasswordEnv, "password_env"); err != nil {
		return err
	}
	// Encode once now so an unknown login_format fails at startup, not at the
	// first call.
	_, _, err := a.LoginBody("", "")
	return err
}
