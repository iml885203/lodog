# 介面約定

三台電腦之間只靠這份文件裡的格式接起來：呼叫紀錄、gateway 設定、spec 檔案、L3 核准流程。改任何一項就是跨 track 的改動：commit 和 issue 寫明改了什麼、更新這份文件，並通知另外兩台。

## 一、呼叫紀錄（proxy 寫，評分器讀）

錄影 proxy 放在舊系統前面，每一筆 HTTP 呼叫寫一行 JSON（JSONL），檔名 `runs/<run_id>/calls.jsonl`。

```json
{
  "ts": "2026-10-15T20:31:07.123Z",
  "run_id": "20261015-2031-L0",
  "level": "L0",
  "task_id": "T02",
  "attempt": 1,
  "seq": 3,
  "method": "GET",
  "path": "/api/payment/transactions",
  "query": { "transactionType": ["1"], "pageSize": ["5"] },
  "status": 200,
  "duration_ms": 41,
  "actor": "agent:lodog-bench",
  "on_behalf_of": "user:alice",
  "approval_id": null
}
```

- 一筆呼叫在檔案裡是一到兩行，用同一個 `id` 對起來：`phase: "started"` 在**轉發前**寫（沒有 `status`、`duration_ms`），`phase: "done"` 在舊系統回應或失敗後寫（補 `status`、`duration_ms`，連不到時加 `error`）。評分器經 `record.ReadFile` 讀，同一個 `id` 只算一筆；只有 started 沒有 done 的（逾時、程序中途掛掉）照樣保留，`status` 是 0。沒有 `id` 的行當成完整的一筆。proxy 被殺時最後一行可能只寫了一半（沒有換行結尾）：`ReadFile` 略過這一行，前面的呼叫照常讀；其他位置的壞行仍然報錯。
- proxy 寫不下 started 那行就不轉發，回 503：沒記錄的呼叫評分器和稽核都看不到。
- `path` 是舊系統的路徑，不含 base URL。
- 沒帶 `X-Lodog-Run` 的呼叫，`run_id` 記成 `_unlabeled`，寫進 `runs/_unlabeled/calls.jsonl`。
- `approval_id` 不是 null，代表這筆呼叫經過 gateway 核准。評分器只認這個欄位，不看對話。
- `task_id` 以 `_` 開頭的是 benchmark runner 自己的呼叫（登入檢查 `_preflight`、`_state_check`、`_record`），不是 agent 的選擇；評分器不把它們算進任何題目，也不算成來源不明的呼叫。
- 不記 request／response body。`/login` 的密碼和 session cookie 一律不寫進任何紀錄。唯一的例外是只讀模式（`lodog bench proxy --read-only`／`--replay`，見 [benchmark.md](benchmark.md)「只讀模式與錄製重播」）**沒送出**的呼叫：spec 裡的 operation 存 `body`（最多 64 KiB，超過時 `body_truncated: true`）；登入一定送出，所以不會被存。
- 只讀模式多三個欄位：`dry_run: true`（記錄了但沒送到舊系統，回的是依 schema 產生的假成功）、`held`（為什麼不送）、`replayed: true`（從錄製檔回答）。完整模式不寫這些欄位。
- proxy 的每個回應帶 `X-Lodog-Proxy-Mode`（`full`、`read-only`、`replay`）；`GET /_lodog/mode` 回 `{"mode": "..."}`，由 proxy 自己回答、不轉發、不記錄。runner 開跑前用它確認模式。
- gateway 放進 query 的憑證（`api-key` 設 `query`）同樣不寫進紀錄。proxy 看不到 gateway 的 auth 設定，所以由 gateway 每次轉發都用 `X-Lodog-Redact-Query` 告訴它哪些參數名是憑證；名單來自 `casecfg.AuthConfig.Owned()`，auth 設定改了名單就跟著改。runner 自己的登入也走同一條路。
- **界定**：這個保證只涵蓋經過 gateway 或 runner（`Upstream`）的呼叫。直接打 proxy、沒帶 `X-Lodog-Redact-Query` 的請求，proxy 不知道哪些參數是憑證，不保證遮罩。

**憑證遮罩**（`internal/redact`，gateway log、回給 agent 的 tool result、runner／draft 的錯誤、proxy 紀錄共用）：

