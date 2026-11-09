package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/redact"
)

type memSink struct {
	mu    sync.Mutex
	calls []record.Call
}

func (m *memSink) Append(c record.Call) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
	return nil
}

// finished is the closing record of every call, in the order the calls started.
func (m *memSink) finished() []record.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []record.Call
	for _, c := range m.calls {
		if c.Phase == record.PhaseDone {
			out = append(out, c)
		}
	}
	return out
}

func (m *memSink) snapshot() []record.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]record.Call(nil), m.calls...)
}

type seen struct {
	path    string
	headers http.Header
	body    string
}

func newUpstream(t *testing.T, status int, got *seen) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = seen{path: r.URL.Path, headers: r.Header.Clone(), body: string(b)}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProxy(t *testing.T, upstream string, sink Sink) *httptest.Server {
	t.Helper()
	target, err := url.Parse(upstream + "/mgmt")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(target, nil, sink, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv
}

func send(t *testing.T, method, url, body string, headers map[string]string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestACallIsForwardedAndRecordedWithItsLabels(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusBadRequest, &got)
	sink := &memSink{}
	px := newProxy(t, up.URL, sink)

	send(t, http.MethodGet, px.URL+"/api/payment/transactions?transactionType=2&status=1", "", map[string]string{
		HeaderRun: "20261015-2031-L0", HeaderLevel: "L0", HeaderTask: "T03", HeaderAttempt: "2",
		HeaderActor: "agent:lodog-bench", HeaderOnBehalfOf: "user:alice", HeaderApproval: "apr_9",
	})

	if got.path != "/mgmt/api/payment/transactions" {
		t.Errorf("upstream path = %q", got.path)
	}
	if len(sink.finished()) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(sink.finished()))
	}
	assertLabelled(t, sink.finished()[0])
}

func assertLabelled(t *testing.T, c record.Call) {
	t.Helper()
	if c.RunID != "20261015-2031-L0" || c.Level != "L0" || c.TaskID != "T03" || c.Attempt != 2 || c.Seq != 1 {
		t.Errorf("labels = %+v", c)
	}
	if c.Path != "/api/payment/transactions" || c.Method != http.MethodGet || c.Status != http.StatusBadRequest {
		t.Errorf("call = %+v", c)
	}
	if q := url.Values(c.Query); q.Get("transactionType") != "2" || q.Get("status") != "1" {
		t.Errorf("query = %v", c.Query)
	}
	if c.Actor != "agent:lodog-bench" || c.OnBehalfOf != "user:alice" || c.ApprovalID == nil || *c.ApprovalID != "apr_9" {
		t.Errorf("identity = %+v", c)
	}
}

func TestLabelHeadersNeverReachTheLegacySystem(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	px := newProxy(t, up.URL, &memSink{})

	headers := map[string]string{}
	for _, h := range labelHeaders {
		headers[h] = "x"
	}
	send(t, http.MethodGet, px.URL+"/api/x", "", headers)

	for _, h := range labelHeaders {
		if got.headers.Get(h) != "" {
			t.Errorf("header %s reached upstream", h)
		}
	}
}

func TestLoginPasswordAndCookieAreNeverRecorded(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	sink := &memSink{}
	px := newProxy(t, up.URL, sink)

	send(t, http.MethodPost, px.URL+"/api/operator/login", `{"LoginName":"dev-admin","Password":"s3cret"}`,
		map[string]string{"Cookie": ".AspNetCore.Session=cookie-value", HeaderRun: "r1"})

	if !strings.Contains(got.body, "s3cret") {
		t.Fatal("login body must still be forwarded")
	}
	for _, c := range sink.snapshot() {
		encoded := strings.Join([]string{c.Path, c.Method, c.Error}, " ")
		for k, v := range c.Query {
			encoded += k + strings.Join(v, ",")
		}
		if strings.Contains(encoded, "s3cret") || strings.Contains(encoded, "cookie-value") {
			t.Fatalf("secret leaked into record: %+v", c)
		}
	}
}

func TestAnUnreachableUpstreamIsStillRecorded(t *testing.T) {
	sink := &memSink{}
	px := newProxy(t, "http://127.0.0.1:1", sink)

	send(t, http.MethodPost, px.URL+"/api/payment/transaction/withdraw/reject", "", map[string]string{HeaderRun: "r1", HeaderTask: "T07"})

	done := sink.finished()
	if len(done) != 1 || done[0].Status != http.StatusBadGateway || done[0].Error == "" {
		t.Fatalf("calls = %+v, want one 502 with the error", sink.snapshot())
	}
}

