package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

const testOpenAPI = `{
  "openapi": "3.0.1",
  "info": {"title": "t", "version": "1"},
  "paths": {
    "/api/customer/{customerId}": {
      "get": {"responses": {"200": {"description": "ok"}}},
      "delete": {"responses": {"204": {"description": "gone"}}}
    },
    "/api/customer/searchPlayerProfile": {"get": {"responses": {"200": {"description": "ok"}}}},
    "/api/customer/{customerId}/resetPassword": {"get": {"responses": {"200": {"description": "ok"}}}},
    "/api/customer/{customerId}/edit": {"post": {"responses": {"200": {"description": "ok",
      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Envelope"}}}}}}},
    "/api/operator/login": {"post": {"responses": {"200": {"description": "ok"}}}}
  },
  "components": {"schemas": {"Envelope": {"type": "object", "properties": {
    "data": {"type": "object", "properties": {"id": {"type": "integer"}, "updated": {"type": "string", "format": "date-time"}}},
    "errorCode": {"type": "integer"},
    "message": {"type": "string", "nullable": true}
  }}}}
}`

const testPolicy = `
risks: {read: allow, write: approval, credential: hide}
default: write
rules:
  - {match: "GET /api/customer/*/resetPassword", risk: credential}
  - {match: "GET /**", risk: read}
`

func testGuard(t *testing.T) *Guard {
	t.Helper()
	doc, err := spec.Parse([]byte(testOpenAPI))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse([]byte(testPolicy))
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard(doc, pol, []string{"POST /api/operator/login"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// system is a legacy system that remembers every call that reached it.
type system struct {
	mu   sync.Mutex
	seen []string
}

func (s *system) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "secret-session-value", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	_, _ = io.WriteString(w, `{"name":"bob"}`)
}

func (s *system) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func startSystem(t *testing.T) (*system, *url.URL) {
	t.Helper()
	sys := &system{}
	srv := httptest.NewServer(http.HandlerFunc(sys.handler))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return sys, target
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

type answer struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, method, target, body string, headers map[string]string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
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
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, string(b)}
}

func TestAReadOnlyProxySendsOnlyWhatAnExplicitRuleAllows(t *testing.T) {
	sys, target := startSystem(t)
	sink := &memSink{}
	px := serve(t, New(target, nil, sink, slog.New(slog.DiscardHandler)).ReadOnly(testGuard(t)))

	calls := []struct {
		method, path string
		sent         bool
	}{
		{"GET", "/api/customer/7", true},
		{"GET", "/api/customer/searchPlayerProfile?username=bob", true},
		{"POST", "/api/operator/login", true},
		{"GET", "/api/customer/7/resetPassword", false},
		{"GET", "/API/Customer/7/RESETPASSWORD", false},
		{"POST", "/api/customer/7/edit", false},
		{"DELETE", "/api/customer/7", false},
		{"GET", "/api/not/in/the/document", false},
	}
	var want []string
	for _, c := range calls {
		do(t, c.method, px+c.path, "", nil)
		if c.sent {
			want = append(want, c.method+" "+strings.Split(c.path, "?")[0])
		}
	}
	if got := sys.calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("the system saw %v, want only %v", got, want)
	}
	done := sink.finished()
	if len(done) != len(calls) {
		t.Fatalf("recorded %d calls, want %d", len(done), len(calls))
	}
	for i, c := range calls {
		if done[i].DryRun == c.sent || (done[i].Held != "") == c.sent {
			t.Errorf("%s %s: record = %+v", c.method, c.path, done[i])
		}
	}
	for _, line := range sink.snapshot() {
		if line.Phase == record.PhaseStarted && line.Path == "/api/customer/7/edit" && !line.DryRun {
			t.Errorf("the started line of a held call does not say it was held: %+v", line)
		}
	}
}

