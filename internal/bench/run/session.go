package run

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
)

// ErrPreflight means the run was refused before any attempt started.
var ErrPreflight = errors.New("preflight failed")

// Session is the runner's own signed-in client to the legacy system, through
// the same upstream (the recording proxy) and the same authenticator the
// gateway uses. The runner uses it to read state, never to act for the agent.
// Its calls carry a task label starting with score.InternalTaskPrefix so the
// scorer keeps them apart.
type Session struct {
	upstream *gateway.Upstream
	runID    string
}

// preflightTask labels the sign-in, and any renewal of it.
const preflightTask = score.InternalTaskPrefix + "preflight"

// NewSession signs in once. Every attempt restarts the gateway, which signs in
// again; with a wrong password that would lock the operator account (Backoffice
// deactivates it after five failures), so a refused sign-in here stops the run
// after one failure.
func NewSession(ctx context.Context, cfg *casecfg.Config, getenv func(string) string, runID string) (*Session, error) {
	upstream, err := gateway.NewUpstreamFor(cfg, getenv)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	upstream.SetDefaultHeaders(labels(runID, preflightTask, 0))
	if err := upstream.EnsureSignedIn(ctx); err != nil {
		if !errors.Is(err, gateway.ErrLoginFailed) {
			err = fmt.Errorf("upstream %s unreachable (is the recording proxy running?): %w", cfg.Upstream, err)
		}
		return nil, fmt.Errorf("%w: %w; not starting, so retries cannot lock the account", ErrPreflight, err)
	}
	return &Session{upstream: upstream, runID: runID}, nil
}

func labels(runID, task string, attempt int) http.Header {
	h := http.Header{}
	h.Set(proxy.HeaderRun, runID)
	h.Set(proxy.HeaderTask, task)
	h.Set(proxy.HeaderAttempt, fmt.Sprint(attempt))
	return h
}

// Do sends "METHOD /path?query" labelled as task/attempt and returns the
// status and body.
func (s *Session) Do(ctx context.Context, call, task string, attempt int, body []byte) (int, []byte, error) {
	method, target, ok := strings.Cut(strings.TrimSpace(call), " ")
	if !ok {
		return 0, nil, fmt.Errorf("%w: %q", score.ErrBadEndpoint, call)
	}
	path, rawQuery, _ := strings.Cut(target, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %q: %w", score.ErrBadEndpoint, call, err)
	}
	req, err := s.upstream.NewRequest(ctx, method, path, query, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range labels(s.runID, task, attempt) {
		req.Header[k] = v
	}
	resp, err := s.upstream.Do(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

// CheckState runs a task's state check and reports whether the data is as it
// should be after an attempt that must not have changed it.
func (s *Session) CheckState(ctx context.Context, sc score.StateCheck, attempt int, now time.Time) (bool, error) {
	call := score.ExpandCall(sc.Call, now)
	status, body, err := s.Do(ctx, call, score.InternalTaskPrefix+"state_check", attempt, nil)
	if err != nil {
		return false, err
	}
	if status >= http.StatusBadRequest {
		return false, fmt.Errorf("state check %s returned HTTP %d", call, status)
	}
	return score.Holds(string(body), sc.Include, sc.Exclude), nil
}