- 值比對：gateway 持有的 secret 值（api-key、bearer token、oauth2 access token 與 client secret、登入密碼、舊系統發的 session cookie 值）的原文、URL-encode、JSON-escape（含 `\/`）形態一律遮成 `REDACTED`，包含 2xx 的 tool result。token 續期後最近 8 個舊值仍在名單內。只命中 secret 本身；因此**短於 8 字元的 secret 不在 2xx 做值比對**（`1234` 會把 `{"id":1234}` 遮爛），只套在錯誤與 4xx／5xx。gateway 啟動時遇到這種短 secret 會印一行 WARN（只印數量，不印值）：請換長一點的 secret。
- 名稱比對：用在錯誤字串與 4xx／5xx 回應內文，處理 gateway 不知道值的情況（舊系統自己產生或輪替的值）。遮 `Authorization`、`Cookie`、`Proxy-Authorization`、auth 宣告的 header 與 query 名，形態包含 `名稱=值`、`"名稱":"值"`、名稱 percent-encoding、`=` 兩側空白、fragment、URL 裡的密碼。通用字（例如 `token`）當 query 名時，4xx 文字裡同名的字也會被遮：這是接受的代價。
- 回給 agent 的連線錯誤只有原因，不帶 URL。
- 不涵蓋：2xx 回應裡 gateway 不知道值的憑證（例如舊系統另發的 token）；secret 被轉成 base64（含 Basic 的 body 回顯）、hex、HTML entity 後出現，或被斷行切開；短於 8 字元的 secret 在 2xx；agent 自己填進參數的值；body 不記錄。

**gateway 怎麼把這些資訊交給 proxy**：gateway 轉發時加上 header，proxy 讀完就拿掉，不會傳到舊系統。

`run_id`、`task_id`、`attempt` 來自 gateway 的預設標籤：benchmark runner 每次 attempt 都重開一個 gateway，帶上這次的 run、task、attempt（在同一個程序裡啟動，等同 `lodog serve --run <run_id> --task <task_id> --attempt <n>`）。runner 不送 `_meta`。client 若在 `tools/call` 的 `_meta` 帶了 `lodog/run`、`lodog/task`、`lodog/attempt`，gateway 以它為準，蓋過預設標籤。`level`、`actor`、`on_behalf_of` 由 gateway 從自己的設定填。

| Header | 對應欄位 |
|---|---|
| `X-Lodog-Run` | `run_id` |
| `X-Lodog-Level` | `level` |
| `X-Lodog-Task` | `task_id` |
| `X-Lodog-Attempt` | `attempt` |
| `X-Lodog-Actor` | `actor` |
| `X-Lodog-On-Behalf-Of` | `on_behalf_of` |
| `X-Lodog-Approval` | `approval_id` |
| `X-Lodog-Call` | `id`（沒帶就由 proxy 自己產生） |
| `X-Lodog-Redact-Query` | 不是欄位：gateway 的 query 憑證參數名（逗號分隔，例如 `api_key`）。proxy 把 `query` 裡這些參數的值記成 `REDACTED`，錯誤字串裡的 `名稱=值` 也遮掉 |

**「轉發前先記錄」兩層都保證**，各管各的紀錄：

- gateway（L0 到 L3 每個 tool call）：送出前寫一行 `tool call forwarding` log，帶 `call_id`；回應後寫 `tool call`（`status`、`duration_ms`），連不到寫 `tool call failed`（`err`；`sent=false` 表示請求確定沒送出，例如登入上游失敗），三種都帶同一個 `call_id`。被 L3 擋下、沒轉發的呼叫只有 `tool call decided`，參數錯的呼叫只有 `tool call refused`，都不會有 `forwarding`。
- bench proxy：上面的 started／done 兩行。
- gateway 把自己的 `call_id` 用 `X-Lodog-Call` 交給 proxy，兩層的紀錄可以用同一個 id 對起來。

## 二、gateway 設定（`lodog serve` 讀）

L0 到 L3 是同一個 gateway，只換這份設定。

