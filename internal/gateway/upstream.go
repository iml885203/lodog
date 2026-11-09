package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/redact"
)

// ErrLoginFailed means the legacy system refused the gateway's credentials.
// It is sticky: the gateway never signs in again after a refusal, because
// Backoffice deactivates an operator after five wrong passwords and a retrying
// gateway would lock its own account.
var ErrLoginFailed = errors.New("upstream login failed")

// errUnreachable marks a sign-in that never got an answer. Unlike a refusal it
// is not sticky: no credential was judged, so asking again cannot lock an
// account, and a gateway that lost the network for a moment must recover.
var errUnreachable = errors.New("upstream unreachable")

// Upstream is the legacy system as the gateway sees it: one signed-in
// identity shared by every tool call.
type Upstream struct {
	base   *url.URL
	auth   authenticator
	client *http.Client
	// defaults are added to every request that goes over the wire, sign-in
	// included, unless the request already carries the header.
	defaults http.Header

	mu       sync.Mutex
	signedIn bool
	loginErr error
}

// NewUpstream builds the client for base, signing in with auth.
func NewUpstream(base string, auth authenticator, insecureTLS bool) (*Upstream, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("upstream %q is not an absolute URL", base)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // G402: opt-in for a local self-signed dev upstream only
	}
	return &Upstream{
		base:   u,
		auth:   auth,
		client: &http.Client{Jar: jar, Transport: transport, Timeout: 60 * time.Second},
	}, nil
}

// Redaction is what a log line or an error for an agent must hide for this
// upstream: the scheme's parameter names, and every secret value held now or
// before, including the session cookies the legacy system issued. Call it
// each time: the secrets change when a token is renewed.
func (u *Upstream) Redaction() redact.Names {
	n := ownedNames(u.auth.owns())
	n.Values = u.auth.secrets()
	for _, c := range u.client.Jar.Cookies(u.base) {
		n.Values = append(n.Values, c.Value)
	}
	return n
}

// redactedError reads as msg but still unwraps to the original, so callers
// keep matching it with errors.Is and errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// scrub is the one exit every error leaves Upstream through: a Go URL error
// spells out the request URL, query credential included, and the gateway,
// the benchmark runner and the draft checks all print what they get.
func (u *Upstream) scrub(err error) error {
	if err == nil {
		return nil
	}
	if msg := u.Redaction().Text(err.Error()); msg != err.Error() {
		return &redactedError{msg: msg, err: err}
	}
	return err
}

// SetDefaultHeaders sets headers added to every request this upstream sends,
// sign-in included, unless the request already sets them. The benchmark runner
// uses it to label its own sign-in. Call it before the first request.
func (u *Upstream) SetDefaultHeaders(h http.Header) { u.defaults = h.Clone() }

// NewUpstreamFor builds the upstream cfg describes: its base URL, TLS choice
// and auth block. The gateway and the benchmark runner both come through it,
// so there is one sign-in implementation.
func NewUpstreamFor(cfg *casecfg.Config, getenv func(string) string) (*Upstream, error) {
	auth, err := newAuthenticator(cfg.Auth, getenv)
	if err != nil {
		return nil, err
	}
	return NewUpstream(cfg.Upstream, auth, cfg.UpstreamInsecureTLS)
}

// notSentError is a failure before the request left the gateway: nothing
// reached the legacy system, so an approval the call carried is still unspent.
type notSentError struct{ err error }

func (e *notSentError) Error() string { return e.err.Error() }
func (e *notSentError) Unwrap() error { return e.err }

// sent reports whether a request whose Do failed with err may have reached
// the legacy system. Only a failure known to precede sending says no.
func sent(err error) bool {
	var early *notSentError
	return !errors.As(err, &early)
}

// Response is what the legacy system answered.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends one request with the gateway's credential. It signs in first if
// needed, and once more if a renewable session has expired (401) — but never
// after a refused sign-in.
func (u *Upstream) Do(ctx context.Context, req *http.Request) (*Response, error) {
	resp, err := u.do(ctx, req)
	return resp, u.scrub(err)
}

func (u *Upstream) do(ctx context.Context, req *http.Request) (*Response, error) {
	if err := u.ensureSignedIn(ctx); err != nil {
		return nil, &notSentError{err: err}
	}
	u.auth.apply(req)
	resp, err := u.send(req)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusUnauthorized || !u.auth.renewable() {
		return resp, nil
	}
	u.mu.Lock()
	u.signedIn = false
	u.mu.Unlock()
	if err := u.ensureSignedIn(ctx); err != nil {
		return nil, err
	}
	u.auth.apply(req)
	return u.send(req)
}

// NewRequest builds a request against the upstream base URL.
func (u *Upstream) NewRequest(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Request, error) {
	target := *u.base
	target.Path = u.base.Path + path
	target.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// send transmits req as it is, without adding the credential: sign-in uses it.
func (u *Upstream) send(req *http.Request) (*Response, error) {
	// The recording proxy cannot see the auth config; tell it which query
	// parameters are credentials so it never files them.
	if q := u.auth.owns().Queries; len(q) > 0 {
		req.Header.Set(redact.HeaderQuery, redact.FormatHeader(q))
	}
	for k, v := range u.defaults {
		if _, ok := req.Header[k]; !ok {
			req.Header[k] = append([]string(nil), v...)
		}
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind request body: %w", err)
		}
		req.Body = body
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBody))
	if err != nil {
		return nil, fmt.Errorf("read upstream response: %w", err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// maxUpstreamBody bounds what one call may pull into memory. It is a safety
// limit, not response shaping: shaping is L2's job.
const maxUpstreamBody = 8 << 20

// EnsureSignedIn signs in if the upstream is not signed in yet, or its
// credential has expired. A refused sign-in is returned again on every later
// call without asking the legacy system.
func (u *Upstream) EnsureSignedIn(ctx context.Context) error {
	return u.scrub(u.ensureSignedIn(ctx))
}

func (u *Upstream) ensureSignedIn(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.loginErr != nil {
		return u.loginErr
	}
	if u.signedIn && !u.auth.expired() {
		return nil
	}
	if err := u.auth.signIn(ctx, u); err != nil {
		if !errors.Is(err, errUnreachable) {
			u.loginErr = err
		}
		return err
	}
	u.signedIn = true
	return nil
}
