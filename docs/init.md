# lodog init：接上你自己的系統

`lodog init` 在**你自己的 repo** 開好接新系統需要的檔案。不用 clone lodog repo，只要有 `lodog` 執行檔。

```bash
lodog init my-system --swagger https://my-system/swagger/v1/swagger.json
lodog init my-system --swagger ./swagger.json --with-bench   # 連題庫範例一起產生
```

| 參數 | 說明 |
|---|---|
| `<folder>` | 要建立的資料夾，必須還不存在；已存在就拒絕，不覆蓋你的檔案。最後一段是系統名稱 |
| `--swagger` | 系統的 API 文件，URL 或檔案路徑（必填）。Swagger 2.0、OpenAPI 3.0／3.1，JSON 或 YAML 都行 |
| `--upstream` | 系統的 base URL，要包含 API 的 base path（例如 `https://host/api/v3`）。沒給就用文件 `servers` 的第一筆（相對路徑以 `--swagger` 的 URL 為基準），文件沒有 `servers` 時用 `--swagger` 的 host。**這些都推不出來時（例如 `--swagger` 是本機檔、`servers` 是相對路徑）init 會失敗、不留任何檔案**，錯誤訊息會要你給 `--upstream` |
| `--auth` | 系統怎麼登入：`cookie-session`、`bearer`、`api-key`、`oauth2-client-credentials`（機器身份，用 client id／secret 換 token）。**不給就從文件的 `securitySchemes` 讀**，給了以它為準（欄位仍盡量取自文件）。選到的寫成有效設定，其他以註解附上；欄位意義見 [contracts.md](contracts.md) §2 |
| `--insecure` | 系統用自簽憑證（例如本機 `https://localhost:7144`）時加上：抓 `--swagger` 時不驗證憑證，並在每個 `gateway/L0–L3.yaml` 寫入 `upstream_insecure_tls: true`。只給本機開發用；不加時遇到自簽憑證，錯誤訊息會提示這個參數 |
| `--with-bench` | 另外產生題庫範例 |

## 產生什麼

```
my-system/
  case.yaml            系統說明：API 位置、登入方式
  spec/openapi.json    抓下來的文件，一律存成 OpenAPI 3.x JSON（2.0 和 YAML 會先轉換）
  gateway/L0.yaml      gateway 設定：每個 operation 原樣變成一個 tool
  gateway/L1.yaml      同上，套用 spec/overlay.yaml 的說明（要先 lodog enrich）
  gateway/L2.yaml      任務 tool（spec/workflows.arazzo.yaml，要先 lodog workflows draft），不強制執行
  gateway/L3.yaml      L2 的任務 tool（spec/workflows.arazzo.yaml，要先 lodog workflows draft）加上 spec/policy.yaml（要先 lodog policy draft）
  README.md            下一步怎麼做
  tasks.md             --with-bench：一題範例
  expected/answers.yaml --with-bench：一題範例（格式見 benchmark-task-types.md）
  rubric.yaml          --with-bench：評分權重和模型
```

每個設定檔的欄位都附註解；要你填的地方寫 `TODO`。帳號只寫環境變數名稱，不寫值。

## auth 怎麼從文件推出來

init 讀 `securitySchemes`，文件頂層 `security` 有指名的 scheme 排在前面，其餘依名稱排序，取第一個 lodog 能用的：

| 文件寫的 | 產生的 auth |
|---|---|
| `apiKey`，`in: header`／`query` | `api-key`，`header`／`query` 用文件的 `name` |
| `apiKey`，`in: cookie` | `cookie-session`（登入的呼叫文件看不出來，仍是 `TODO`） |
| `http`，`scheme: bearer` | `bearer`（固定 token；用登入換 token 的欄位以註解附上） |
| `oauth2` 有 `clientCredentials` | `oauth2-client-credentials`，`token_url` 和 `scope` 取自文件 |
| `http basic`、其他 `oauth2` flow、`openIdConnect` | 目前不支援，在註解裡列出並說明為什麼不能用 |

文件沒有 `securitySchemes`、或沒有一個能用，auth 是 `cookie-session`，檔裡的註解會明講這是猜的。推出的依據和其他 scheme 都寫在 `auth:` 區塊的註解裡，所以打開檔案就看得出每個欄位從哪來。

## 下一步

在**包含這個資料夾的目錄**（`gateway/L0.yaml` 的 `case` 是你給 init 的路徑，相對於執行 lodog 的目錄）：

```bash
LODOG_UPSTREAM_USER=... LODOG_UPSTREAM_PASSWORD=... lodog serve -c my-system/gateway/L0.yaml
```

agent 連 `http://127.0.0.1:7400/mcp`。

## 目前的限制

- request body 是 JSON、`application/x-www-form-urlencoded`、`multipart/form-data` 以外型別（XML、`application/octet-stream` 等）的 operation 不會變成 tool：`lodog init` 執行完就列出有哪些（`not served: N operation(s) ...`，方法、路徑、原因），`lodog score` 的報告最後也列同一份，`lodog serve` 啟動時每個印一行 `WARN operation not served`。表單 body（urlencoded、multipart，檔案以 base64 給）的引數形狀見 [contracts.md](contracts.md) §2。
- 設定檔裡還留著範本的 `TODO`（`auth.login`、`auth.token_url`、`identity.on_behalf_of`）時，`lodog serve` 一啟動就報錯並指出欄位，填好才起得來。
- L0–L3 四份設定一起產出，`auth`、`upstream` 四份相同；改的時候四份都要改。L1、L2、L3 的檔案頭有註解說它們需要什麼：
  - L2：`spec/overlay.yaml` 和 `spec/workflows.arazzo.yaml`（用 `lodog workflows draft --case <folder>` 從 overlay 草擬、人工審核），不強制執行。
  - L1：`spec/overlay.yaml`，用 `lodog enrich --case <folder>` 產生、人工審核。
  - L3：服務 L2 的任務 tool，所以要 L1 的 overlay、`spec/workflows.arazzo.yaml`（用 `lodog workflows draft --case <folder>` 從 overlay 草擬、人工審核），再加 `spec/policy.yaml`（用 `lodog policy draft <folder>/spec/openapi.json --case <folder> --out <folder>/spec/policy.yaml` 草擬、人工審核）。
  - 檔案不在時 `lodog serve` 起不來，錯誤訊息會寫要先跑哪個指令。
  - 各級差別見 [levels.md](levels.md)。
