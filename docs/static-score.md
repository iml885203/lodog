# 靜態分數：lodog score

只看文件、不跑 agent，評一個 API 對 agent 有多友善：一級總分 100 裡的 30 分。任何 Swagger 2.0、OpenAPI 3.0 或 3.1 的 JSON／YAML 都能跑，不用準備題庫或環境。

```bash
lodog score path/to/openapi.json              # 原始文件，agent 一支 API 一個 tool 看到的樣子
lodog score https://host/openapi.json         # 文件也可以直接給 URL
lodog score -c cases/backoffice/gateway/L3.yaml  # 某一級 gateway 交給 agent 的樣子
lodog score ... --json                        # 輸出 JSON
lodog score openapi.json --case cases/backoffice # 加上系統自己的詞表（見「詞表」）
lodog score ... --origin https://api.example.com  # 另外探測系統有沒有公開 agent 標準
```

`lodog bench` 評一級時會用那次 run 記下的 gateway 設定自動算靜態分，報告的 `points.static` 就是它，加進總分。

## 評什麼

八項，配分固定在 code 裡（`internal/readiness`），不同系統的分數才能比。

| 項目 | 分數 | 怎麼算 | 為什麼 |
|---|---|---|---|
| 說明覆蓋 | 6 | 有說明的 tool 比例、有說明的參數比例，各半 | agent 只能從說明判斷用哪支、怎麼帶參數 |
| Tool 數量 | 4 | 20 個以內滿分，超過按 `20 / 數量` 遞減 | tool 太多，agent 選錯的機率上升，context 也被吃掉 |
| 風險標記 | 5 | 會改資料的 tool 中，明確宣告了風險等級的比例；只落在 policy default 的不算 | 沒有標記，就分不出查詢和派彩 |
| GET 卻會改資料 | 3 | 名字是動作的 GET（reset、delete、cancel…；只回傳預覽、範例的不算）都被標了非 read 的風險或被擋下才給分，有一個沒處理就 0 分 | agent 會把 GET 當成安全的查詢 |
| Auth 宣告 | 2 | 有 `securitySchemes` 給一半，另一半看標了 security 的 tool 比例 | agent 要知道怎麼取得權限 |
| Agent 身份 | 2 | gateway 給 agent 自己的身份，並帶出代表的人 | 行為要追得到是誰、代表誰 |
| 錯誤回應 | 5 | 寫了 4xx／5xx（或 `default`）回應的 tool 比例 | 錯誤有說明，agent 才能自己修正 |
| 分頁一致 | 3 | 列表用同一組分頁參數的比例、寫了 page size 上限的比例，各半；沒有分頁列表就滿分（分頁參數怎麼認，見「詞表」） | 學會一個列表，就會翻所有列表 |

「會改資料」= 所有非 GET，加上名字是動作、且不是預覽的 GET（見下方詞表）。風險標記只算明確的宣告：operation 自己的 `x-agent.risk`，或 L3 policy 裡對到它的規則。對不到規則、落在 policy default 的不算宣告：default 讓每支沒對到的 API 都是同一個風險，分不出仔細寫的 policy 和一行的 policy。default 必須是 approval 以上，所以它仍然擋得住 GET 陷阱，「GET 卻會改資料」照樣看它；會改資料的呼叫有沒有被管住，由動態分數的安全分和 gate 量。

## L4：要改原系統 code 的清單

報告在八項之後多一段 L4：L1 到 L3 做不到、只能改原系統 code 的項目。lodog 只建議，改不改由系統的人決定。每項有三樣東西：

- **能多拿幾分**：對應上表的某一項，分數 = 該項滿分 − 現在得分，所以跟評分標準同步。已經拿滿的（例如 overlay 已宣告風險或 policy 擋住的 GET 陷阱、gateway 給的 agent 身份）不印「+0.00 分」，改放在報告最後的「靜態分不計、原系統仍建議改」區，和只在動態分看得到的項目（dry-run、idempotency key）一起，因為真正的修法仍在 code。GET 那項的說明分開計數：名字是動作的 GET、被 overlay 宣告成會改資料的 GET。
- **哪些端點**：最多列 10 個，其餘寫「還有 N 個」。
- **可直接貼給 coding agent 的指令**：寫明要改什麼、怎麼改、改完要同步什麼。

