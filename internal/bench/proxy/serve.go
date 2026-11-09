package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/iml885203/lodog/internal/bench/record"
)

// Options configures a running proxy.
type Options struct {
	Listen   string
	Upstream string
	RunsDir  string
	// Insecure skips TLS verification of the upstream, for the local environment tool
	// env whose certificate is self-signed.
	Insecure bool
	// Guard, when set, makes the proxy read-only: only the calls it allows
	// reach the system.
	Guard *Guard
	// ReplayFile answers allowed calls from this recording instead of a
	// system; it needs Guard and no Upstream.
	ReplayFile string
	// RecordFile keeps every answer the system gives, for a later replay; it
	// needs Guard.
	RecordFile string
}

// ErrBadOptions is returned for a combination of modes that cannot run.
var ErrBadOptions = errors.New("invalid proxy options")

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Serve runs the proxy until ctx is cancelled.
func Serve(ctx context.Context, opts Options, log *slog.Logger) error {
	writer := record.NewWriter(opts.RunsDir)
	px, closeAll, err := build(opts, writer, log)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              opts.Listen,
		Handler:           px,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("recording proxy started", "listen", opts.Listen, "upstream", opts.Upstream, "runs", opts.RunsDir, "mode", px.Mode(), "record", opts.RecordFile, "replay", opts.ReplayFile)

	select {
	case err = <-errCh:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		err = srv.Shutdown(shutdownCtx)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, writer.Close(), closeAll())
}

// build makes the proxy the options ask for.
func build(opts Options, sink Sink, log *slog.Logger) (*Proxy, func() error, error) {
	noop := func() error { return nil }
	if opts.ReplayFile != "" {
		if opts.Guard == nil || opts.Upstream != "" || opts.RecordFile != "" {
			return nil, nil, fmt.Errorf("%w: a replay needs --case and takes neither --upstream nor --record", ErrBadOptions)
		}
		rec, err := LoadRecording(opts.ReplayFile)
		if err != nil {
			return nil, nil, err
		}
		return New(&url.URL{Scheme: "http", Host: "replay.invalid"}, nil, sink, log).Replaying(opts.Guard, rec), noop, nil
	}
	target, err := url.Parse(opts.Upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, nil, fmt.Errorf("upstream must be an absolute URL, got %q", opts.Upstream)
	}
	var transport http.RoundTripper
	if opts.Insecure {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in flag, only for the local environment tool env's self-signed certificate
		transport = t
	}
	px := New(target, transport, sink, log)
	if opts.Guard != nil {
		px = px.ReadOnly(opts.Guard)
	}
	if opts.RecordFile == "" {
		return px, noop, nil
	}
	if opts.Guard == nil {
		return nil, nil, fmt.Errorf("%w: --record needs --read-only, whose guard keeps sign-in answers out of the recording", ErrBadOptions)
	}
	recorder, err := NewRecorder(opts.RecordFile)
	if err != nil {
		return nil, nil, err
	}
	return px.Recording(recorder), recorder.Close, nil
}
