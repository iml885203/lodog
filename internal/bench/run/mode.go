package run

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/bench/proxy"
	"github.com/iml885203/lodog/internal/bench/record"
)

const modeTimeout = 10 * time.Second

// ProxyMode asks the recording proxy at upstream which mode it runs in. It
// returns "" when the answer is not a lodog proxy's: an older proxy, or the
// system itself.
func ProxyMode(ctx context.Context, upstream string, insecure bool, runID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, modeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(upstream, "/")+proxy.ModePath, nil)
	if err != nil {
		return "", fmt.Errorf("ask proxy mode: %w", err)
	}
	for k, v := range labels(runID, preflightTask, 0) {
		req.Header[k] = v
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // G402: same opt-in as the gateway's upstream_insecure_tls
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("ask proxy mode at %s: %w", upstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Mode string `json:"mode"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil || resp.StatusCode != http.StatusOK || json.Unmarshal(b, &body) != nil || resp.Header.Get(proxy.HeaderMode) != body.Mode {
		return "", nil
	}
	switch body.Mode {
	case record.ModeFull, record.ModeReadOnly, record.ModeReplay:
		return body.Mode, nil
	}
	return "", nil
}

// runMode is the mode the run records. A read-only run needs a proxy that
// does not send writes; a full run refuses one, because its writes would
// never land and every state check would pass without proving anything.
func runMode(ctx context.Context, readOnly bool, upstream string, insecure bool, runID string) (string, error) {
	mode, err := ProxyMode(ctx, upstream, insecure, runID)
	if readOnly {
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrPreflight, err)
		}
		if record.Sends(mode) {
			return "", fmt.Errorf("%w: --read-only needs a lodog bench proxy started with --read-only or --replay at %s; it answered mode %q", ErrPreflight, upstream, mode)
		}
		return mode, nil
	}
	if err == nil && mode != "" && !record.Sends(mode) {
		return "", fmt.Errorf("%w: the proxy at %s runs %s and does not send writes; pass --read-only", ErrPreflight, upstream, mode)
	}
	return record.ModeFull, nil
}
