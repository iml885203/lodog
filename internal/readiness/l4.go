package readiness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

// Status values of a published agent standard.
const (
	StandardFound     = "found"
	StandardMissing   = "missing"
	StandardUnchecked = "unchecked"
)

var (
	dryRunParam = regexp.MustCompile(`(?i)^(dry_?run|validate_?only|preview|simulate)$`)
	idempotency = regexp.MustCompile(`(?i)^(idempotency[-_]?key|(client[-_]?)?request[-_]?id)$`)
)

// L4 is what only a change to the legacy code can improve (docs/levels.md):
// the part of the static score L1 to L3 cannot reach, plus the practices
// that are not in the static score. lodog recommends; the owner decides.
type L4 struct {
	// Gain is the static points the scored items add up to.
	Gain      float64    `json:"gain"`
	Items     []L4Item   `json:"items"`
	Standards []Standard `json:"standards"`
}

// L4Item is one change to the legacy system.
type L4Item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Check is the static check this item earns points in; empty when the
	// item is not in the static score.
	Check string `json:"check,omitempty"`
	// Measured says where the item shows up in the score.
	Measured string `json:"measured"`
	// Gain is the static points the change adds: the check's maximum minus
	// what it scores now. 0 for an item outside the static score.
	Gain float64 `json:"gain"`
	// Handled is true for an item in the static score whose check is already
	// full (an overlay, a policy or the gateway took care of it): it earns
	// nothing more, but the legacy code should still change.
	Handled  bool     `json:"handled,omitempty"`
	Detail   string   `json:"detail"`
	Examples []string `json:"examples,omitempty"`
	// Instruction is written to be pasted to a coding agent as it stands.
	Instruction string `json:"instruction"`
	// Endpoints are the endpoints the item asks the legacy API to add.
	Endpoints []MissingEndpoint `json:"endpoints,omitempty"`
}

// Counted is true for an item that still adds static points; the rest are
// listed apart, because the legacy code should change all the same.
func (it L4Item) Counted() bool {
	return it.Check != "" && !it.Handled
}

// CountedItems are the items that still add static points, in list order.
func (l *L4) CountedItems() []L4Item {
	return l.items(true)
}

// UncountedItems are the items outside the static score or already full.
func (l *L4) UncountedItems() []L4Item {
	return l.items(false)
}

func (l *L4) items(counted bool) []L4Item {
	var out []L4Item
	for _, it := range l.Items {
		if it.Counted() == counted {
			out = append(out, it)
		}
	}
	return out
}

// Standard is a public agent standard a native system publishes.
type Standard struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// standards are the well-known documents of docs/reference-isitagentready.md.
var standards = []Standard{
	{ID: "mcp_server_card", Name: "MCP Server Card", Path: "/.well-known/mcp/server-card.json"},
	{ID: "oauth_protected_resource", Name: "OAuth Protected Resource（RFC 9728）", Path: "/.well-known/oauth-protected-resource"},
	{ID: "api_catalog", Name: "API Catalog（RFC 9727）", Path: "/.well-known/api-catalog"},
}