func TestSeqCountsPerTaskAttemptAndUnlabeledCallsAreKept(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	sink := &memSink{}
	px := newProxy(t, up.URL, sink)

	a1 := map[string]string{HeaderRun: "r1", HeaderTask: "T02", HeaderAttempt: "1"}
	a2 := map[string]string{HeaderRun: "r1", HeaderTask: "T02", HeaderAttempt: "2"}
	send(t, http.MethodGet, px.URL+"/a", "", a1)
	send(t, http.MethodGet, px.URL+"/b", "", a1)
	send(t, http.MethodGet, px.URL+"/c", "", a2)
	send(t, http.MethodGet, px.URL+"/d", "", nil)

	want := []int{1, 2, 1, 1}
	for i, c := range sink.finished() {
		if c.Seq != want[i] {
			t.Errorf("call %d seq = %d, want %d", i, c.Seq, want[i])
		}
	}
	if sink.finished()[3].RunID != record.UnlabeledRun {
		t.Errorf("unlabeled call run id = %q", sink.finished()[3].RunID)
	}
}

// A call that never gets an answer must already be on record: the upstream
// holds the request open and the test reads the sink meanwhile. Deleting the
// pre-forward Append makes this fail.
func TestACallIsRecordedBeforeItReachesTheLegacySystem(t *testing.T) {
	sink := &memSink{}
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unhold := func() { releaseOnce.Do(func() { close(release) }) }
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(arrived)
		<-release
	}))
	// Registered after up, so it runs first: a failed assertion must not leave
	// the held call blocking up.Close.
	t.Cleanup(up.Close)
	t.Cleanup(unhold)
	px := newProxy(t, up.URL, sink)

	done := make(chan struct{})
	go func() {
		defer close(done)
		send(t, http.MethodPost, px.URL+"/api/payment/transaction/withdraw/reject", "", map[string]string{HeaderRun: "r1", HeaderTask: "T07"})
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the legacy system")
	}
	before := sink.snapshot()
	unhold()
	<-done

	if len(before) != 1 || before[0].Phase != record.PhaseStarted || before[0].Path != "/api/payment/transaction/withdraw/reject" {
		t.Fatalf("records while the legacy system was still working = %+v, want the started record", before)
	}
}

func TestTheClosingRecordSharesTheCallIDAndCompletesIt(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	sink := &memSink{}
	px := newProxy(t, up.URL, sink)

	send(t, http.MethodGet, px.URL+"/a", "", map[string]string{HeaderRun: "r1", HeaderCall: "call_gw1"})
	send(t, http.MethodGet, px.URL+"/b", "", map[string]string{HeaderRun: "r1"})

	rs := sink.snapshot()
	if len(rs) != 4 {
		t.Fatalf("records = %d, want a started and a done line per call", len(rs))
	}
	if rs[0].ID != "call_gw1" || rs[1].ID != "call_gw1" || rs[0].Phase != record.PhaseStarted || rs[1].Phase != record.PhaseDone {
		t.Errorf("the gateway's call id must name both lines: %+v %+v", rs[0], rs[1])
	}
	if rs[2].ID == "" || rs[2].ID != rs[3].ID || rs[2].ID == rs[0].ID {
		t.Errorf("a call without a gateway id gets its own: %+v %+v", rs[2], rs[3])
	}
	if rs[0].Status != 0 || rs[1].Status != http.StatusOK || rs[0].Seq != rs[1].Seq {
		t.Errorf("started must not claim a status and done must keep the seq: %+v %+v", rs[0], rs[1])
	}
	if got.headers.Get(HeaderCall) != "" {
		t.Error("the call id header reached the legacy system")
	}
}

type failingSink struct{}

func (f *failingSink) Append(record.Call) error { return errors.New("disk full") }

func TestACallThatCannotBeRecordedIsNotForwarded(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	px := newProxy(t, up.URL, &failingSink{})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, px.URL+"/api/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || got.path != "" {
		t.Fatalf("status %d, upstream saw %q: an unrecorded call must be refused", resp.StatusCode, got.path)
	}
}

func TestAQueryCredentialTheGatewayNamedIsNeverRecorded(t *testing.T) {
	var got seen
	up := newUpstream(t, http.StatusOK, &got)
	sink := &memSink{}
	px := newProxy(t, up.URL, sink)

	send(t, http.MethodGet, px.URL+"/api/x?page=2&api_key=SENTINEL-key", "", map[string]string{HeaderRedactQuery: "api_key"})

	for _, c := range sink.snapshot() {
		if c.Query["api_key"][0] != redact.Mask || c.Query["page"][0] != "2" {
			t.Errorf("query = %v", c.Query)
		}
		if b, _ := json.Marshal(c); strings.Contains(string(b), "SENTINEL") {
			t.Errorf("record has the secret: %s", b)
		}
	}
}

func TestATransportErrorNeverCarriesTheCredentialIntoTheRecord(t *testing.T) {
	sink := &memSink{}
	target, _ := url.Parse("http://127.0.0.1:1")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("connection refused")}
	})
	srv := httptest.NewServer(New(target, transport, sink, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	send(t, http.MethodGet, srv.URL+"/api/x?api_key=SENTINEL-key", "", map[string]string{HeaderRedactQuery: "api_key"})

	done := sink.finished()
	if len(done) != 1 || done[0].Error == "" {
		t.Fatalf("want one failed record, got %+v", done)
	}
	if b, _ := json.Marshal(done[0]); strings.Contains(string(b), "SENTINEL") {
		t.Errorf("record has the secret: %s", b)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
