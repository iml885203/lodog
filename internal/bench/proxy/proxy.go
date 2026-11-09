// Package proxy is the recording proxy that sits in front of the legacy
// system. It forwards every call unchanged and writes one call record per call
// (docs/contracts.md, section 1). The scorer trusts only these records, never
// what the agent says it did.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/redact"
)

// Headers the gateway sets to label a call. The proxy reads them and removes
// them before forwarding, so the legacy system never sees them.
const (
	HeaderRun        = "X-Lodog-Run"
	HeaderLevel      = "X-Lodog-Level"
	HeaderTask       = "X-Lodog-Task"
	HeaderAttempt    = "X-Lodog-Attempt"
	HeaderActor      = "X-Lodog-Actor"
	HeaderOnBehalfOf = "X-Lodog-On-Behalf-Of"
	HeaderApproval   = "X-Lodog-Approval"
	// HeaderCall is the gateway's id for the call; the proxy files its records
	// under it so the two layers' records match. Absent, the proxy makes one.
	HeaderCall = "X-Lodog-Call"
	// HeaderRedactQuery names the query parameters that carry the gateway's
	// credential; their values are never recorded.
	HeaderRedactQuery = redact.HeaderQuery
)

var labelHeaders = []string{
	HeaderRun, HeaderLevel, HeaderTask, HeaderAttempt,
	HeaderActor, HeaderOnBehalfOf, HeaderApproval, HeaderCall, HeaderRedactQuery,
}

// Sink receives the records of each call: one before it is forwarded, one
// after the legacy system answered or failed.
type Sink interface {
	Append(record.Call) error
}

// ModePath is answered by the proxy itself, never forwarded: the runner asks
// it which mode the proxy runs in before it starts.
const ModePath = "/_lodog/mode"

// HeaderMode is set on every answer with the proxy's mode.
const HeaderMode = "X-Lodog-Proxy-Mode"

// Proxy forwards to one upstream and records every call.
type Proxy struct {
	forward *httputil.ReverseProxy
	sink    Sink
	log     *slog.Logger
	now     func() time.Time

	mode     string
	guard    *Guard
	replay   Recording
	recorder *Recorder

	mu  sync.Mutex
	seq map[string]int
}

// New returns a proxy to target. transport may be nil for the default one.
func New(target *url.URL, transport http.RoundTripper, sink Sink, log *slog.Logger) *Proxy {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			for _, h := range labelHeaders {
				pr.Out.Header.Del(h)
			}
			if _, keeping := pr.In.Context().Value(keepKey{}).(*keeper); keeping {
				pr.Out.Header.Del("Accept-Encoding")
			}
		},
		Transport:      transport,
		ModifyResponse: keepAnswer,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if f, ok := r.Context().Value(failureKey{}).(*failure); ok {
				f.err = err
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return &Proxy{forward: rp, sink: sink, log: log, now: time.Now, seq: map[string]int{}, mode: record.ModeFull}
}

// ReadOnly makes p send only the calls g allows; every other call is
// recorded with its body and answered with a made-up success.
func (p *Proxy) ReadOnly(g *Guard) *Proxy {
	p.guard, p.mode = g, record.ModeReadOnly
	return p
}

// Replaying makes p answer the calls g allows from rec and never reach the
// system; every other call is handled as in ReadOnly.
func (p *Proxy) Replaying(g *Guard, rec Recording) *Proxy {
	p.guard, p.replay, p.mode = g, rec, record.ModeReplay
	return p
}

// Recording makes p keep every answer the system gives, for a later replay.
// Only a read-only proxy records: its guard knows which calls are sign-ins,
// whose answers are kept without their body.
func (p *Proxy) Recording(r *Recorder) *Proxy {
	p.recorder = r
	return p
}

// Mode is the mode p runs in.
func (p *Proxy) Mode() string { return p.mode }

// failure carries a transport error from ReverseProxy's error handler back to
// ServeHTTP, which owns the record.
type failure struct{ err error }

type failureKey struct{}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(HeaderMode, p.mode)
	if r.URL.Path == ModePath {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, "{\"mode\":%q}\n", p.mode)
		return
	}
	call := p.label(r)
	secret := redact.Names{Queries: redact.ParseHeader(r.Header.Get(HeaderRedactQuery))}
	start := p.now()
	call.TS = start.UTC()
	call.Seq = p.nextSeq(call)
	call.Phase = record.PhaseStarted
	v := p.decide(r, &call)

	// Record before forwarding: a call that hangs or kills the proxy still
	// left a line. If the line cannot be written the call is refused, because
	// an unrecorded call is one the scorer and the audit cannot see.
	if err := p.sink.Append(call); err != nil {
		p.log.Error("record call before forwarding; refusing it", "err", err, "run_id", call.RunID, "method", call.Method, "path", call.Path)
		http.Error(w, "lodog proxy could not record this call, so it was not forwarded", http.StatusServiceUnavailable)
		return
	}

	switch {
	case !v.send:
		call.Status = writeStub(w, v.op, v.reason)
	case p.replay != nil:
		call.Status, call.Error, call.Replayed = p.answerFromRecording(w, r, secret)
	default:
		call.Status, call.Error = p.send(w, r, secret, v.signIn)
	}
	call.Phase = record.PhaseDone
	call.DurationMS = p.now().Sub(start).Milliseconds()
	if err := p.sink.Append(call); err != nil {
		p.log.Error("record call result", "err", err, "run_id", call.RunID, "method", call.Method, "path", call.Path)
	}
}