| L4 項目 | 對應 | 不用改 code 的做法 |
|---|---|---|
| 會改資料的 GET 改成 POST | 靜態：GET 卻會改資料（3） | L3 policy 擋下 |
| 錯誤回應結構化並寫進文件 | 靜態：錯誤回應（5） | 無 |
| 宣告驗證方式 | 靜態：Auth 宣告（2） | 無 |
| 給 agent 真正的機器身份 | 靜態：Agent 身份（2） | L3 gateway 的身份標籤 |
| 分頁參數一致並設上限 | 靜態：分頁一致（3） | 無 |
| 危險操作支援 dry-run | 動態：安全分與 gate，不在靜態分 | gateway 的核准 |
| 寫入操作支援 idempotency key | 動態：安全分與 gate，不在靜態分 | 無 |
| 補上 agent 需要、API 卻沒有的端點 | 不在靜態分（建議）；`lodog report` 附 run 的呼叫紀錄 | 無 |

dry-run 與 idempotency key 看文件裡的 query、header、cookie 參數（例如 `Idempotency-Key`）和 body 最上層欄位。

**API 缺的端點**：前面幾項都在改現有的端點，這一項指出該**新增**的 GET。agent 做完寫入要能讀回那一筆，確認狀態真的變了；找一筆紀錄要能用 id 直接查，不必翻分頁列表。沒有可量的靜態檢查，所以不加分，列在「靜態分不計」區。

- **寫入讀不回**：agent 拿到的每個會改資料、風險不是 read 的 operation（L2、L3 是任務 tool 背後的），在整份文件的 GET 裡找讀得回它的那一支（被 policy 藏起來的 GET 也算，它還在）：
  - 同一個路徑的 GET（`PUT /settings` 與 `GET /settings`）；
  - 路徑裡有 `{id}` 時，那一筆或它底下的 GET（`POST /orders/{id}/approve` 由 `GET /orders/{id}` 讀回）；那一層上面的集合連 GET 都沒有時，它只是動作的一部分（`…/moveAfter/{id}`），往上一筆找；
  - 路徑沒有 `{id}`（id 在 body）時，上面任一層集合的 `GET …/{id}`（`POST /operator/unlock` 由 `GET /operator/{id}` 讀回）；
  - 集合的列表沒有分頁參數也算讀得回，一次就看得到全部；有分頁的不算，agent 得翻列表找那一筆。
  - 比對時大小寫不分、字尾 s 不分（`/transaction/…` 和 `/transactions` 是同一個集合）。
- **建議的路徑**照文件自己的寫法推：路徑有 `{id}` 就是那一筆；否則是那個分頁列表底下一筆（`GET /api/payment/transactions/{id}`）；文件已經有 `…/{xId}` 的集合，用它（`POST /units` 旁有 `PATCH /units/{unitId}`）；都沒有就是去掉最後一段動作的 POST 路徑、或 PUT／PATCH／DELETE 路徑本身，加 `/{id}`。缺同一支 GET 的寫入合成一筆，L2、L3 另外列出哪些任務 tool 會走到這些寫入。
- **run 的證據**（只有 `lodog report`，有 run 時）：讀各級最後一次 run 的 `calls.jsonl`，只替文件已經確定缺的端點補證據：
  - 寫入成功（或 read-only 模式的 dry-run）之後，同一個 attempt 翻那個集合的分頁列表找回那一筆，或之後完全沒有讀取；
  - 同一個 attempt 對同一個分頁列表拿到 2 次以上成功回應，而文件沒有它底下（`/xs/{id}`）或旁邊（`/xs/search` 旁的 `/xs/{id}`）的單筆 GET：這時才會多出一支只有 run 證據的端點。錯誤後重試不算。
  - 文件有單筆 GET、agent 卻去翻列表，是 agent 自己的選擇，不算 API 缺（Keycloak 有 `GET /users/{id}`，所以 agent 用 `GET /users?search=` 找人不會列出）。
  - 「agent 說完成卻沒讀回」要讀 transcript 判斷，沒有可靠的判斷方式，所以不列。
  - 每支端點最多列 3 筆 run 證據；貼給 coding agent 的指令只列要做的端點，不帶 run 證據。

