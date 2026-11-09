// Package docfetch reads an API description from where its owner points: an
// http(s) URL or a file path. lodog init and lodog score both start from one,
// so they share this one way of fetching it.
package docfetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// MaxBytes bounds a document; an API description larger than this is not one
// lodog should hold in memory.
const MaxBytes = 32 << 20

// InsecureClient is the default client without TLS verification, for a
// document served with a self-signed certificate. Only for a local system the
// owner runs; it is opt-in behind the --insecure flags.
func InsecureClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // G402: opt-in --insecure, for a local self-signed certificate only
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}

// Fetch returns the bytes of src. client may be nil: a client with a timeout
// is used. The format is not checked here; spec.Parse does that.
func Fetch(ctx context.Context, client *http.Client, src string) ([]byte, error) {
	if src == "" {
		return nil, errors.New("no OpenAPI document given: pass a URL or a file path")
	}
	if u, err := url.Parse(src); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		b, err := os.ReadFile(filepath.Clean(src))
		if err != nil {
			return nil, fmt.Errorf("read OpenAPI document: %w", err)
		}
		return b, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI document: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		var unknown x509.UnknownAuthorityError
		if errors.As(err, &unknown) {
			return nil, fmt.Errorf("fetch OpenAPI document: %w (a local system with a self-signed certificate: add --insecure)", err)
		}
		return nil, fmt.Errorf("fetch OpenAPI document: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch OpenAPI document %s: %s", src, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI document: %w", err)
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("fetch OpenAPI document %s: larger than %d bytes", src, MaxBytes)
	}
	return b, nil
}