// recommendL4 lists the code changes for what v gives the agent. The gain of a
// scored item is read off the check it belongs to, so it moves with the
// scoring standard.
func recommendL4(v View, tools []spec.Operation, r Report) *L4 {
	l4 := &L4{Standards: uncheckedStandards()}
	byID := map[string]Check{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	scored := func(it L4Item) {
		c := byID[it.Check]
		it.Gain = round2(c.Max - c.Points)
		it.Measured = "靜態：" + c.Name
		if it.Gain == 0 {
			it.Handled = true
			it.Measured += "，已拿滿，不再加分"
		}
		l4.Gain += it.Gain
		l4.Items = append(l4.Items, it)
	}
	if it, ok := sideEffectGETItem(v); ok {
		scored(it)
	}
	if it, ok := errorItem(tools); ok {
		scored(it)
	}
	if it, ok := authItem(v, tools); ok {
		scored(it)
	}
	scored(identityItem())
	if it, ok := pagingItem(tools); ok {
		scored(it)
	}
	for _, it := range []func(View, []spec.Operation) (L4Item, bool){dryRunItem, idempotencyItem} {
		if item, ok := it(v, tools); ok {
			item.Measured = "動態：安全分與 gate"
			l4.Items = append(l4.Items, item)
		}
	}
	if it, ok := missingItem(v, tools); ok {
		l4.Items = append(l4.Items, it)
	}
	l4.Gain = round2(l4.Gain)
	return l4
}

func uncheckedStandards() []Standard {
	out := make([]Standard, len(standards))
	copy(out, standards)
	for i := range out {
		out[i].Status = StandardUnchecked
		out[i].Detail = "沒有給 --origin，未檢查"
	}
	return out
}

// sideEffectGETItem looks at every operation the gateway can forward, hidden
// or not: a GET that policy hides still has to change in the legacy code.
func sideEffectGETItem(v View) (L4Item, bool) {
	served, _ := serving.RawTools(v.Doc)
	var found []string
	var byName, byDeclaration int
	for _, t := range served {
		op := t.Op
		if op.Method != "GET" || !v.Signals.ScoreMutating(op) {
			continue
		}
		word, ok := v.Signals.ScoreSideEffectGET(op)
		if ok {
			byName++
		} else {
			byDeclaration++
			word = "宣告 " + op.AgentRisk
		}
		found = append(found, name(op)+"（"+word+"）")
	}
	if len(found) == 0 {
		return L4Item{}, false
	}
	var sources []string
	if byName > 0 {
		sources = append(sources, fmt.Sprintf("%d 個 GET 的名字是動作", byName))
	}
	if byDeclaration > 0 {
		sources = append(sources, fmt.Sprintf("%d 個 GET 被 overlay 宣告成會改資料", byDeclaration))
	}
	return L4Item{
		ID: "side_effect_gets", Name: "把會改資料的 GET 改成 POST", Check: "side_effect_gets",
		Detail:   strings.Join(sources, "、") + "；L3 policy 只能擋住它們，改掉才是根本",
		Examples: examples(found),
		Instruction: "下列 GET 端點會改資料，但 GET 應該是安全的查詢（爬蟲、預取、agent 都會當它無害）。" +
			"請把每一個改成 POST（刪除類改 DELETE），路徑和權限檢查維持不變，並同步更新 OpenAPI 文件和所有呼叫端；" +
			"舊的 GET 路徑回 405，不要繼續執行動作。\n" + bullets(found),
	}, true
}

func errorItem(tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if !hasErrorResponse(op) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	return L4Item{
		ID: "error_responses", Name: "錯誤回應結構化並寫進文件", Check: "error_responses",
		Detail:   fmt.Sprintf("%d/%d 個 tool 沒有寫 4xx/5xx 回應；agent 看不懂錯誤就沒辦法自己修正", len(missing), len(tools)),
		Examples: examples(missing),
		Instruction: "請讓所有錯誤回應用同一個結構 application/problem+json（RFC 9457）：type、title、status、detail，" +
			"另加 errors 陣列指出哪個欄位錯在哪、該怎麼改。然後在 OpenAPI 為每支 API 寫出 400、401、403、404、409、500 中實際會出現的回應，" +
			"schema 指向共用的 Problem。先從下列沒寫任何錯誤回應的端點開始：\n" + bullets(examples(missing)),
	}, true
}

func authItem(v View, tools []spec.Operation) (L4Item, bool) {
	var open []string
	for _, op := range tools {
		if !op.Secured {
			open = append(open, name(op))
		}
	}
	if len(v.Doc.SecuritySchemes) > 0 && len(open) == 0 {
		return L4Item{}, false
	}
	return L4Item{
		ID: "auth_declared", Name: "宣告驗證方式", Check: "auth_declared",
		Detail:   fmt.Sprintf("securitySchemes：%s；%d/%d 個 tool 沒標 security", list(v.Doc.SecuritySchemes), len(open), len(tools)),
		Examples: examples(open),
		Instruction: "請在 OpenAPI 的 components.securitySchemes 寫出系統實際使用的驗證方式（例如 bearer token、或登入後的 session cookie 用 apiKey in cookie），" +
			"並在頂層 security 或每支 operation 標上要用哪一個；不需驗證的端點（如登入）明確寫 security: []。" +
			"讓 agent 光看文件就知道先登入再帶什麼。",
	}, true
}

func identityItem() L4Item {
	return L4Item{
		ID: "agent_identity", Name: "給 agent 真正的機器身份", Check: "agent_identity",
		Detail: "gateway 的身份只是紀錄上的標籤，舊系統看不到；分數已經由 gateway 拿到的話，這項不再加分",
		Instruction: "請讓系統支援機器身份：agent 用自己的 service account（OAuth client credentials）取得 token，不共用任何人的帳號。" +
			"token 帶 sub（agent）與 act（代表的人，RFC 8693）；權限依這組身份判斷，可以單獨停用某個 agent；" +
			"稽核紀錄同時寫下 agent 與代表的人。",
	}
}

func pagingItem(tools []spec.Operation) (L4Item, bool) {
	conventions := map[string]int{}
	convOf := map[string]string{}
	var lists, bounded int
	var unbounded []string
	for _, op := range tools {
		params := PagingParams(op)
		if len(params) == 0 {
			continue
		}
		lists++
		var names []string
		limited := false
		for _, p := range params {
			names = append(names, strings.ToLower(p.Name))
			if _, ok := p.Schema["maximum"]; ok {
				limited = true
			}
		}
		sort.Strings(names)
		key := strings.Join(names, "+")
		conventions[key]++
		convOf[name(op)] = key
		if limited {
			bounded++
		} else {
			unbounded = append(unbounded, name(op))
		}
	}
	if lists == 0 {
		return L4Item{}, false
	}
	common, most := mostCommon(conventions)
	var odd []string
	for n, k := range convOf {
		if k != common {
			odd = append(odd, n)
		}
	}
	sort.Strings(odd)
	listed := union(odd, unbounded)
	if most == lists && bounded == lists {
		return L4Item{}, false
	}
	return L4Item{
		ID: "paging", Name: "分頁參數一致並設上限", Check: "paging",
		Detail:   fmt.Sprintf("%d 個列表：%d 個不是最常見的分頁參數 %s；%d 個沒寫 page size 上限", lists, len(odd), common, len(unbounded)),
		Examples: examples(listed),
		Instruction: fmt.Sprintf("請讓所有列表 API 用同一組分頁參數 %s，不一致的列表改成它（舊參數可暫時保留相容）。"+
			"每個列表的 page size 在 OpenAPI 用 maximum 寫出上限，伺服器端也要強制，超過就回 400。需要統一或補上限的端點：\n%s",
			common, bullets(examples(listed))),
	}, true
}

func dryRunItem(v View, tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if v.Signals.ScoreMutating(op) && !hasInput(op, dryRunParam) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	return L4Item{
		ID: "dry_run", Name: "危險操作支援 dry-run",
		Detail:   fmt.Sprintf("%d 個會改資料的 tool 沒有 dry-run 參數；agent 無法在送出前預覽結果", len(missing)),
		Examples: examples(missing),
		Instruction: "請為會改資料的端點（優先是金額、狀態會變的）加上 dryRun=true 參數：照常驗證並計算結果、回傳「會改什麼」，" +
			"但不寫入資料庫、不發通知、不打外部系統；在 OpenAPI 寫出這個參數與預覽回應。優先處理：\n" + bullets(examples(missing)),
	}, true
}

func idempotencyItem(v View, tools []spec.Operation) (L4Item, bool) {
	var missing []string
	for _, op := range tools {
		if v.Signals.ScoreMutating(op) && !hasInput(op, idempotency) {
			missing = append(missing, name(op))
		}
	}
	if len(missing) == 0 {
		return L4Item{}, false
	}
	return L4Item{
		ID: "idempotency_key", Name: "寫入操作支援 idempotency key",
		Detail:   fmt.Sprintf("%d 個會改資料的 tool 沒看到 idempotency key（文件裡的 query、header 參數或 body 欄位）；逾時重試可能重複執行", len(missing)),
		Examples: examples(missing),
		Instruction: "請讓會改資料的端點接受 Idempotency-Key（header 或 body 欄位 idempotencyKey）：" +
			"同一個 key 在保存期限內重送，回傳第一次的結果、不重複執行；同 key 不同內容回 409。" +
			"保存期限至少 24 小時，並寫進 OpenAPI。優先處理金額類端點：\n" + bullets(examples(missing)),
	}, true
}

// hasInput reports whether op takes a query, header or cookie parameter, or a top-level body
// field whose name matches re.
func hasInput(op spec.Operation, re *regexp.Regexp) bool {
	for _, p := range op.Params {
		if p.In != "path" && re.MatchString(p.Name) {
			return true
		}
	}
	props, _ := op.Body["properties"].(map[string]any)
	for k := range props {
		if re.MatchString(k) {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func bullets(xs []string) string {
	var b strings.Builder
	for _, x := range xs {
		if strings.HasPrefix(x, "…") {
			x += "，一併處理"
		}
		b.WriteString("- " + x + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// CheckStandards looks for each public agent standard under origin and
// records what it found on the report. It is the one part of the score that
// touches the network, so the caller opts in with an origin.
func CheckStandards(ctx context.Context, client *http.Client, origin string, r *Report) error {
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("origin %q must be an absolute URL such as https://api.example.com", origin)
	}
	if r.L4 == nil {
		return nil
	}
	for i, s := range r.L4.Standards {
		r.L4.Standards[i].Status, r.L4.Standards[i].Detail = probe(ctx, client, base.ResolveReference(&url.URL{Path: s.Path}).String())
	}
	return nil
}

func probe(ctx context.Context, client *http.Client, target string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return StandardMissing, err.Error()
	}
	resp, err := client.Do(req)
	if err != nil {
		return StandardMissing, "連不上：" + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	// A catch-all page answers 200 for any path; only JSON counts as the document.
	if resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return StandardFound, target
	}
	return StandardMissing, fmt.Sprintf("%s 回 %d", target, resp.StatusCode)
}

func renderL4(b *strings.Builder, l4 *L4) {
	if l4 == nil {
		return
	}
	if l4.Gain == 0 {
		b.WriteString("\nL4：要改原系統 code 才拿得到的（靜態分已經沒有可再加的）\n")
	} else {
		fmt.Fprintf(b, "\nL4：要改原系統 code 才拿得到的（靜態分最多再 +%.2f）\n", l4.Gain)
	}
	for _, it := range l4.CountedItems() {
		renderItem(b, fmt.Sprintf("+%.2f 分", it.Gain), it)
	}
	if uncounted := l4.UncountedItems(); len(uncounted) > 0 {
		b.WriteString("\n靜態分不計、原系統仍建議改（已由 overlay、policy 或 gateway 處理的，或只在動態分看得到的）\n")
		for _, it := range uncounted {
			renderItem(b, "·", it)
		}
	}
	b.WriteString("\n公開的 agent 標準（參考 docs/reference-isitagentready.md，不計分）\n")
	for _, s := range l4.Standards {
		fmt.Fprintf(b, "  %-8s %s %s：%s\n", s.Status, s.Name, s.Path, s.Detail)
	}
}

func renderItem(b *strings.Builder, mark string, it L4Item) {
	fmt.Fprintf(b, "\n%s  %s（%s）\n    %s\n", mark, it.Name, it.Measured, it.Detail)
	for _, e := range it.Examples {
		fmt.Fprintf(b, "      - %s\n", e)
	}
	fmt.Fprintf(b, "    貼給 coding agent：\n%s\n", indent(it.Instruction, "      "))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