**公開的 agent 標準**（[reference-isitagentready.md](reference-isitagentready.md)）：MCP Server Card、OAuth Protected Resource、API Catalog。`lodog score` 只有兩種情況會連網：文件給的是 URL（下載它；自簽憑證加 `--insecure`），和給了 `--origin`（探測這些標準）；沒給 `--origin` 就顯示「未檢查」。回 200 且是 JSON 才算有，不計分。

## 詞表：哪些詞算動作、算金額

「名字是動作的 GET」和「碰到金額」都是從 path、operationId、summary 的字猜的。字照 camelCase 和標點切成單字，整個字比對（`block` 不算 `lock`），複數（s、es）算同一個字。

- **預設詞表是通用的**，只收任何後台都有的動作和金額詞：動作 reset、send、approve、cancel、delete、pay…；金額 payment、transfer、refund、payout、transaction、invoice、billing、balance、credit、debit…。完整清單在 `internal/readiness/signals.go`。
- **動作詞的決策原則：在 GET 上出現時，多數系統真的代表會改資料嗎？** 誤判的代價是把唯讀的 GET 當成危險（扣分、L4 多列一項），所以不夠確定的不收：
  - 只收動詞，且 GET 上出現幾乎都是真的在做事：reset、send、approve、cancel、delete、unlock…
  - 也是名詞的不收：lock、trigger、transfer、refund、payout、sync（`GET /locks`、`GET /refunds` 是列表；複數也算同一個字，所以名詞更容易誤中）。
  - 常常只是預覽或計算的不收：generate（Keycloak 的 `GET …/evaluate-scopes/generate-example-access-token` 是唯讀）。
  - 系統確實拿這些字當動作，就加進自己的 `case.yaml`。
  - 這只影響靜態分。**policy 草擬不共用這份判斷**：它對這些字和金額詞的 GET 一律不判 read、交給模型（[policy.md](policy.md)），因為分數誤報只是扣分，policy 誤放行是沒經核准的危險呼叫。金額詞不受此限，因為它只標記「碰到金額」，不判斷會不會改資料。
- **預覽詞讓動作詞不算數**：動作詞所在的那一段，或路徑最後一段，有 example、preview、sample、template，這支 GET 只是回傳「做了會怎樣」，靜態分和 L4 不算它會改資料。所以 case 把 generate 加成動作詞後，`GET …/reports/generate` 照樣被標，`…/generate-example-access-token`、`…/send/preview` 不會。預覽詞只看動作旁邊，不看整條路徑：`GET /templates/send` 還是會被標。這只影響靜態分和 L4：policy 草擬和 `lodog bench draft scan` 不看預覽詞，預覽的 GET 在那裡仍算動作。
- **業務詞放系統自己的 `case.yaml`**，加在預設詞表上，不取代：

  ```yaml
  signals:
    action_words: [publish]
    money_words: [deposit, withdraw, wallet, bonus, promotion]
    preview_words: [dryrun]
  ```

  每一個詞只能是一個字（字母和數字），否則載入時報錯。Backoffice 的金額詞就寫在 `cases/backoffice/case.yaml`。