func TestAHeldWriteKeepsItsRequestAndGetsASuccessShapedLikeTheDocument(t *testing.T) {
	sys, target := startSystem(t)
	sink := &memSink{}
	px := serve(t, New(target, nil, sink, slog.New(slog.DiscardHandler)).ReadOnly(testGuard(t)))

	got := do(t, http.MethodPost, px+"/api/customer/7/edit", `{"remark":"CALLBACK-0420"}`, nil)
	if len(sys.calls()) != 0 {
		t.Fatalf("the system saw %v", sys.calls())
	}
	var body struct {
		Data struct {
			ID      *int    `json:"id"`
			Updated *string `json:"updated"`
		} `json:"data"`
		ErrorCode *int `json:"errorCode"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("answer %q: %v", got.body, err)
	}
	if got.status != http.StatusOK || body.Data.ID == nil || body.ErrorCode == nil || body.Data.Updated == nil ||
		got.header.Get(HeaderDryRun) == "" || got.header.Get(HeaderMode) != record.ModeReadOnly {
		t.Fatalf("answer = %+v", got)
	}
	rec := sink.finished()[0]
	if rec.Body != `{"remark":"CALLBACK-0420"}` || rec.Status != http.StatusOK || !strings.Contains(rec.Held, "write") {
		t.Fatalf("record = %+v", rec)
	}

	gone := do(t, http.MethodDelete, px+"/api/customer/7", "", nil)
	unknown := do(t, http.MethodPost, px+"/token", "client_secret=s3cret", nil)
	if gone.status != http.StatusNoContent || unknown.status != http.StatusForbidden {
		t.Fatalf("delete = %d, unknown = %d", gone.status, unknown.status)
	}
	if b := sink.finished()[2].Body; b != "" {
		t.Fatalf("a call outside the document recorded its body %q", b)
	}
}

func TestTheProxyTellsItsModeWithoutForwardingOrRecording(t *testing.T) {
	sys, target := startSystem(t)
	sink := &memSink{}
	full := serve(t, New(target, nil, sink, slog.New(slog.DiscardHandler)))
	ro := serve(t, New(target, nil, sink, slog.New(slog.DiscardHandler)).ReadOnly(testGuard(t)))
	for want, base := range map[string]string{record.ModeFull: full, record.ModeReadOnly: ro} {
		got := do(t, http.MethodGet, base+ModePath, "", nil)
		if got.body != `{"mode":"`+want+`"}`+"\n" || got.header.Get(HeaderMode) != want {
			t.Errorf("%s: answer = %+v", want, got)
		}
	}
	if len(sys.calls()) != 0 || len(sink.snapshot()) != 0 {
		t.Fatalf("the mode question reached the system (%v) or a record (%v)", sys.calls(), sink.snapshot())
	}
}

// recordOnce records one answer of the system to a file and returns it.
func recordOnce(t *testing.T) string {
	t.Helper()
	_, target := startSystem(t)
	file := filepath.Join(t.TempDir(), "recording.jsonl")
	recorder, err := NewRecorder(file)
	if err != nil {
		t.Fatal(err)
	}
	recording := serve(t, New(target, nil, &memSink{}, slog.New(slog.DiscardHandler)).ReadOnly(testGuard(t)).Recording(recorder))
	do(t, http.MethodGet, recording+"/api/customer/7?from=2026-09-16T00:00:00Z&api_key=test-key", "",
		map[string]string{HeaderRedactQuery: "api_key"})
	do(t, http.MethodPost, recording+"/api/operator/login", `{"LoginName":"op"}`, nil)
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestARecordingKeepsNoCredential(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(recordOnce(t)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `{\"name\":\"bob\"}`) != 1 || strings.Contains(string(raw), "secret-session-value") || strings.Contains(string(raw), "test-key") {
		t.Fatalf("recording = %s", raw)
	}
}

func TestARecordedAnswerIsReplayedWithoutTheSystem(t *testing.T) {
	rec, err := LoadRecording(recordOnce(t))
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	replay := serve(t, New(&url.URL{Scheme: "http", Host: "replay.invalid"}, nil, sink, slog.New(slog.DiscardHandler)).Replaying(testGuard(t), rec))

	got := do(t, http.MethodGet, replay+"/api/customer/7?from=2026-10-16T00:00:00Z&api_key=OTHER-KEY-99", "",
		map[string]string{HeaderRedactQuery: "api_key"})
	if got.status != http.StatusOK || got.body != `{"name":"bob"}` || !strings.Contains(got.header.Get("Set-Cookie"), "session="+replayCookie) {
		t.Fatalf("replayed answer = %+v", got)
	}
	missing := do(t, http.MethodGet, replay+"/api/customer/8", "", nil)
	held := do(t, http.MethodPost, replay+"/api/customer/7/edit", "{}", nil)
	if missing.status != http.StatusBadGateway || held.status != http.StatusOK || held.header.Get(HeaderDryRun) == "" {
		t.Fatalf("missing = %+v, held = %+v", missing, held)
	}
	done := sink.finished()
	if !done[0].Replayed || done[1].Replayed || done[1].Error != errNotRecorded || !done[2].DryRun {
		t.Fatalf("records = %+v", done)
	}
}

func TestAReplayNeedsAGuardAndNoSystem(t *testing.T) {
	g := testGuard(t)
	file := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(file, []byte(`{"key":"GET /a?","status":200}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	for name, opts := range map[string]Options{
		"no guard":      {ReplayFile: file},
		"with upstream": {ReplayFile: file, Guard: g, Upstream: "http://localhost:1"},
		"with record":   {ReplayFile: file, Guard: g, RecordFile: file + ".new"},
		"record, full":  {Upstream: "http://localhost:1", RecordFile: file + ".new"},
	} {
		if _, _, err := build(opts, &memSink{}, log); !errors.Is(err, ErrBadOptions) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	px, closeAll, err := build(Options{ReplayFile: file, Guard: g}, &memSink{}, log)
	if err != nil || px.Mode() != record.ModeReplay || closeAll() != nil {
		t.Fatalf("replay: mode = %v, err = %v", px, err)
	}
}

func TestAMadeUpSuccessFollowsTheSchema(t *testing.T) {
	schema := map[string]any{"allOf": []any{
		map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "enum": []any{"Done", "Pending"}}}},
		map[string]any{"type": "object", "properties": map[string]any{
			"items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"ok":    map[string]any{"type": []any{"null", "boolean"}},
			"limit": map[string]any{"type": "integer", "default": 20},
			"kind":  map[string]any{"oneOf": []any{map[string]any{"type": "string", "format": "uuid"}}},
		}},
	}}
	want := map[string]any{"status": "Done", "items": []any{}, "ok": false, "limit": 20, "kind": "00000000-0000-0000-0000-000000000000"}
	if got := example(schema, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("example = %#v", got)
	}
}
