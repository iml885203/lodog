package approval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/iml885203/lodog/internal/redact"
)

// webPrefix marks an identity a person typed into the approval page. It is
// a name, not a verified identity.
const webPrefix = "web:"

// maxNonces bounds the page views one request remembers.
const maxNonces = 8

// WebhookConfig is what the link-based notifier needs. URL is a secret.
type WebhookConfig struct {
	URL     string // Slack incoming webhook
	BaseURL string // how approvers reach the page, no trailing slash
	Listen  string // where the page is served
	HTTP    *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

// Webhook implements Notifier: it posts to an
// incoming webhook a summary and a one-time link to a page the gateway
// serves. Whoever can open the link can decide, so the link is the
// credential: it exists only in the webhook message, is bound to one
// request, and dies when used or expired.
type Webhook struct {
	cfg  WebhookConfig
	mask redact.Names

	mu      sync.Mutex
	pending map[string]*link
}

var _ Notifier = (*Webhook)(nil)

type link struct {
	notice    Notice
	tokenHash [sha256.Size]byte
	nonces    []string
}

// NewWebhook checks cfg and returns the notifier.
func NewWebhook(cfg WebhookConfig) (*Webhook, error) {
	u, err := url.Parse(cfg.URL)
	if cfg.URL == "" || err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("slack webhook url is empty or not an http(s) address")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("approval page base url must be an http(s) address")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Listen == "" {
		return nil, errors.New("approval page listen address is required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	secrets := []string{cfg.URL}
	if len(u.Path) > 8 { // the path is the secret part; "/" would mask everything
		secrets = append(secrets, u.Path)
	}
	return &Webhook{
		cfg:     cfg,
		mask:    redact.Names{Values: secrets},
		pending: map[string]*link{},
	}, nil
}

// LiveLinks is how many one-time links can still decide a request.
func (w *Webhook) LiveLinks() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

func (w *Webhook) fail(format string, args ...any) error {
	return errors.New(w.mask.Text(fmt.Sprintf(format, args...)))
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Notify mints the request's one-time token and posts the message carrying
// the link. The token goes nowhere else.
func (w *Webhook) Notify(ctx context.Context, n Notice) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", w.fail("approval token: %v", err)
	}
	entry := &link{notice: n, tokenHash: sha256.Sum256([]byte(token))}
	w.mu.Lock()
	w.pending[n.ID] = entry
	w.mu.Unlock()
	text, blocks := factBlocks(n)
	href := w.cfg.BaseURL + "/approve/" + n.ID + "/" + token
	blocks = append(blocks,
		section("agent 想做的事要先經人核准。打開連結看完整參數，再決定："),
		map[string]any{"type": "actions", "elements": []map[string]any{
			{"type": "button", "text": map[string]any{"type": "plain_text", "text": "開啟核准頁"}, "url": href, "style": "primary"},
		}},
		map[string]any{"type": "context", "elements": []map[string]any{
			{"type": "mrkdwn", "text": "連結只能用一次，到 " + n.ExpiresAt.Format("15:04") + " 前有效。拿到連結的人就能核准，請不要轉貼。"},
		}})
	if err := w.post(ctx, map[string]any{"text": text + "\n" + href, "blocks": blocks}); err != nil {
		w.mu.Lock()
		delete(w.pending, n.ID)
		w.mu.Unlock()
		return "", err
	}
	return n.ID, nil
}

// Update posts a short follow-up: an incoming webhook cannot edit the first
// message. The request's link stops working in every state.
func (w *Webhook) Update(ctx context.Context, ref string, n Notice, state NoticeState, by string) error {
	w.mu.Lock()
	delete(w.pending, ref)
	w.mu.Unlock()
	line := map[NoticeState]string{
		NoticeApproved: ":white_check_mark: 已核准（頁面上自填的名稱：" + escape(strings.TrimPrefix(by, webPrefix)) + "）。等 agent 重送。",
		NoticeDenied:   ":no_entry: 已拒絕（頁面上自填的名稱：" + escape(strings.TrimPrefix(by, webPrefix)) + "）。",
		NoticeUsed:     ":ballot_box_with_check: 已用掉：核准過的呼叫已轉發一次。",
		NoticeExpired:  ":hourglass: 逾時沒人處理，連結已失效。",
	}[state]
	return w.post(ctx, map[string]any{"text": "`" + n.ID + "` " + line})
}

func (w *Webhook) post(ctx context.Context, body map[string]any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return w.fail("slack webhook: encode: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(encoded))
	if err != nil {
		return w.fail("slack webhook: %v", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := w.cfg.HTTP.Do(req)
	if err != nil {
		return w.fail("slack webhook: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return w.fail("slack webhook answered %d: %s", resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	return nil
}

// Listen serves the approval page until ctx ends.
func (w *Webhook) Listen(ctx context.Context, handle func(context.Context, Verdict) Answer) error {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", w.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", w.cfg.Listen, err)
	}
	srv := &http.Server{Handler: w.Handler(handle), ReadHeaderTimeout: 10 * time.Second}
	w.cfg.Log.Info("approval page serving", "addr", lis.Addr().String())
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()
	if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the approval page: GET shows a request, POST decides it. Every
// way a link can fail to work answers the same, so a guess learns nothing.
func (w *Webhook) Handler(handle func(context.Context, Verdict) Answer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /approve/{id}/{token}", func(rw http.ResponseWriter, r *http.Request) {
		secure(rw)
		view, nonce, ok := w.open(r.PathValue("id"), r.PathValue("token"), "", false)
		if !ok {
			gone(rw)
			return
		}
		render200(rw, pageTmpl, pageData{Notice: view, Nonce: nonce, Args: prettyArgs(view.Arguments)})
	})
	mux.HandleFunc("POST /approve/{id}/{token}", func(rw http.ResponseWriter, r *http.Request) {
		secure(rw)
		if !w.sameOrigin(r) {
			http.Error(rw, "cross-site request refused", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(rw, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(rw, "bad form", http.StatusBadRequest)
			return
		}
		name, ok := cleanName(r.PostFormValue("name"))
		verdict := r.PostFormValue("verdict")
		if !ok || (verdict != "approve" && verdict != "deny") {
			http.Error(rw, "請填你的名稱（1 到 64 個字），並選核准或拒絕。", http.StatusBadRequest)
			return
		}
		// The link is spent here, before the verdict is delivered, so two
		// presses at once cannot both count.
		view, _, ok := w.open(r.PathValue("id"), r.PathValue("token"), r.PostFormValue("csrf"), true)
		if !ok {
			gone(rw)
			return
		}
		answer := handle(r.Context(), Verdict{ID: view.ID, Approve: verdict == "approve", By: webPrefix + name})
		render200(rw, doneTmpl, doneData{Accepted: answer.Accepted, Message: answer.Message, Approved: verdict == "approve", Name: name})
	})
	return mux
}

// open finds the request the link names and checks its token in constant
// time. With a nonce it is the deciding call: the nonce must be one this
// request issued, and both are consumed. A page view (deciding false) issues
// a nonce for the form and consumes nothing.
func (w *Webhook) open(id, token, nonce string, deciding bool) (Notice, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.pending[id]
	if !ok {
		return Notice{}, "", false
	}
	given := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(given[:], entry.tokenHash[:]) != 1 {
		return Notice{}, "", false
	}
	if !w.cfg.Now().Before(entry.notice.ExpiresAt) {
		delete(w.pending, id)
		return Notice{}, "", false
	}
	if !deciding {
		fresh, err := randomToken(16)
		if err != nil {
			return Notice{}, "", false
		}
		entry.nonces = append(entry.nonces, fresh)
		if len(entry.nonces) > maxNonces {
			entry.nonces = entry.nonces[1:]
		}
		return entry.notice, fresh, true
	}
	matched := false
	for _, n := range entry.nonces {
		if subtle.ConstantTimeCompare([]byte(n), []byte(nonce)) == 1 {
			matched = true
		}
	}
	if !matched {
		return Notice{}, "", false
	}
	delete(w.pending, id)
	return entry.notice, "", true
}

// sameOrigin refuses a deciding request another site caused the browser to
// send. A request that names no origin passes: it still needs the token and
// a nonce only the page itself carries.
func (w *Webhook) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	base, err := url.Parse(w.cfg.BaseURL)
	if err != nil {
		return false
	}
	return origin == base.Scheme+"://"+base.Host
}

func cleanName(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || len([]rune(s)) > 64 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

func secure(rw http.ResponseWriter) {
	h := rw.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
}

func gone(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(rw, "<!doctype html><meta charset=utf-8><title>lodog</title><p>這個連結無效、已經用過或已逾時。需要的話請 agent 重新請求核准。")
}

func render200(rw http.ResponseWriter, t *template.Template, data any) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = t.Execute(rw, data)
}

func prettyArgs(args map[string]any) string { return redact.PreviewJSON(args, true) }

type pageData struct {
	Notice
	Nonce string
	Args  string
}

type doneData struct {
	Accepted, Approved bool
	Message, Name      string
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-Hant"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>lodog 核准</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem}
dt{font-weight:600;margin-top:.8rem}dd{margin:0}pre{background:#f4f4f4;padding:.8rem;overflow:auto;white-space:pre-wrap}
.warn{background:#fff6d6;padding:.6rem .8rem;border-radius:4px}button{font:inherit;padding:.5rem 1.2rem;margin-right:.5rem}
@media (prefers-color-scheme:dark){body{background:#161616;color:#eee}pre{background:#262626}.warn{background:#4a3f12}}</style>
<h1>需要核准</h1>
<p><b>{{.OnBehalfOf}}</b>（{{.Actor}}）的 agent 想要：<b>{{.What}}</b></p>
<dl>
<dt>對象</dt><dd><code>{{.Method}} {{.Target}}</code></dd>
<dt>風險</dt><dd>{{.Risk}}</dd>
{{if .Reason}}<dt>agent 自述的理由（不是 gateway 查證過的）</dt><dd>{{.Reason}}</dd>{{end}}
<dt>完整參數</dt><dd><pre>{{.Args}}</pre></dd>
</dl>
<form method="post">
<input type="hidden" name="csrf" value="{{.Nonce}}">
<p><label>你的名稱 <input name="name" required maxlength="64" autocomplete="off"></label></p>
<p class="warn">名稱由你自己填，gateway 不會驗證；能打開這個連結的人就能核准。核准後 agent 重送同一個呼叫才會執行，且只執行一次。</p>
<button name="verdict" value="approve">核准</button><button name="verdict" value="deny">拒絕</button>
</form></html>`))

var doneTmpl = template.Must(template.New("done").Parse(`<!doctype html>
<html lang="zh-Hant"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>lodog 核准</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem}@media (prefers-color-scheme:dark){body{background:#161616;color:#eee}}</style>
{{if .Accepted}}<h1>{{if .Approved}}已核准{{else}}已拒絕{{end}}</h1><p>記錄的名稱：{{.Name}}（自填，未驗證）。連結已失效。</p>
{{else}}<h1>沒有生效</h1><p>{{.Message}}</p>{{end}}</html>`))