- **要告訴 lodog 用哪個 case 的詞表**：`lodog score -c <gateway 設定>` 自動讀設定裡的 case；`lodog score <openapi>`、`lodog policy draft <openapi>`、`lodog bench draft scan` 要加 `--case <目錄>`，沒給就只用預設詞表。
- **分頁參數不是單字比對，是成對判斷**：
  - 單獨就算分頁：`page`、`pageSize`、`pageNumber`、`pageIndex`、`perPage`、`limit`、`offset`、`cursor`、`pageToken`、`startingAfter`…
  - 太常見的字（`first`、`max`、`size`、`count`、`skip`、`take`、`top`、`start`、`from`、`after`、`before`）單獨不算（`max` 可能是價格上限、`before` 可能是日期篩選），要同一支 API 有「位置」配「數量」才算：`first`+`max`（Keycloak）、`skip`+`take`、`$skip`+`$top`（OData）、`after`+`limit`、`page`+`size`。
  - 大小寫、`_`、`-`、開頭 `$` 都忽略（`per_page`、`page-size` 同 `perPage`、`pageSize`）。
  - 認不得的組合會讓列表被當成沒分頁，而「沒有分頁的列表」是滿分，所以要補齊；完整清單在 `internal/readiness/signals.go`。
- **宣告的風險優先於詞表。** operation 有 `x-agent.risk`（例如 L1 overlay 寫的）時，GET 是不是「會改資料」由宣告決定：宣告 read 就不算，宣告其他風險就算；詞表只猜沒宣告的。非 GET 永遠算會改資料，宣告 read 也一樣，gateway 對看不懂的 operation 一律當要核准。

## 各級怎麼評

| 級 | 評的東西 |
|---|---|
| 原始文件、L0 | OpenAPI 原樣，gateway 轉發得了的 operation 都是 tool |
| L1 | 原始文件套上 `spec/overlay.yaml`：說明、參數說明、`x-agent.risk`、錯誤回應都看 overlay 補過的樣子 |
| L2 | 任務 tool（`spec/workflows.arazzo.yaml`）：一個 workflow 算一個 tool，說明看 workflow 的 summary／description、參數看它的 inputs、風險看它的 `x-agent.risk`；「會改資料」「GET 陷阱」看它第一個會改資料的步驟（沒有就看最後一步）；錯誤回應要每一步的 operation 都寫了才算；L4 段列的是這些任務 tool 背後的 operation |
| L3 | L2 的任務 tool 加上 gateway 設定選的 policy（[policy.md](policy.md)）：有一步被 hide 的任務 tool 不算、風險標記與 GET 陷阱也看 policy、gateway 設定的 agent 身份算分 |

報告最後列出**不會變成 tool 的 operation**（方法、路徑、原因）：gateway 轉發不了的 body 型別，和 L3 被 policy hide 的。這份清單和 `lodog serve` 啟動時印的 `WARN operation not served` 是同一個函式算的（`gateway.Plan`），不算進上面的分數；`--json` 在 `skipped` 欄位。

L0 的 gateway 也會在紀錄裡帶 agent 身份，但那只是 benchmark 用來歸檔的標籤，沒有強制執行，所以不算分。

## Backoffice

| | 分數 | tool |
|---|---|---|
| 原始文件／L0 | 1.19 | 232 <!-- lodog-number: score cases/backoffice/spec/openapi.json static=1.19 tools=232 --><!-- lodog-number: score cases/backoffice/gateway/L0.yaml static=1.19 tools=232 --> |
| L1（`gateway/L1.yaml`） | 20.17 | 232 <!-- lodog-number: score cases/backoffice/gateway/L1.yaml static=20.17 tools=232 --> |
| L2（`gateway/L2.yaml`） | 24.83 | 25 <!-- lodog-number: score cases/backoffice/gateway/L2.yaml static=24.83 tools=25 --> |
| L3（`gateway/L3.yaml`） | 26.83 | 25 <!-- lodog-number: score cases/backoffice/gateway/L3.yaml static=26.83 tools=25 --> |

分數是 `lodog score -c <gateway 設定>` 在 repo 當下的 spec、overlay、workflows 上算的；spec 改了，分數就跟著變。

原始文件有 232 個 operation<!-- lodog-number: operations cases/backoffice/spec/openapi.json 232 -->，每個都是 tool，都沒有說明、沒有錯誤回應、沒有 auth 宣告；有 2 個名字是動作的 GET：`resetPassword`、`TestDelete`。`manualBonus/issue` 是報表，不是動作，所以不算。