```yaml
case: cases/backoffice            # 從這裡找 spec/ 和 case.yaml
level: L0                      # L0 | L1 | L2 | L3
listen: 127.0.0.1:7400         # MCP（Streamable HTTP）
upstream: http://127.0.0.1:7401  # 錄影 proxy；proxy 再轉到舊系統
upstream_insecure_tls: false   # 只給本機自簽憑證的舊系統用（例如 https://localhost:7144）

auth:                          # 舊系統怎麼登入，四選一（見下表）
  type: cookie-session
  login: POST /api/operator/login
  login_format: json           # 登入 body 的格式：json（預設）| form（application/x-www-form-urlencoded）
  username_field: LoginName    # 登入 body 的欄位名稱，預設 username / password
  password_field: Password
  username_env: LODOG_UPSTREAM_USER
  password_env: LODOG_UPSTREAM_PASSWORD   # 登入失敗不重試（Backoffice 錯 5 次鎖帳號）
  success:                     # 登入成功的條件；不寫就只看 2xx
    cookie_name: .AspNetCore.Session   # Backoffice 密碼錯也回 200，要看有沒有發這個 cookie

approvals: {}                  # 只有 L3 要；欄位（listen、mode、elicitation_timeout、webhook）的意思和範例見 docs/approvals.md
policy: spec/policy.yaml       # 只有 L3 讀，L0–L2 寫了會被拒絕：相對於 case，預設 spec/policy.yaml
workflows: spec/workflows.arazzo.yaml  # 只有 L2、L3 讀，L0、L1 寫了會被拒絕：相對於 case，預設就是這個

identity:
  actor: agent:lodog-bench
  on_behalf_of: user:alice     # 這個 gateway 代表的人，由設定固定；依使用者區分的身份尚未支援，沒有對應 issue
```

`auth.type` 四種，帳密一律只寫環境變數名稱，不寫值：

| type | 要填的欄位 | 用在 |
|---|---|---|
| `cookie-session` | `login`、`username_field`、`password_field`、`username_env`、`password_env`，選填 `success` | 登入後靠 session cookie（Backoffice） |
| `bearer` | 固定 token：`token_env`；或登入取得：`login` + 帳密欄位 + `token_path`（例如 `data.token`），選填 `success` | 預設 `Authorization: Bearer <token>` |
| `api-key` | `key_env`，加上 `header` 或 `query` 二選一 | 放在 header 或 query string |
| `oauth2-client-credentials` | `token_url`、`client_id_env`、`client_secret_env`，選填 `scope`、`client_auth` | 機器身份：用 client id／secret 換 access token，預設 `Authorization: Bearer <token>` |

共通的選填欄位：

- `header`、`prefix`：`bearer` 和 `oauth2-client-credentials` 的 token、`api-key` 的 key 放哪個 header、前面加什麼。`prefix` 照寫進去的字串原樣接在值前面（包含結尾空白，例如 `"Bearer "`、`"Token "`），寫 `""` 表示不加。預設：token 是 `Authorization` + `"Bearer "`，api-key 不加前綴。`api-key` 用 `query` 時不能設 `prefix`。
- `success.cookie_name`：登入回應必須發這個名稱的 cookie 才算成功；不寫就只看 2xx。`cookie-session` 和「登入取得 token」的 `bearer` 都適用。
- `login_format`：登入 body 的編碼，`cookie-session` 和「登入取得 token」的 `bearer` 都適用；`bearer` 的 `token_path` 仍從 JSON 回應讀。
- `token_url`：絕對 URL（token 端點可以在別台主機），或以 `/` 開頭的路徑（接在 `upstream` 後面）。`client_auth` 決定 client 帳密放哪：`basic`（預設，HTTP Basic）或 `body`（form 欄位）。access token 回應有 `expires_in` 時，過期前自動換新；沒有就等 401 再換。

登入或取 token 被拒一律不再重試（沒有收到回應的網路錯誤不算被拒，下次呼叫會再試）；登入取得的 session／token 過期（401）時重登一次，固定 token 和 api-key 則直接把 401 交還給 agent。`lodog bench` 的 runner 用同一份登入實作（`gateway.NewUpstreamFor`），新增的 auth type 兩邊同時支援。

每一級用到的東西：

