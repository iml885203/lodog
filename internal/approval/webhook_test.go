package approval_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/policy"

	"github.com/iml885203/lodog/internal/approval"
)

var sample = approval.Notice{
	ID: "apr_0123456789abcdef", OnBehalfOf: "user:alice", Actor: "agent:bench", What: "Reject a withdrawal.",
	Method: "POST", Target: "/api/payment/transaction/withdraw/reject", Risk: "money",
	Reason: "Customer asked support.", Arguments: map[string]any{"requestBody": map[string]any{"refNo": "W-1"}},
	ExpiresAt: time.Date(2026, 10, 16, 14, 30, 0, 0, time.UTC),
}

const hookSecret = "T0SENTINEL/B0SENTINEL/hookSecret8f3a"

// hook is an incoming webhook that keeps what it was sent.
type hook struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	status int
	echo   bool
}

func newHook(t *testing.T) *hook {
	t.Helper()
	h := &hook{status: http.StatusOK}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.bodies = append(h.bodies, string(raw))
		w.WriteHeader(h.status)
		if h.echo {
			_, _ = io.WriteString(w, "no_service at "+r.URL.Path+" "+r.Host) //nolint:gosec // G705: a test server echoing plain text to a Go client
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) url() string { return h.srv.URL + "/services/" + hookSecret }

func (h *hook) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

var linkPattern = regexp.MustCompile(`(https?://[^/\s"]+)/approve/(apr_[0-9a-f]+)/([A-Za-z0-9_-]+)`)

// page is a Webhook behind a real HTTP server, with the decisions it passed on.
type page struct {
	hook    *hook
	web     *approval.Webhook
	srv     *httptest.Server
	logs    *bytes.Buffer
	now     time.Time
	mu      sync.Mutex
	decided []approval.Verdict
	handler http.Handler
}

func newPage(t *testing.T, log *slog.Logger) *page {
	t.Helper()
	p := &page{hook: newHook(t), logs: &bytes.Buffer{}, now: time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		h := p.handler
		p.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	if log == nil {
		log = slog.New(slog.NewTextHandler(p.logs, nil))
	}
	web, err := approval.NewWebhook(approval.WebhookConfig{
		URL: p.hook.url(), BaseURL: p.srv.URL, Listen: "127.0.0.1:0", Log: log,
		Now: func() time.Time { p.mu.Lock(); defer p.mu.Unlock(); return p.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	p.web = web
	p.srvHandler(web.Handler(func(_ context.Context, d approval.Verdict) approval.Answer {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.decided = append(p.decided, d)
		return approval.Answer{Accepted: true}
	}))
	return p
}

func (p *page) srvHandler(h http.Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = h
}

func (p *page) decisions() []approval.Verdict {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]approval.Verdict(nil), p.decided...)
}

// notify sends the sample request and returns its link parts.
func (p *page) notify(t *testing.T) (path, token string) {
	t.Helper()
	n := sample
	n.ExpiresAt = p.now.Add(30 * time.Minute)
	if _, err := p.web.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	m := linkPattern.FindStringSubmatch(p.hook.sent()[len(p.hook.sent())-1])
	if m == nil || m[1] != p.srv.URL {
		t.Fatalf("no link to %s in the message: %v", p.srv.URL, p.hook.sent())
	}
	return "/approve/" + m[2] + "/" + m[3], m[3]
}

func get(t *testing.T, base, path string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func post(t *testing.T, base, path string, form url.Values, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var nonceField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (p *page) nonce(t *testing.T, path string) string {
	t.Helper()
	code, body, _ := get(t, p.srv.URL, path)
	m := nonceField.FindStringSubmatch(body)
	if code != http.StatusOK || m == nil {
		t.Fatalf("page %d, no form:\n%s", code, body)
	}
	return m[1]
}

func (p *page) approve(t *testing.T, path, name string) (int, string) {
	t.Helper()
	return post(t, p.srv.URL, path, url.Values{"csrf": {p.nonce(t, path)}, "name": {name}, "verdict": {"approve"}}, nil)
}

func TestTheMessageCarriesTheFactsAndAOneTimeLinkWithAStrongToken(t *testing.T) {
	p := newPage(t, nil)
	_, token := p.notify(t)
	msg := p.hook.sent()[0]
	for _, want := range []string{"user:alice", "Reject a withdrawal.", "POST /api/payment/transaction/withdraw/reject", "money", "Customer asked support.", "開啟核准頁"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message lacks %q:\n%s", want, msg)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 16 {
		t.Fatalf("token %q decodes to %d bytes (%v), want at least 16 (128 bits)", token, len(raw), err)
	}
	_, token2 := p.notify(t)
	if token == token2 {
		t.Fatal("two requests got the same token")
	}
	if strings.Contains(p.logs.String(), token) {
		t.Fatalf("the token reached the log:\n%s", p.logs.String())
	}
}

func TestThePageShowsTheRequestAndSaysTheNameIsNotVerified(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	code, body, hdr := get(t, p.srv.URL, path)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Reject a withdrawal.", "user:alice", "W-1", "money", "Customer asked support.", "核准", "拒絕", "gateway 不會驗證", `method="post"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if hdr.Get("Cache-Control") != "no-store" || hdr.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("headers = %v", hdr)
	}
	if len(p.decisions()) != 0 {
		t.Fatal("opening the page decided something")
	}
	if code, _, _ := get(t, p.srv.URL, path+"?verdict=approve&name=x&csrf=y"); code != http.StatusOK || len(p.decisions()) != 0 {
		t.Fatalf("a GET with a verdict decided something (%d, %v)", code, p.decisions())
	}
}

func TestAnAgentsWordsAreEscapedOnThePage(t *testing.T) {
	p := newPage(t, nil)
	n := sample
	n.ExpiresAt = p.now.Add(time.Hour)
	n.Reason = `<script>alert(1)</script>`
	n.What = `<img src=x onerror=alert(1)>`
	if _, err := p.web.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	m := linkPattern.FindStringSubmatch(p.hook.sent()[0])
	_, body, _ := get(t, p.srv.URL, "/approve/"+m[2]+"/"+m[3])
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img src=x") {
		t.Fatalf("agent text became live markup:\n%s", body)
	}
}

func TestADecisionNeedsTheNonceTheFormCarries(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	good := p.nonce(t, path)
	for name, form := range map[string]url.Values{
		"no nonce":    {"name": {"Alice"}, "verdict": {"approve"}},
		"wrong nonce": {"csrf": {"AAAAAAAAAAAAAAAAAAAAAA"}, "name": {"Alice"}, "verdict": {"approve"}},
	} {
		if code, _ := post(t, p.srv.URL, path, form, nil); code == http.StatusOK || len(p.decisions()) != 0 {
			t.Errorf("%s: answered %d and decided %v", name, code, p.decisions())
		}
	}
	form := url.Values{"csrf": {good}, "name": {"Alice"}, "verdict": {"approve"}}
	for name, hdr := range map[string]map[string]string{
		"another origin":   {"Origin": "https://evil.example"},
		"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
	} {
		if code, _ := post(t, p.srv.URL, path, form, hdr); code != http.StatusForbidden || len(p.decisions()) != 0 {
			t.Errorf("%s: answered %d and decided %v", name, code, p.decisions())
		}
	}
	// None of the refused attempts spent the link.
	if code, _ := post(t, p.srv.URL, path, form, map[string]string{"Origin": p.srv.URL, "Sec-Fetch-Site": "same-origin"}); code != http.StatusOK || len(p.decisions()) != 1 {
		t.Fatalf("the real press answered %d, decisions %v", code, p.decisions())
	}
}

func TestAnApprovalLinkWorksOnceAndRecordsTheTypedName(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	if code, body := p.approve(t, path, "  Alice   Lin "); code != http.StatusOK || !strings.Contains(body, "已核准") {
		t.Fatalf("%d %s", code, body)
	}
	want := approval.Verdict{ID: sample.ID, Approve: true, By: "web:Alice Lin"}
	if got := p.decisions(); len(got) != 1 || got[0] != want {
		t.Fatalf("decisions = %+v, want %+v", got, want)
	}
	form := url.Values{"csrf": {"x"}, "name": {"Mallory"}, "verdict": {"deny"}}
	if code, _ := post(t, p.srv.URL, path, form, nil); code != http.StatusNotFound {
		t.Errorf("a used link answered a POST with %d", code)
	}
	if code, _, _ := get(t, p.srv.URL, path); code != http.StatusNotFound {
		t.Errorf("a used link still shows the page: %d", code)
	}
	if len(p.decisions()) != 1 {
		t.Fatalf("decisions = %+v", p.decisions())
	}
}

func TestADenyPressIsDelivered(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	code, body := post(t, p.srv.URL, path, url.Values{"csrf": {p.nonce(t, path)}, "name": {"Bo"}, "verdict": {"deny"}}, nil)
	if code != http.StatusOK || !strings.Contains(body, "已拒絕") || p.decisions()[0].Approve {
		t.Fatalf("%d %s %+v", code, body, p.decisions())
	}
}

func TestEveryWayALinkCanBeWrongLooksTheSame(t *testing.T) {
	p := newPage(t, nil)
	path, token := p.notify(t)
	other, _ := p.notify(t) // a second request, with its own token
	otherID := strings.Split(other, "/")[2]
	var answers []string
	for name, bad := range map[string]string{
		"wrong token":     strings.Replace(path, token, strings.Repeat("A", len(token)), 1),
		"truncated token": path[:len(path)-1],
		"another's token": "/approve/" + otherID + "/" + token,
		"unknown id":      "/approve/apr_00000000000000ff/" + token,
		"empty-ish token": path + "x",
	} {
		code, body, _ := get(t, p.srv.URL, bad)
		if code != http.StatusNotFound {
			t.Errorf("%s: GET answered %d", name, code)
		}
		answers = append(answers, body)
		form := url.Values{"csrf": {"x"}, "name": {"Mallory"}, "verdict": {"approve"}}
		if code, _ := post(t, p.srv.URL, bad, form, nil); code == http.StatusOK {
			t.Errorf("%s: POST answered 200", name)
		}
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			t.Errorf("a wrong link says more than another wrong link:\n%s\n%s", a, answers[0])
		}
	}
	if len(p.decisions()) != 0 {
		t.Fatalf("decided %+v", p.decisions())
	}
}

func TestAnExpiredLinkIsDead(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	nonce := p.nonce(t, path)
	p.mu.Lock()
	p.now = p.now.Add(31 * time.Minute)
	p.mu.Unlock()
	if code, _ := post(t, p.srv.URL, path, url.Values{"csrf": {nonce}, "name": {"Alice"}, "verdict": {"approve"}}, nil); code != http.StatusNotFound || len(p.decisions()) != 0 {
		t.Fatalf("an expired link answered %d and decided %v", code, p.decisions())
	}
	if code, _, _ := get(t, p.srv.URL, path); code != http.StatusNotFound {
		t.Fatalf("an expired link still shows the page: %d", code)
	}
}

func TestABadNameIsRefusedAndKeepsTheLink(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	for _, name := range []string{"", "   ", strings.Repeat("a", 65), "bad\x00name", "tab\x07"} {
		code, _ := post(t, p.srv.URL, path, url.Values{"csrf": {p.nonce(t, path)}, "name": {name}, "verdict": {"approve"}}, nil)
		if code != http.StatusBadRequest {
			t.Errorf("name %q answered %d, want 400", name, code)
		}
	}
	if code, _ := post(t, p.srv.URL, path, url.Values{"csrf": {p.nonce(t, path)}, "name": {"Al"}, "verdict": {"maybe"}}, nil); code != http.StatusBadRequest {
		t.Errorf("a verdict that is neither answered %d", code)
	}
	if code, _ := p.approve(t, path, "Alice"); code != http.StatusOK {
		t.Fatalf("the link did not survive refused attempts: %d", code)
	}
}

func TestTwoPressesAtOnceDecideOnce(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	nonce := p.nonce(t, path)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post(t, p.srv.URL, path, url.Values{"csrf": {nonce}, "name": {"Alice"}, "verdict": {"approve"}}, nil)
		}()
	}
	wg.Wait()
	if n := len(p.decisions()); n != 1 {
		t.Fatalf("%d decisions from one link", n)
	}
}

