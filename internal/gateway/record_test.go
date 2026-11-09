package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lockedBuffer is a log sink a test can read while the gateway still writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var callIDPattern = regexp.MustCompile(`call_id=(call_[0-9a-f]+)`)

// linesWith returns the log lines that contain every fragment.
func linesWith(logs string, fragments ...string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		ok := line != ""
		for _, f := range fragments {
			ok = ok && strings.Contains(line, f)
		}
		if ok {
			out = append(out, line)
		}
	}
	return out
}

// hold makes the legacy system sit on a call until released; arrived fires
// when the call is in its hands.
func hold() (arrive func(), arrived <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var signalled, released sync.Once
	return func() { signalled.Do(func() { close(in) }); <-out }, in, func() { released.Do(func() { close(out) }) }
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the legacy system")
	}
}

// assertRecordedThenClosed checks the Record before forwarding invariant for
// one gateway: while the legacy system is still holding the call its opening
// line is already in the log, and the closing line carries the same id.
func assertRecordedThenClosed(t *testing.T, logs *lockedBuffer, arrived <-chan struct{}, release func(), call func()) {
	t.Helper()
	// Release even when an assertion fails, or the held call blocks cleanup.
	defer release()
	done := make(chan struct{})
	go func() { defer close(done); call() }()
	waitFor(t, arrived)

	opening := linesWith(logs.String(), "tool call forwarding")
	if len(opening) != 1 {
		t.Fatalf("while the legacy system was holding the call the log had %d opening lines, want 1:\n%s", len(opening), logs.String())
	}
	m := callIDPattern.FindStringSubmatch(opening[0])
	if m == nil {
		t.Fatalf("the opening line has no call id: %s", opening[0])
	}
	if len(linesWith(logs.String(), "status=")) != 0 {
		t.Fatal("a status was logged before the legacy system answered")
	}

	release()
	<-done
	closing := linesWith(logs.String(), `msg="tool call"`, "call_id="+m[1], "status=200", "duration_ms=")
	if len(closing) != 1 {
		t.Fatalf("want one closing line for %s, got %d:\n%s", m[1], len(closing), logs.String())
	}
	if n := len(linesWith(logs.String(), "call_id="+m[1], "tool call forwarding")); n != 1 {
		t.Errorf("call %s counted %d times", m[1], n)
	}
}

func TestL0RecordsACallBeforeForwardingIt(t *testing.T) {
	arrive, arrived, release := hold()
	h := newHarness(t, &fakeLegacy{arrive: arrive})
	assertRecordedThenClosed(t, h.logs, arrived, release, func() {
		h.call(t, map[string]any{"PageSize": 5}, nil)
	})
}

func TestL3RecordsACallBeforeForwardingIt(t *testing.T) {
	arrive, arrived, release := hold()
	h := newL3(t)
	h.legacy.arrive = arrive
	assertRecordedThenClosed(t, h.logs, arrived, release, func() {
		h.call(t, list, map[string]any{"PageSize": 5})
	})
}

// A legacy system that drops the connection must still leave the call in the
// log, with the failure under the same id.
func TestAnUnreachableLegacySystemStillLeavesTheCallInTheLog(t *testing.T) {
	h := newHarness(t, &fakeLegacy{arrive: func() { panic(http.ErrAbortHandler) }})
	res, _ := h.call(t, map[string]any{"PageSize": 5}, nil)
	if !res.IsError {
		t.Fatal("want a tool error")
	}
	opening := linesWith(h.logs.String(), "tool call forwarding")
	if len(opening) != 1 {
		t.Fatalf("opening lines = %d, want 1:\n%s", len(opening), h.logs.String())
	}
	id := callIDPattern.FindStringSubmatch(opening[0])[1]
	if len(linesWith(h.logs.String(), "tool call failed", "call_id="+id)) != 1 {
		t.Errorf("no failure line for %s:\n%s", id, h.logs.String())
	}
}

func TestTheCallIDReachesTheRecordingProxy(t *testing.T) {
	legacy := &fakeLegacy{}
	h := newHarness(t, legacy)
	h.call(t, map[string]any{"PageSize": 5}, nil)
	id := callIDPattern.FindStringSubmatch(h.logs.String())[1]
	if got := legacy.calls[0].Header.Get(headerCall); got != id {
		t.Errorf("%s header = %q, want the logged id %q", headerCall, got, id)
	}
}

const petstoreDoc = `{"openapi":"3.0.4","paths":{"/pets/{petId}":{"get":{"operationId":"showPetById","parameters":[
	{"name":"petId","in":"path","required":true,"schema":{"type":"string"}}]}}}}`

// The guarantee belongs to the gateway, not to one legacy system: a public
// pet-store document with api-key auth behaves the same.
func TestRecordBeforeForwardingHoldsForAnyOpenAPIDocument(t *testing.T) {
	arrive, arrived, release := hold()
	up := httptest.NewServer(&recorder{arrive: arrive})
	t.Cleanup(up.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(petstoreDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: up.URL,
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "api_key"},
		Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	logs := &lockedBuffer{}
	gw, err := New(cfg, func(string) string { return "k" }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	assertRecordedThenClosed(t, logs, arrived, release, func() {
		_, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "showPetById", Arguments: map[string]any{"petId": "7"}})
	})
}