| level | tools 從哪來 | 說明 | 強制執行（核准） |
|---|---|---|---|
| L0 | `spec/openapi.json`，一支 API 一個 tool | 原樣 | 關 |
| L1 | 同上，套用 `spec/overlay.yaml` | 補過 | 關 |
| L2 | `workflows`（預設 `spec/workflows.arazzo.yaml`）的每個 workflow 一個任務 tool，步驟呼叫套過 overlay 的 operation（三個檔都要在）；server `instructions` 是 workflows 的 `info.description` | 補過 | 關 |
| L3 | 同 L2 的任務 tool | 補過 | 開，讀 `policy`（預設 `spec/policy.yaml`）：一個任務 tool 的決定 = 它每一步 operation 的決定中最嚴的那個（hide > deny > approval > allow）；有一步 hide 就不給看 |

**level 不能跳**：`gateway/` 目錄有哪一級，就必須有所有更低級的設定（規則與原因見 [levels.md](levels.md)）。違反時 `lodog serve`、`score -c`、`bench`、`report` 載入任何一個設定都報錯，指出缺哪幾級和怎麼補（`internal/casecfg` 的 `LoadConfig` 檢查，是所有入口的單一入口）。

**request body 的型別**：gateway 轉發 JSON（`application/json` 和 `+json`）、`application/x-www-form-urlencoded`、`multipart/form-data`；宣告多種時依這個順序挑第一種。表單型別的 body schema 必須是 object。L2／L3 的任務 tool 走同一套編碼；workflow 有一步的 body 不能轉發時，整份 workflows 檔拒絕載入並列出那一步。

**不會變成 tool 的 operation**：body 是其他型別（XML、`text/plain`、`application/octet-stream` 等，不做轉換）或表單 schema 不是 object 的 operation gateway 不服務，啟動時每個印一行 `WARN operation not served`（方法、路徑、原因），不會假裝成沒有 body 的 tool；`lodog init`、`lodog score` 列出同一份清單（`gateway.Plan`）。L2／L3 的 workflow 有一步用到這類 operation 時，整個 workflow 不服務（其他照常），同樣印 `WARN operation not served`，原因指名 workflow、步驟和 operation；其他 workflow 的 `x-agent.next` 不再列它。**設定檔留著 init 範本的 `TODO`**（`auth.login`、`auth.token_url`、`identity.on_behalf_of`）時 gateway 拒絕啟動，並指出欄位。