L1 的 overlay 補了每支 API 和每個參數的說明、每支 API 的風險、錯誤回應（231／232 支有 4xx／5xx，只有 `logout` 沒有），說明覆蓋和風險標記拿滿分，錯誤回應 4.98；232 個 tool 讓 tool 數量只拿 0.34，分頁一致只拿 0.85（都沒寫 page size 上限）。

L2 是 `lodog workflows draft` 從 L1 的 overlay 草擬、審過的 25 個任務 tool（找玩家、玩家資料與錢包、改玩家資料、交易與注單、存提款與審核、紅利、CRM、停用帳號、公司報表）。tool 數量拿 3.20（超過 20 個），分頁一致拿 2.63（4 個列表裡 3 個用同一組分頁引數，4 個都有 page size 上限）。兩個 GET 陷阱沒有任何任務 tool 會呼叫。錯誤回應拿滿分：每個任務 tool 背後的 operation 都有 overlay 補的 4xx／5xx 回應（舊系統自己的文件仍是 0，要靠 L4 改 code）。Auth 宣告仍是 0。

L3 用和 L2 相同的 25 個任務 tool，多拿 agent 身份的 2 分。policy 是 `lodog policy draft` 原樣產出的 `spec/policy.auto.yaml`（lodog 的產品分數）；它沒有藏掉任何一個任務 tool，任務 tool 的風險又都由 workflow 的 `x-agent.risk` 宣告，所以換成別份 policy 靜態分也不會變，差別在核准行為，由動態分量。審核後的 policy 尚未產生（#36）。

L4 列出一支 API 缺的端點：L2、L3 的任務 tool 會走到 4 個存提款審核的寫入（`POST /api/payment/transaction/deposit/complete`、`…/deposit/reject`、`…/withdraw/process`、`…/withdraw/reject`），寫完沒有 GET 讀得回，只能翻分頁列表 `GET /api/payment/transactions` 找那一筆，所以建議 `GET /api/payment/transactions/{id}`。`lodog report --runs cases/backoffice/results` 另外附上 run 的證據。原始文件／L0、L1 看全部 232 支，還會列出其他缺讀回的寫入（遊戲設定、付款帳戶、轉帳等）。

## Keycloak

| | 分數 | tool |
|---|---|---|
| 原始文件／L0 | 11.71 | 413 <!-- lodog-number: score cases/keycloak/spec/openapi.json static=11.71 tools=413 --><!-- lodog-number: score cases/keycloak/gateway/L0.yaml static=11.71 tools=413 --> |
| L1（`gateway/L1.yaml`） | 20.69 | 413 <!-- lodog-number: score cases/keycloak/gateway/L1.yaml static=20.69 tools=413 --> |
| L2（`gateway/L2.yaml`） | 24.95 | 25 <!-- lodog-number: score cases/keycloak/gateway/L2.yaml static=24.95 tools=25 --> |
| L3（`gateway/L3.yaml`） | 27.39 | 22 <!-- lodog-number: score cases/keycloak/gateway/L3.yaml static=27.39 tools=22 --> |

Keycloak Admin REST API 有 415 個 operation<!-- lodog-number: operations cases/keycloak/spec/openapi.json 415 -->，2 個的 body 是 `text/plain`，gateway 不轉發，所以是 413 個 tool；Keycloak 自己的文件已經寫了部分說明和錯誤回應，L0 就比 Backoffice 高。L2、L3 用同一份 25 個任務 tool，L3 套上的 policy 把其中 3 個判成 `credential`（hide），剩 22 個。overlay、workflows、policy 都沒經人工審核（[cases/keycloak](../cases/keycloak/README.md)）。

Keycloak 的單筆 GET 很齊，L3 的 L4 只列出一支缺的端點（原始文件／L0 另有組織群組成員那一支）：把使用者加入、移出群組（`PUT`／`DELETE …/users/{user-id}/groups/{groupId}`）之後，只能翻分頁的 `GET …/users/{user-id}/groups` 確認，建議 `GET …/users/{user-id}/groups/{groupId}`。run 裡 agent 用 `GET /users?search=` 找人不算證據，因為 `GET /users/{id}` 存在。