func TestAnUpdateKillsTheLinkAndPostsAFollowUp(t *testing.T) {
	p := newPage(t, nil)
	path, _ := p.notify(t)
	if err := p.web.Update(context.Background(), sample.ID, sample, approval.NoticeExpired, ""); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := get(t, p.srv.URL, path); code != http.StatusNotFound {
		t.Fatalf("the link survived: %d", code)
	}
	if sent := p.hook.sent(); len(sent) != 2 || !strings.Contains(sent[1], "逾時") {
		t.Fatalf("follow-up = %v", sent)
	}
}

func TestAFailedPostLeavesNoLiveLinkAndNoWebhookURLInTheError(t *testing.T) {
	p := newPage(t, nil)
	p.hook.status, p.hook.echo = http.StatusNotFound, true
	n := sample
	n.ExpiresAt = p.now.Add(time.Hour)
	_, err := p.web.Notify(context.Background(), n)
	if err == nil {
		t.Fatal("a refused webhook was not reported")
	}
	// The link the failed message would have carried must not work.
	live := p.web.LiveLinks()
	if live != 0 {
		t.Fatalf("%d links survived a message nobody received", live)
	}
	p.hook.srv.Close()
	_, err2 := p.web.Notify(context.Background(), n)
	for _, e := range []error{err, err2} {
		if e == nil || strings.Contains(e.Error(), hookSecret) || strings.Contains(e.Error(), "hookSecret8f3a") {
			t.Errorf("error is missing or shows the webhook URL: %v", e)
		}
	}
}