// decide asks the guard whether to send the call; with no guard every call
// is sent. A call that is not sent keeps its body in the record when it is an
// operation of the document, the agent's full request to the system.
func (p *Proxy) decide(r *http.Request, call *record.Call) verdict {
	if p.guard == nil {
		return verdict{send: true}
	}
	v := p.guard.check(r.Method, r.URL.Path)
	if v.send {
		return v
	}
	call.DryRun, call.Held = true, v.reason
	if v.op != nil && r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, record.MaxBodyBytes+1))
		if err != nil {
			call.Error = "read body: " + err.Error()
		}
		call.BodyTruncated = len(b) > record.MaxBodyBytes
		call.Body = string(b[:min(len(b), record.MaxBodyBytes)])
	}
	return v
}

func (p *Proxy) send(w http.ResponseWriter, r *http.Request, secret redact.Names, signIn bool) (int, string) {
	fail := &failure{}
	ctx := context.WithValue(r.Context(), failureKey{}, fail)
	if p.recorder != nil {
		ctx = context.WithValue(ctx, keepKey{}, &keeper{
			recorder: p.recorder, log: p.log, signIn: signIn,
			key: recordingKey(r.Method, r.URL.Path, r.URL.Query(), secret.Queries),
		})
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	p.forward.ServeHTTP(rec, r.WithContext(ctx))
	if fail.err != nil {
		return rec.status, secret.Text(fail.err.Error())
	}
	return rec.status, ""
}

// errNotRecorded answers a replayed call the recording does not hold.
const errNotRecorded = "not in the recording"

func (p *Proxy) answerFromRecording(w http.ResponseWriter, r *http.Request, secret redact.Names) (int, string, bool) {
	rec, ok := p.replay[recordingKey(r.Method, r.URL.Path, r.URL.Query(), secret.Queries)]
	if !ok {
		http.Error(w, "lodog replay: "+errNotRecorded, http.StatusBadGateway)
		return http.StatusBadGateway, errNotRecorded, false
	}
	rec.write(w)
	return rec.Status, "", true
}

// keeper records the system's answer to one call for a later replay.
type keeper struct {
	recorder *Recorder
	log      *slog.Logger
	key      string
	signIn   bool
}

type keepKey struct{}

// keepAnswer copies the answer into the recording on its way back. An answer
// too large to keep is passed on unrecorded, and a replay misses it.
func keepAnswer(resp *http.Response) error {
	k, ok := resp.Request.Context().Value(keepKey{}).(*keeper)
	if !ok {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordedBody+1))
	rest := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(b), rest), rest}
	if err != nil || len(b) > maxRecordedBody || resp.Header.Get("Content-Encoding") != "" {
		k.log.Warn("answer not recorded", "key", k.key, "err", err, "bytes", len(b), "content_encoding", resp.Header.Get("Content-Encoding"))
		return nil
	}
	rec := Recorded{
		Key: k.key, Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"),
		Cookies: cookieNames(resp.Header),
	}
	if !k.signIn {
		rec.Body = string(b)
	}
	if err := k.recorder.Add(rec); err != nil {
		k.log.Error("record answer", "key", k.key, "err", err)
	}
	return nil
}

// label builds the record from the request. Only the method, path, query and
// labels are kept (a query value the gateway marked as a credential is masked): bodies, cookies and other headers are never recorded, which
// is what keeps /login passwords and session cookies out of every record.
func (p *Proxy) label(r *http.Request) record.Call {
	h := r.Header
	call := record.Call{
		RunID:      h.Get(HeaderRun),
		Level:      h.Get(HeaderLevel),
		TaskID:     h.Get(HeaderTask),
		Actor:      h.Get(HeaderActor),
		OnBehalfOf: h.Get(HeaderOnBehalfOf),
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      redact.Query(r.URL.Query(), redact.ParseHeader(h.Get(HeaderRedactQuery))),
	}
	call.ID = h.Get(HeaderCall)
	if call.ID == "" {
		call.ID = newCallID()
	}
	if call.RunID == "" {
		call.RunID = record.UnlabeledRun
	}
	if v := h.Get(HeaderAttempt); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			p.log.Warn("ignore non-numeric attempt header", "value", v, "run_id", call.RunID)
		}
		call.Attempt = n
	}
	if v := h.Get(HeaderApproval); v != "" {
		call.ApprovalID = &v
	}
	return call
}

func (p *Proxy) nextSeq(c record.Call) int {
	key := fmt.Sprintf("%s\x00%s\x00%d", c.RunID, c.TaskID, c.Attempt)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq[key]++
	return p.seq[key]
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer for flushing.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func newCallID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// A random id is only a join key; the clock and seq still tell calls apart.
		return fmt.Sprintf("call_t%d", time.Now().UnixNano())
	}
	return "call_" + hex.EncodeToString(b)
}
