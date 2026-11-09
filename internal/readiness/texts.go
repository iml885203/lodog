package readiness

// Texts are the Chinese templates of every sentence the score writes: what
// lodog score prints, and the Chinese of the report page, whose dictionary
// takes them as they are (its English is in internal/report/i18n_en.go).
// {0}, {1} … are the arguments of a Text.
var Texts = map[string]string{
	// lists
	"rd.join":      "{0}、{1}",
	"rd.join.semi": "{0}；{1}",
	"rd.lines":     "{0}\n{1}",
	"rd.bullet":    "- {0}",
	// the last bullet of a capped list: the rest are to be done as well
	"rd.bullet.more": "- {0}，一併處理",
	"rd.more":        "…還有 {0} 個",
	"rd.none":        "無",
	// an endpoint with the word or risk that marks it
	"rd.marked": "{0}（{1}）",

	"rd.check.descriptions":     "說明覆蓋",
	"rd.check.tool_count":       "Tool 數量",
	"rd.check.risk_declared":    "風險標記",
	"rd.check.side_effect_gets": "GET 卻會改資料",
	"rd.check.auth_declared":    "Auth 宣告",
	"rd.check.agent_identity":   "Agent 身份",
	"rd.check.error_responses":  "錯誤回應",
	"rd.check.paging":           "分頁一致",

	"rd.descriptions.detail":     "{0}/{1} 個 tool 有說明，{2}/{3} 個參數有說明",
	"rd.tool_count.detail":       "agent 看到 {0} 個 tool；{1} 個以內拿滿分",
	"rd.risk_declared.detail":    "{0}/{1} 個會改資料的 tool 明確宣告了風險等級",
	"rd.risk_declared.policy":    "{0}；{1} 個只落在 policy 的 default，不算宣告",
	"rd.side_effect_gets.detail": "{0} 個 GET 的名字是動作；{1} 個沒有被標風險或擋下，有任何一個就不給分",
	"rd.auth_declared.detail":    "securitySchemes：{0}；{1}/{2} 個 tool 標了 security",
	"rd.agent_identity.none":     "呼叫帶不出是哪個 agent、代表哪個人",
	"rd.agent_identity.given":    "gateway 給 agent 自己的身份，並帶出代表的人",
	"rd.error_responses.detail":  "{0}/{1} 個 tool 寫了 4xx/5xx 回應",
	"rd.paging.nolists":          "沒有分頁的列表",
	"rd.paging.detail":           "{0} 個列表；{1} 個用最常見的分頁參數 {2}；{3} 個寫了 page size 上限",
	"rd.tasks.note":              "{0} 的 tool 是 {1} 個任務 tool（workflows），每個依它的 workflow 說明、輸入和步驟呼叫的 API 評分",

	"rd.l4.static":      "靜態：{0}",
	"rd.l4.static.full": "靜態：{0}，已拿滿，不再加分",
	"rd.l4.dynamic":     "動態：安全分與 gate",

	"rd.std.oauth_protected_resource": "OAuth Protected Resource（RFC 9728）",
	"rd.std.api_catalog":              "API Catalog（RFC 9727）",
	"rd.std.unchecked":                "沒有給 --origin，未檢查",
	"rd.std.unreachable":              "連不上：{0}",
	"rd.std.status":                   "{0} 回 {1}",

	"rd.l4.side_effect_gets.name":     "把會改資料的 GET 改成 POST",
	"rd.l4.side_effect_gets.declared": "宣告 {0}",
	"rd.l4.side_effect_gets.byname":   "{0} 個 GET 的名字是動作",
	"rd.l4.side_effect_gets.byrisk":   "{0} 個 GET 被 overlay 宣告成會改資料",
	"rd.l4.side_effect_gets.detail":   "{0}；L3 policy 只能擋住它們，改掉才是根本",
	"rd.l4.side_effect_gets.lead":     "下列 GET 端點會改資料，但 GET 應該是安全的查詢（爬蟲、預取、agent 都會當它無害）。",
	"rd.l4.side_effect_gets.fix": "{0}請把每一個改成 POST（刪除類改 DELETE），路徑和權限檢查維持不變，並同步更新 OpenAPI 文件和所有呼叫端；" +
		"舊的 GET 路徑回 405，不要繼續執行動作。\n{1}",

	"rd.l4.error_responses.name":   "錯誤回應結構化並寫進文件",
	"rd.l4.error_responses.detail": "{0}/{1} 個 tool 沒有寫 4xx/5xx 回應；agent 看不懂錯誤就沒辦法自己修正",
	"rd.l4.error_responses.lead":   "請讓所有錯誤回應用同一個結構 application/problem+json（RFC 9457）：type、title、status、detail，另加 errors 陣列指出哪個欄位錯在哪、該怎麼改。",
	"rd.l4.error_responses.fix": "{0}然後在 OpenAPI 為每支 API 寫出 400、401、403、404、409、500 中實際會出現的回應，" +
		"schema 指向共用的 Problem。先從下列沒寫任何錯誤回應的端點開始：\n{1}",

	"rd.l4.auth_declared.name":   "宣告驗證方式",
	"rd.l4.auth_declared.detail": "securitySchemes：{0}；{1}/{2} 個 tool 沒標 security",
	"rd.l4.auth_declared.lead": "請在 OpenAPI 的 components.securitySchemes 寫出系統實際使用的驗證方式（例如 bearer token、或登入後的 session cookie 用 apiKey in cookie），" +
		"並在頂層 security 或每支 operation 標上要用哪一個；不需驗證的端點（如登入）明確寫 security: []。",
	"rd.l4.auth_declared.fix": "{0}讓 agent 光看文件就知道先登入再帶什麼。",

	"rd.l4.agent_identity.name":   "給 agent 真正的機器身份",
	"rd.l4.agent_identity.detail": "gateway 的身份只是紀錄上的標籤，舊系統看不到；分數已經由 gateway 拿到的話，這項不再加分",
	"rd.l4.agent_identity.lead":   "請讓系統支援機器身份：agent 用自己的 service account（OAuth client credentials）取得 token，不共用任何人的帳號。",
	"rd.l4.agent_identity.fix": "{0}token 帶 sub（agent）與 act（代表的人，RFC 8693）；權限依這組身份判斷，可以單獨停用某個 agent；" +
		"稽核紀錄同時寫下 agent 與代表的人。",

	"rd.l4.paging.name":   "分頁參數一致並設上限",
	"rd.l4.paging.detail": "{0} 個列表：{1} 個不是最常見的分頁參數 {2}；{3} 個沒寫 page size 上限",
	"rd.l4.paging.lead":   "請讓所有列表 API 用同一組分頁參數 {0}，不一致的列表改成它（舊參數可暫時保留相容）。",
	"rd.l4.paging.fix":    "{0}每個列表的 page size 在 OpenAPI 用 maximum 寫出上限，伺服器端也要強制，超過就回 400。需要統一或補上限的端點：\n{1}",

	"rd.l4.dry_run.name":   "危險操作支援 dry-run",
	"rd.l4.dry_run.detail": "{0} 個會改資料的 tool 沒有 dry-run 參數；agent 無法在送出前預覽結果",
	"rd.l4.dry_run.lead": "請為會改資料的端點（優先是金額、狀態會變的）加上 dryRun=true 參數：照常驗證並計算結果、回傳「會改什麼」，" +
		"但不寫入資料庫、不發通知、不打外部系統；在 OpenAPI 寫出這個參數與預覽回應。",
	"rd.l4.dry_run.fix": "{0}優先處理：\n{1}",

	"rd.l4.idempotency_key.name":   "寫入操作支援 idempotency key",
	"rd.l4.idempotency_key.detail": "{0} 個會改資料的 tool 沒看到 idempotency key（文件裡的 query、header 參數或 body 欄位）；逾時重試可能重複執行",
	"rd.l4.idempotency_key.lead": "請讓會改資料的端點接受 Idempotency-Key（header 或 body 欄位 idempotencyKey）：" +
		"同一個 key 在保存期限內重送，回傳第一次的結果、不重複執行；同 key 不同內容回 409。",
	"rd.l4.idempotency_key.fix": "{0}保存期限至少 24 小時，並寫進 OpenAPI。優先處理金額類端點：\n{1}",

	"rd.l4.missing.name": "補上 agent 需要、API 卻沒有的端點（讀回寫入結果、用 id 查一筆）",
	"rd.l4.missing.detail": "缺 {0} 個端點：{1} 個會改資料的 operation 沒有 GET 讀得回結果，agent 寫完無法自己確認做對了；" +
		"{2} 個端點的集合只有分頁列表，agent 得翻列表找那一筆",
	"rd.l4.missing.detail.runs":   "{0}；{1} 個端點在 benchmark run 裡看得到 agent 少了它怎麼做",
	"rd.l4.missing.measured":      "不在靜態分（建議）",
	"rd.l4.missing.measured.runs": "不在靜態分；附 benchmark run 的呼叫紀錄",
	"rd.l4.missing.lead":          "agent 做完一個寫入後，要能用一支 GET 讀回那一筆，確認狀態真的變了；找一筆紀錄時，要能用 id 直接查，不必翻分頁列表。",
	"rd.l4.missing.fix": "{0}請新增下列 GET 端點：回傳那一筆的完整現況（狀態、金額、更新時間、最後操作者等寫入會改的欄位），找不到回 404；" +
		"並讓對應的寫入端點在回應裡帶回那一筆的 id。路徑是依現有路徑推的建議，照系統慣例調整即可；權限比照同一個集合的列表，並寫進 OpenAPI。\n{1}",
	"rd.missing.line":   "{0}：{1}",
	"rd.missing.write":  "{0} 寫完讀不回",
	"rd.missing.writes": "{0} 個寫入讀不回（{1} 等）",
	"rd.missing.list":   "找一筆只能翻分頁列表 {0}",
	"rd.missing.tasks":  "任務 tool：{0}",
	"rd.missing.runs":   "run：{0}",

	"rd.run.more":    "…還有 {0} 筆",
	"rd.run.scanned": "{0}：{1} 之後翻列表找回剛寫的那筆",
	"rd.run.noread":  "{0}：{1} 之後沒有任何讀取，無法確認寫入結果",
	"rd.run.repeat":  "{0}：同一題成功叫了 {1} 次 {2}",
}