func TestConfigIsChecked(t *testing.T) {
	ok := approval.WebhookConfig{URL: "https://hooks.slack.com/services/x", BaseURL: "http://localhost:7403", Listen: "127.0.0.1:7403"}
	for name, mod := range map[string]func(*approval.WebhookConfig){
		"no url":      func(c *approval.WebhookConfig) { c.URL = "" },
		"not a url":   func(c *approval.WebhookConfig) { c.URL = "hooks" },
		"no base url": func(c *approval.WebhookConfig) { c.BaseURL = "" },
		"no listen":   func(c *approval.WebhookConfig) { c.Listen = "" },
	} {
		c := ok
		mod(&c)
		if _, err := approval.NewWebhook(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := approval.NewWebhook(ok); err != nil {
		t.Fatal(err)
	}
}

func TestListenServesThePageUntilCancelled(t *testing.T) {
	p := newPage(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.web.Listen(ctx, nil) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not stop")
	}
}

func TestThePageMasksCredentialFieldsAndClipsABigInput(t *testing.T) {
	p := newPage(t, nil)
	big := strings.Repeat("QUJD", 2500)
	n := sample
	n.ExpiresAt = p.now.Add(time.Hour)
	n.Arguments = map[string]any{"requestBody": map[string]any{"refNo": "W-1", "password": "S3NTINEL-pw", "attachment": big}}
	if _, err := p.web.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	m := linkPattern.FindStringSubmatch(p.hook.sent()[0])
	_, body, _ := get(t, p.srv.URL, "/approve/"+m[2]+"/"+m[3])
	if strings.Contains(body, "S3NTINEL-pw") || strings.Contains(body, big[:300]) || len(body) > 8000 {
		t.Fatalf("the page (%d bytes) shows a credential or the whole blob:\n%s", len(body), body)
	}
	if !strings.Contains(body, "W-1") || !strings.Contains(body, "10000 字元") {
		t.Fatalf("the page lost the ordinary field or the blob's size:\n%s", body)
	}
}

func TestThePageMasksACredentialInTheTarget(t *testing.T) {
	p := newPage(t, nil)
	store := approval.NewStore()
	req, err := store.Hold("t", "GET", "/api/x?token=S3NT-path&page=2", "t", policy.Decision{Action: policy.Approval, Risk: "money"}, nil, casecfg.IdentityConfig{Actor: "agent:t"})
	if err != nil {
		t.Fatal(err)
	}
	n := approval.NoticeFor(req, "x", "")
	n.ExpiresAt = p.now.Add(time.Hour)
	if _, err := p.web.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	sent := p.hook.sent()[0]
	m := linkPattern.FindStringSubmatch(sent)
	_, body, _ := get(t, p.srv.URL, "/approve/"+m[2]+"/"+m[3])
	for place, got := range map[string]string{"message": sent, "page": body} {
		if strings.Contains(got, "S3NT-path") || !strings.Contains(got, "page=2") {
			t.Errorf("%s:\n%s", place, got)
		}
	}
}