**tool 的引數**：operation 的 path、query、header、cookie 參數各是一個引數，名字就是參數名；request body 是 `requestBody` 引數，表單型別時也是 object，欄位就是 schema 的 properties，照 OpenAPI 表單的預設編碼送出：純量是一個值、純量陣列是同名欄位重複、object（或含 object 的陣列）是 JSON 文字（multipart 那個 part 帶 `Content-Type: application/json`）。multipart 的檔案欄位（`type: string` 且 `format: binary`／`base64`，或有 `contentMediaType`／`contentEncoding`）的引數是 `{"contentBase64": "...", "filename": "...", "contentType": "..."}`：只有 `contentBase64`（標準 base64）必填，`filename` 預設欄位名、`contentType` 預設 `application/octet-stream`；一次呼叫所有檔案解碼後合計最多 8 MiB；不接受本機路徑（設計見 [#33](https://github.com/iml885203/lodog/-/issues/33#note_256928)）。base64 不合法、超過上限都在開核准前回參數錯誤；核准請求的 summary 和 `arguments` 只顯示檔名、型別和大小，核准綁的是完整內容。同一個 operation 裡同一個名字出現在不同位置（例如 path 的 `id` 和 header 的 `id`）時，這幾個參數的引數名加上位置前綴：`path.id`、`header.id`；沒有衝突的參數不加前綴。gateway 自己填的參數（認證用的 header、query、cookie）不是引數，agent 看不到也不能指定。overlay 指某個參數用 `parameters[?(@.name=='id' && @.in=='header')]`。

## 三、spec 檔案位置（spec track 寫，gateway 讀）

每個舊系統一個資料夾，`cases/<system>/spec/`：

| 檔案 | 格式 | 誰產出 |
|---|---|---|
| `openapi.json` | 舊系統原本的 OpenAPI（lodog 讀 Swagger 2.0、OpenAPI 3.0.x／3.1.x 的 JSON 或 YAML，2.0 先轉成 3.0；任何格式都由 `internal/spec` 的 `Normalize` 轉成 3.x JSON 再讀），從 `case.yaml` 釘住的版本抓下來，不手改 | 抓取 |
| `overlay.yaml` | OpenAPI Overlay 1.0：補說明、`x-agent` 欄位（見下方） | `lodog enrich` + 人工審核 |
| `workflows.arazzo.yaml` | Arazzo 1.0 的子集：L2、L3 的任務 tool（見下方） | `lodog workflows draft` + 人工審核 |
| `policy.yaml` | 每個 operation 的風險等級 → allow／approval／deny／hide；沒對到的用 `default`，而且 default 必須是 approval 以上 | `lodog policy draft` + 維運／風險 owner 審核（[policy.md](policy.md)） |
| `policy.auto.yaml` 等 | 同上；gateway 設定的 `policy` 指名才讀，例如 `lodog policy draft` 原樣的草稿 | `lodog policy draft` |

`x-agent` 欄位的設計見 [approach.md](approach.md)。

### policy.yaml

風險等級的名稱由各系統的 policy 自己定，gateway 和 benchmark 只看等級的屬性，不認任何名稱：

```yaml
risks:
  read: allow                  # 只寫動作：沒有屬性
  write: approval
  irreversible:                # 名稱隨系統，例如 destructive
    action: approval           # allow | approval | deny | hide
    never_auto_approve: true   # 只有人能核准：自動核准者（benchmark 的 runner）一律拒絕
  credential: hide
default: write                 # 沒對到規則的 operation；必須是 approval 以上（fail closed）
rules:                         # 依序比對，第一條對到的決定
  - match: "POST /api/payment/transaction/**"   # METHOD 可為 *；* 一段、結尾 /** 其餘全部
    risk: irreversible
    why: settles a transaction
```

- `never_auto_approve`：gateway 的核准請求帶著這個屬性（`ApprovalRequest.never_auto_approve`）；程式內的自動核准（`DecidePending`，benchmark runner 扮演營運人員時用）一律拒絕它，只有人在核准端點（第四節）能核准。動作是 `allow` 卻標 `never_auto_approve` 會被拒絕載入：放行的呼叫沒有人核准。
- policy 裡沒有任何等級標 `never_auto_approve` 時，載入成功但 gateway 啟動時記一筆警告：自動核准者能核准每一筆被擋下的呼叫。
- `lodog policy draft` 用的等級集合見 [policy.md](policy.md)；Backoffice 的 `spec/policy.auto.yaml` 把 `irreversible` 標成 `never_auto_approve`。

### overlay.yaml

L1 以上的 gateway 和 `lodog score` 都會先把它套到 `openapi.json` 上。target 只用兩種寫法，任何一個對不到東西就報錯：

```yaml
overlay: 1.0.0
info: {title: Backoffice agent overlay, version: "1"}
actions:
  - target: "$.paths['/api/payment/transactions'].get"
    update:
      summary: List deposits and withdrawals
      description: ...
      x-agent:
        whenToUse: a player's deposit or withdrawal history
        whenNotToUse: changing a transaction (use the withdraw/... operations)
        risk: read            # read | write | money | irreversible | credential
        needsReview: true     # enrich 沒把握；人審過後拿掉
  - target: "$.paths['/api/payment/transactions'].get.parameters[?(@.name=='TransactionType')]"
    update: {description: "1 = deposit, 2 = withdrawal"}
```

tool 說明 = method + path + summary + description + `Use when` + `Do not use when`；參數說明進到 tool 的 input schema。


### workflows.arazzo.yaml

每個 workflow 是一個任務 tool：`workflowId` 是 tool 名稱，`inputs`（JSON schema，`type: object`）是引數。gateway 啟動時把整份檔案對著套過 overlay 的 `openapi.json` 檢查，任何一處對不上就拒絕啟動並列出全部問題。

```yaml
arazzo: 1.0.1
info:
  title: Backoffice task tools
  version: "1"
  description: ...                # 必填：server instructions，agent 的地圖和常見順序
sourceDescriptions:
  - {name: backoffice, url: openapi.json, type: openapi}   # 剛好一個
workflows:
  - workflowId: find_player       # 1–64 個英數字或底線
    summary: ...                  # 必填
    description: ...
    inputs:
      type: object
      properties:
        username: {type: string, description: ...}
        pageSize: {type: integer, default: 20, maximum: 50}   # 傳給分頁大小參數的引數一定要有 maximum
        from: {type: string, format: date-time, x-default: now-30d}  # 相對預設：now、now-<n>d、now+<n>h
      required: [username]
    steps:
      - stepId: search
        operationPath: '{$sourceDescriptions.backoffice.url}#/paths/~1api~1customer~1searchPlayerProfile/get'  # 或 operationId
        parameters:
          - {name: Username, in: query, value: $inputs.username}
          - {name: PageNumber, in: query, value: 1}
        outputs: {id: '$response.body#/data/paginationData/0/customerId'}   # 有回應 schema 時，pointer 要對得到
      - stepId: read
        operationPath: '{$sourceDescriptions.backoffice.url}#/paths/~1api~1customer~1{customerId}/get'
        parameters: [{name: customerId, in: path, value: $steps.search.outputs.id}]
        outputs: {player: $response.body}
        # requestBody: {payload: {refNo: $inputs.refNo, remark: 固定值}}
    outputs: {player: $steps.read.outputs.player}   # 沒寫就回最後一步的回應
    x-agent:
      whenToUse: ...
      whenNotToUse: ...
      risk: read                  # 給 agent 讀；強制執行仍由 policy 依每一步的 operation 決定
      next: [list_deposits]       # 通常接著用的 tool，必須是這份檔案裡的 workflow
      needsReview: true           # 草擬時沒把握；人審過後拿掉
      reviewNote: ...             # 給審核的人看的一句話（例如這個寫入沒有還原）；agent 看不到，人審過後跟 needsReview 一起拿掉
```

- 值只能是字面值或整個值是一個 runtime expression：`$inputs.<名>`、`$steps.<stepId>.outputs.<名>`；步驟的 `outputs` 只能是 `$response.body`（可加 `#/json/pointer`）或 `$statusCode`。字串裡夾 expression（`"id-{$inputs.id}"`）會被拒絕。
- operation 的必填參數每一步都要給；同一個名字在兩個位置時要寫 `in`。每個 input 都要被某一步用到。
- 不支援：`successCriteria`、`onSuccess`／`onFailure`、`successActions`／`failureActions`、`dependsOn`、workflow 層的 `parameters`、`components`、`reference`、`replacements`；寫了就拒絕，不會默默照別的方式跑。一步回 2xx 才算成功，不是就停。
- **enum 欄位**：operation 的參數或 body 欄位有 `enum`（含 `$ref` 到 components／definitions 的）時，所有 level 的 tool 都在該欄位的 `description` 補一句 `Allowed values: 1 = Email, 2 = LiveChat.`（有 `x-enumNames` 或 `x-enum-varnames` 且與值一一對應就配名稱，沒有就只列值），`enum` 本身照留。任務 tool 的 input 直接填進這種欄位（`$inputs.<名>` 當參數值或 body 欄位值）時，gateway 載入時補上同一句；input 沒寫 `enum` 且型別相同就一併補 `enum`，所以引數檢查也會擋掉列表外的值。
- **必填欄位**：任務 tool 的 input 直接填進 operation 的必填欄位（必填的 path／query 參數，或 body schema `required` 列的欄位，巢狀物件各看自己那層），而且 input 沒有 `default`／`x-default` 時，載入時自動補進該 tool 的 `inputs.required`；step 給固定值的欄位不產生 input，不受影響。缺必填的呼叫在引數檢查就被拒（一次列出所有缺的名字），不會產生 approval。`workflows draft` 的提示把 body 必填欄位標 `*`，要求照此起草。
- 引數名 `approvalId`、`approvalReason` 保留給 L3 核准：後者是 agent 給核准人看的一句話理由（選填、最多 300 字），不轉發、不算進核准綁定的參數。

呼叫時：gateway 先檢查引數（不認得的引數、缺必填、型別、`minimum`／`maximum`、`enum` 都在轉發前拒絕，回 `Next:`），補上 `default` 和 `x-default`，再依序跑每一步；每一步都照第一節「轉發前先記錄」。沒給的選填值不送：參數不帶、body 裡那個欄位拿掉；必填值來自前一步卻沒拿到（例如搜尋沒找到人）就停在那一步。

回傳是 envelope，放在 `structuredContent`，同一份 JSON 也是 `content` 的文字：

```json
{"status": "ok", "data": {}, "truncated": [{"path": "/data/paginationData", "kept": 120, "total": 500}], "next": ["get_player_profile"]}
{"status": "error", "error": {"step": "read", "call": "GET /api/customer/{customerId}", "httpStatus": 404,
  "reason": "...", "nextAction": "...", "detail": {}}}
```

- `ok` 沒有理由欄位；`error` 一定有 `reason` 和 `nextAction`，`isError` 為 true。`detail` 是舊系統的錯誤回應，和 L0、L1 的 tool 一樣先遮掉憑證。
- `data` 編成 JSON 超過 32 KiB 時，最長的列表從尾端砍半直到放得下，`truncated` 寫明哪個列表留了幾筆、原本幾筆；沒有列表可砍就換成 `{"partialJSON": "<前 32 KiB>"}`。

## 四、L3 核准流程

1. agent 呼叫要核准的 tool → gateway 先用參數組出請求；參數不對（缺必填、header 值有換行等）直接回參數錯誤，**不開核准編號**。參數對的呼叫 gateway **不轉發**，回 `NOT EXECUTED: needs approval` 和核准編號（`apr_...`）。
2. 人決定，管道由 `approvals.mode` 選（`elicitation` 或 `webhook`；兩種模式怎麼運作、取捨與設定見 [approvals.md](approvals.md)）。協定只規定結果：
   - 核准 → 轉發。elicitation 是這次呼叫立刻轉發、只轉發一次，不必重送；webhook 要 agent 重送（第 3 點）。核准人記成 `elicitation:<client 名稱>`（沒有就 `elicitation:mcp-user`）或 `web:<自填名稱>`。
   - 拒絕 → 不轉發，回「使用者拒絕，不要重試」。
   - 取消、逾時、client 不支援、無法詢問、通知沒送出 → 維持等待，不自動放行；回 agent 說明原因和下一步（不帶 `approvalId` 重新呼叫再問一次）。
   - 核准端點（`approvals.listen`）在任何模式都只能 `GET /approvals` 列出，不能核准或拒絕：agent 有 shell 就連得到它。程式內的宿主（benchmark runner，`Approvals.InProcess`）用 `DecidePending` 自己決定，不問使用者、不經頻道。
3. 需要重送時（維持等待後被核准、`webhook`），agent 用**完全相同的參數**加上 `approvalId` 再呼叫一次 → gateway 轉發，並帶 `X-Lodog-Approval` 給 proxy。
4. 一個核准編號最多放行一次呼叫，從 gateway 擋下呼叫、建立核准編號那一刻起算 30 分鐘後失效（不是從人核准的時間起算）；參數或 tool 不同、還沒核准、被拒絕、已用過、已失效，都不轉發，並告訴 agent 下一步。
5. 核准只在請求真的送出時才算用掉：帶 `approvalId` 的呼叫先被保留（同一編號的並行重送會收到 `approval is permitting another call right now`，不會再放行一次），送出後才標成用過；送出前就失敗（參數錯、gateway 登入上游失敗或連不到）時核准原封不動，agent 修正後可再用同一個編號。送出後才失敗（逾時、斷線）算用過，因為上游可能已經收到。
6. `tool call decided` 的 `forwarded=true` 只寫在 gateway 接著要送出的呼叫上；擋下、拒絕、登入上游失敗的呼叫都是 `forwarded=false`。參數錯的呼叫只有一行 `tool call refused`（`reason`）。`lodog approvals list`（`GET /approvals`）沒有待核准時回 `[]`。

任務 tool 的核准：整個 tool call 在第一步之前就被擋下（任何一步都不轉發），核准請求的 `method`、`path` 是決定這次核准的那一步（最嚴的那步），引數只靠輸入就能算出時 `path` 是實際路徑，摘要列出 tool 引數和那一步的 body。核准後同一次 tool call 的每一步都帶同一個 `X-Lodog-Approval`。
