# Getting Started：把你自己的系統接上 lodog

照這份文件做，可以在自己的 OpenAPI（swagger）上走完一輪：量分數、開設定、補說明、起 gateway、讓 Claude Code 接上去；再往下是危險操作要人核准（L3）和用題庫跑 benchmark。不用改你系統的任何一行 code。

範例用公開的 [Petstore 3](https://petstore3.swagger.io/)，每一步的輸出都是真的跑出來的（長的輸出有節錄）。Petstore 的線上文件會變，你跑出來的數字可能略有不同。換成你的系統時，只要換 swagger 的位置和登入方式。

需要：macOS 或 Linux、[Claude Code](https://claude.com/claude-code)（`claude` 在 PATH 裡，`enrich`、`policy draft`、`bench` 和接上 agent 都要用）、你系統的 swagger（URL 或檔案）。

## 讓自己的 agent 帶著做

不想自己跑指令的話，下載 lodog（第 1 步）後，在要放設定的資料夾裝上接入 skill，然後對 Claude Code 說「幫我把這個系統接上 lodog」：

```bash
lodog skill install            # 寫到 ./.claude/skills/lodog-onboard/SKILL.md
lodog skill install --global   # 或寫到 ~/.claude/skills/，每個專案都能用
```

skill 包在 lodog 執行檔裡，不用 clone repo；升級 lodog 後再裝一次（改過的 SKILL.md 不會被覆蓋，要換加 `--force`）。`lodog skill show` 印出內容。agent 會照這份文件的步驟自己跑指令，只在這幾個地方停下來問你：swagger 從哪來、用哪個帳號登入（密碼由你自己在終端機 `export`，agent 看不到）、`lodog review` 整理出的風險與任務 tool 問題、誰是核准人、要不要起 gateway。每一步結束回報幾行：做了什麼、分數變化、需要你決定什麼。

## 1. 下載 lodog

[Releases 頁面](https://github.com/iml885203/lodog/releases) 提供已發版的執行檔；這是 private repo，下載需要 GitHub 權限。尚未發版時可 clone 後執行 `make build`，使用 `bin/lodog`。

```bash
gh release download <vX.Y.Z> --repo iml885203/lodog \
  --pattern lodog_darwin_arm64 --pattern checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing     # lodog_darwin_arm64: OK
chmod +x lodog_darwin_arm64 && mv lodog_darwin_arm64 /usr/local/bin/lodog
lodog --version
```

預期輸出（版本號是你下載的那一版）：

```
lodog version vX.Y.Z
```

| 常見錯誤 | 原因與處理 |
|---|---|
| 用 `curl` 下載的檔案是 HTML（`lodog: line 1: syntax error near unexpected token 'newline'`） | 沒登入，下載到的是登入頁；用瀏覽器或 `gh release download` |
| macOS 執行後立刻被結束（`killed`，exit 137）或跳出「無法驗證開發者」 | 瀏覽器下載的檔案帶隔離標記，執行檔沒有簽章；`xattr -d com.apple.quarantine /usr/local/bin/lodog` |
| `command not found: lodog` | 執行檔不在 PATH；確認放的資料夾在 `$PATH` 裡 |

## 2. 先看自己的系統現在幾分

```bash
curl -sSf https://petstore3.swagger.io/api/v3/openapi.json -o petstore3.json
lodog score petstore3.json
```

swagger 可以是檔案路徑，也可以直接給 URL，例如 `lodog score https://petstore3.swagger.io/api/v3/openapi.json`。

預期輸出（節錄）：

```
靜態分數 22.24 / 30（petstore3.json，18 個 tool）

 5.80 / 6  說明覆蓋：18/18 個 tool 有說明，14/15 個參數有說明
 4.00 / 4  Tool 數量：agent 看到 18 個 tool；20 個以內拿滿分
 0.00 / 5  風險標記：0/10 個會改資料的 tool 明確宣告了風險等級
    - POST /pet
    - PUT /pet
    - DELETE /pet/{petId}
    - …
 3.00 / 3  GET 卻會改資料：…
 1.44 / 2  Auth 宣告：securitySchemes：api_key, petstore_auth；8/18 個 tool 標了 security
 0.00 / 2  Agent 身份：呼叫帶不出是哪個 agent、代表哪個人
 …

L4：要改原系統 code 才拿得到的（靜態分最多再 +2.56）
不會變成 tool 的 operation（1 個，不在上面的分數裡）：
    - POST /pet/{petId}/uploadImage：application/octet-stream bodies are not forwarded

+0.56 分  宣告驗證方式（靜態：Auth 宣告）
    貼給 coding agent：
      請在 OpenAPI 的 components.securitySchemes 寫出系統實際使用的驗證方式…
```

怎麼讀：每一行是「得分 / 滿分」加上原因，扣分的項目會列出是哪些 operation。最下面的 L4 是要改原系統才拿得到的分，附上可以直接貼給 coding agent 的指示；lodog 只建議，改不改由你決定。每一項的算法見 [static-score.md](static-score.md)。

Petstore 有 19 個 operation，這裡只算 18 個 tool：報告最後列出的圖片上傳（`POST /pet/{petId}/uploadImage`）gateway 不轉發，哪些 body 轉發、哪些不轉發見 [contracts.md](contracts.md) §2。

這就是你的 baseline：後面每補一層，回來用同一個指令再量一次。

| 常見錯誤 | 原因與處理 |
|---|---|
| `lodog: read OpenAPI document: open nope.json: no such file or directory` | 路徑打錯 |
| `lodog: fetch OpenAPI document https://…: 404 Not Found` | URL 錯了；到 swagger UI 頁面找「/v3/api-docs」或「openapi.json」的連結 |
| 文件是 Swagger 2.0 或 YAML | 都支援，不用先轉 |

## 3. 在你的 repo 開好設定檔

在**要放設定的資料夾的上一層**執行（`init` 不會覆蓋任何檔案，資料夾必須還不存在）：

```bash
lodog init petstore --swagger petstore3.json --upstream https://petstore3.swagger.io/api/v3
```

預期輸出：

```
upstream: https://petstore3.swagger.io/api/v3
auth.type api-key is read from securitySchemes.api_key (apiKey in header api_key).
not served: 1 operation(s) will not become tools
  POST /pet/{petId}/uploadImage: application/octet-stream bodies are not forwarded
created petstore
next: read petstore/README.md, fill in the TODOs in petstore/gateway/L0.yaml, then
  lodog serve -c petstore/gateway/L0.yaml
```

參數：

- `--swagger`：文件的 URL 或檔案。
- `--upstream`：系統的 base URL，要包含 API 的 base path（這裡是 `/api/v3`），因為文件裡的路徑是相對於它的。文件的 `servers` 是絕對網址、或 `--swagger` 是 URL 時 init 會自己推；這裡 `--swagger` 是本機檔案、`servers` 又是相對路徑，推不出來，init 會失敗並要你給 `--upstream`（不給也不會留下半成品）。
- `--auth`：可以不給，init 從文件的 `securitySchemes` 讀（Petstore 是 `apiKey` 放在 header `api_key`，所以直接寫成 `api-key` + `header: api_key`）。推不出來才會是 `cookie-session`，檔裡的註解會說這是猜的。要指定時四選一：`cookie-session`、`bearer`、`api-key`、`oauth2-client-credentials`。欄位說明在 [init.md](init.md) 和 [contracts.md](contracts.md) §2。

產生的東西：

```
petstore/
  case.yaml            系統說明
  spec/openapi.json    抓下來的文件
  gateway/L0.yaml      gateway 設定：文件原樣
  gateway/L1.yaml      同上，套用 overlay（步驟 4 補完說明後才起得來）
  gateway/L2.yaml      任務 tool（步驟 6 草擬、審核 workflows 後才起得來）
  gateway/L3.yaml      L2 的任務 tool 加上 policy（步驟 6、7 草擬、審核 workflows 和 policy 後才起得來）
  README.md
```

接著打開 `petstore/gateway/L0.yaml`，確認 `auth` 區塊：欄位是從文件讀出來的，註解寫了依據（`securitySchemes.api_key`）和文件裡其他沒用到的 scheme（Petstore 的 `petstore_auth` 是 oauth2 implicit，要人登入，lodog 用不了，所以不選它）。再把 `identity.on_behalf_of` 的 `TODO` 換成這個 gateway 代表的人：

```yaml
identity:
  actor: agent:lodog
  on_behalf_of: user:me
```

`L1.yaml`、`L2.yaml`、`L3.yaml` 的 `auth`、`upstream`、`identity` 和 L0 相同，四份一起改。level 不能跳：缺任何一級，lodog 都不會載入這個 `gateway/` 目錄。

帳號、密碼、key 一律只寫**環境變數的名稱**，值在執行時給，不會寫進任何檔案。

| 常見錯誤 | 原因與處理 |
|---|---|
| `lodog: target already exists: petstore (init never overwrites your files)` | 資料夾已存在；換名字，或自己刪掉再 init |
| `lodog: fetch OpenAPI document: … x509: certificate signed by unknown authority (… add --insecure)` | 系統用自簽憑證；`lodog init` 加 `--insecure`（只給本機開發用），產生的設定會帶 `upstream_insecure_tls: true` |
| `lodog: cannot tell where the system runs: …` | 文件沒有可用的 `servers`、`--swagger` 又不是 URL；加上 `--upstream <系統的 base URL>`。init 失敗時不會留下資料夾 |
| 登入方式不在四種裡 | 先選最接近的，照 [contracts.md](contracts.md) §2 手改設定；`header`、`prefix`、`success.cookie_name` 可以調整 |

## 4. 補說明，人工審核

`gateway/L0.yaml` 是 L0：文件原樣變成 tool，agent 只看得到文件裡有的說明。`enrich` 請 Claude 讀文件，替每個 operation 補上用途、何時用、何時不要用、參數意義和風險的初步判斷，寫成 overlay（L1）：

```bash
lodog enrich --case petstore
```

預期輸出（Petstore 19 個 operation 約 20 秒；模型每次的說法不同，數字也會略有差異）：

```
wrote petstore/spec/overlay.yaml: 36 actions for 19 operations in 23s
18 operations need review (x-agent.needsReview):
  POST /pet: Spec does not mark body fields required; name and photoUrls being required is inferred from the usual Petstore convention. …
  DELETE /pet/{petId}: Deletion being permanent is inferred from the HTTP method. Error codes are inferred from the Petstore convention.
  …
```

- 模型偶爾會漏回某個 operation：輸出會多一行 `missing: PUT /pet`（列出沒補到的）。把那幾個手動補進 `petstore/spec/overlay.yaml`（照其他項目的格式），或加 `--force` 重跑一次；這次示範的輸出沒有 `missing`，所以上面沒有那一行。
- `needsReview` 是模型沒把握的地方，**一定要有人看**：打開 `petstore/spec/overlay.yaml`，確認 `description`、`risk`（`read`／`write`／`irreversible`…）對不對，確認後刪掉該項的 `needsReview: true`。不想讀 YAML 時，`lodog review ask --case petstore --html review.html` 把待審項目整理成少數是非題，回答後 `lodog review apply` 寫回（[review.md](review.md)）。
- `missing:` 列出這次沒補到的 operation（模型漏回）。已有 overlay 時 `enrich` 不會覆蓋（怕蓋掉你審核過的修改），要重做才加 `--force`。
- 能讓模型讀系統的 code 來確認意思時，加 `--source <原始碼資料夾>`，needsReview 會少很多。

overlay 審核完，用 init 一起產好的 `gateway/L1.yaml` 套用它（沒有 overlay 時 L1 起不來，錯誤訊息會叫你先跑 `lodog enrich`）：

```bash
lodog score -c petstore/gateway/L1.yaml
```

預期輸出（節錄）：

```
靜態分數 27.44 / 30（petstore/gateway/L1.yaml，L1，18 個 tool）

 6.00 / 6  說明覆蓋：18/18 個 tool 有說明，14/14 個參數有說明
 4.00 / 4  Tool 數量：agent 看到 18 個 tool；20 個以內拿滿分
 5.00 / 5  風險標記：12/12 個會改資料的 tool 明確宣告了風險等級
 …
```

22.24 到 27.44：這就是補說明的效果。如果輸出有 `missing:`（模型漏回的 operation）或風險標記沒拿滿，手動補進 overlay，或 `enrich --force` 重跑。

| 常見錯誤 | 原因與處理 |
|---|---|
| `lodog: …/spec/overlay.yaml exists and may hold reviewed edits; pass --force to overwrite` | 已有 overlay；確定要重做才加 `--force` |
| `lodog: batch pet: /nonexistent: command not found; is it installed and on PATH, or the path spelled right? (…)`（每一批各一行：`batch pet`、`batch store`…） | 找不到 `claude`；裝好 Claude Code 讓它在 PATH 裡，或用 `--claude <路徑>` 指定 |
| 某幾批失敗、輸出有 `missing:` | 模型偶爾漏回，重跑 `--force`，或手動補那幾個 operation |

## 5. 起 gateway，讓 Claude Code 接上去

```bash
export LODOG_UPSTREAM_API_KEY=special-key     # 你系統真正的 key／帳密
lodog serve -c petstore/gateway/L1.yaml
```

要在**包含 `petstore/` 的資料夾**執行，不是進到 `petstore/` 裡面（設定裡的路徑相對於你執行的位置）。

預期輸出：

```
level=INFO msg="mcp serving" addr=127.0.0.1:7400
level=INFO msg="gateway ready" mcp=http://127.0.0.1:7400/mcp level=L1 tools=18 skipped=1
```

前面還有一行 `level=WARN msg="operation not served" ...`：`skipped=1` 就是上面說的圖片上傳，和 `lodog score`、`lodog init` 列出的是同一個。

在另一個終端機，把它加進 Claude Code：

```bash
claude mcp add --transport http petstore http://127.0.0.1:7400/mcp
claude -p "用 petstore 的工具查 pet id 1 的資料，用一句話回答"
```

預期輸出：

```
Pet id 1 叫 `Pet1`，狀態 `available`，沒有 tags，photoUrls 是 `test1` 和 `test2`。
```

同時 gateway 的終端機會印出這次呼叫（`tool call forwarding`、`tool call`，帶 `actor` 和 `on_behalf_of`）。

到這裡，agent 已經透過 gateway 在用你的系統了。

| 常見錯誤 | 原因與處理 |
|---|---|
| `lodog: environment variable LODOG_UPSTREAM_API_KEY (auth.key_env) is empty` | 設定裡 `*_env` 指的環境變數沒設；錯誤訊息會指名是哪一個 |
| `lodog: level L1 needs petstore/spec/overlay.yaml, which does not exist: …`（跑 L1 時） | 兩種原因：工作目錄不對（要在包含 `petstore/` 的資料夾執行），或還沒跑 `lodog enrich`。先確認目前所在的資料夾，再看有沒有 `petstore/spec/overlay.yaml` |
| `lodog: read openapi document: open petstore/spec/openapi.json: no such file or directory (case "petstore" is relative to the directory lodog runs in: …)`（跑 L0 時） | 工作目錄不對；回到包含 `petstore/` 的資料夾執行 |
| `lodog: listen 127.0.0.1:7400: … bind: address already in use` | 7400 已被別的程序占用；把設定檔的 `listen` 換成空的 port（L3 的 `approvals.listen` 同理），`claude mcp add` 的網址跟著改，`lodog approvals` 加 `--admin http://<approvals.listen>` |
| 呼叫回 401／403 | 帳密或 key 不對、`header` 名稱不對。登入被拒時 gateway **不會重試**（避免鎖帳號），修好之後要重啟 |
| Claude Code 看不到 tool | `claude mcp list` 確認 `petstore` 是 connected；gateway 要保持執行 |
| 服務在 `https://localhost` 用自簽憑證 | `lodog init` 加 `--insecure` 會直接寫好 `upstream_insecure_tls: true`；已產生的設定手動改成 `true`（只給本機開發用） |

## 6. 收斂成任務 tool（L2）

L1 是一支 API 一個 tool，agent 要自己猜一件事該組合哪幾支。L2 把 API 收斂成約 20 個「任務」（找 pet、列出某狀態的 pet、下單、刪 pet…），每個任務 tool 背後是一到幾支 API，寫成 Arazzo workflow。workflows 由 lodog 從 overlay 草擬，**人審核**後才用：

```bash
lodog workflows draft --case petstore --source petstore
```

預期輸出（約 1 分鐘；模型每次分的任務不同）：

```
wrote petstore/spec/workflows.arazzo.yaml: 16 task tools, 5 need review
  review: add_pet_image
  review: delete_pet
  review: place_order
  review: create_user
  review: update_user
```

- `--source` 是 workflows 裡指向你的 OpenAPI 文件的名字，取一個短名就好。
- **審核**：打開 `petstore/spec/workflows.arazzo.yaml`。檔頭的 `info.description` 是 agent 一連上就讀到的地圖（MCP server instructions）；每個 workflow 看 `summary`、`whenToUse`／`whenNotToUse`、`inputs`（引數名稱、說明、`default`、`maximum`）和每一步固定帶的值。`needsReview: true` 是模型沒把握的，確認後刪掉。格式見 [contracts.md](contracts.md) §3。
- 檔案在 gateway 啟動時整份對著文件檢查：步驟指到不存在的 API、漏了必填參數、取回應裡不存在的欄位，都會拒絕啟動並列出每一處。
- 已有檔案時不會覆蓋（怕蓋掉你審核過的修改），要重做才加 `--force`。
- 草擬只把 gateway 會服務的 operation 給模型（和 `lodog score` 列的「不服務」是同一份判斷）。用到不服務的 operation 的 workflow 會先退回修一次，再不行就丟掉並列在 `dropped:`。自己手寫的 workflow 若用到，`lodog serve` 和 `lodog score` 會跳過它並 WARN 指出哪一步，其他 workflow 照常服務。

用 init 一起產好的 `gateway/L2.yaml`：

```bash
lodog score -c petstore/gateway/L2.yaml
```

預期輸出（節錄）：

```
靜態分數 27.53 / 30（petstore/gateway/L2.yaml，L2，15 個 tool）

 6.00 / 6  說明覆蓋：15/15 個 tool 有說明，42/42 個參數有說明
 4.00 / 4  Tool 數量：agent 看到 15 個 tool；20 個以內拿滿分
 5.00 / 5  風險標記：9/9 個會改資料的 tool 明確宣告了風險等級
 …
```

Petstore 本來就只有 18 個 tool，L2 的分數和 L1 差不多；API 有幾百支時差別才大（Backoffice 的例子見 [static-score.md](static-score.md)）。任務 tool 的回傳是固定格式的 envelope，見 [contracts.md](contracts.md) §3。

| 常見錯誤 | 原因與處理 |
|---|---|
| `lodog: …/spec/workflows.arazzo.yaml exists and may hold reviewed edits; pass --force to overwrite` | 已有 workflows；確定要重做才加 `--force` |
| ``level L2 needs the task tools petstore/spec/workflows.arazzo.yaml, which do not exist: draft them with `lodog workflows draft --case petstore` ...`` | 還沒草擬 workflows；照錯誤訊息的指令草擬、審核 |
| `workflows petstore/spec/workflows.arazzo.yaml: workflow "…": step 1 "…": …` | 審核時改壞了；訊息列出每一處對不上的地方，照著修 |

## 7. 危險操作要人核准（L3）

L1 的 agent 看得懂 API，但任何寫入都會直接送到系統。L3 的 gateway 依 policy 決定每支 operation 放行、等人核准、拒絕或不給 agent 看。policy 由 lodog 草擬，**系統的維運或風險 owner 審核**後才用。

```bash
lodog policy draft petstore/spec/openapi.json --case petstore --out petstore/spec/policy.yaml
```

預期輸出（stderr，約 10 秒）：

```
19 operations: 11 decided by heuristics, 8 by the model, 2 need review
  irreversible    3  -> approval, never auto-approved
  money           1  -> approval
  credential      3  -> hide
  write           4  -> approval
  internal        0  -> hide
  test            0  -> hide
  read            8  -> allow
```

`never auto-approved` 的意思與等級集合（`--risks` 換成系統自己的分類）見 [policy.md](policy.md)。

**審核**：打開 `petstore/spec/policy.yaml`。每支 operation 一條規則，依風險分組，`why` 寫出是規則還是模型判斷的。要看的是排在前面的風險（`irreversible`、`money`、`credential`）的每一條、每一條 `needs review`、被 hide 但營運真的會用的、標成 `write` 但其實只是查詢的 POST，改好存回同一個檔；或用 `lodog review` 只回答是非題（[review.md](review.md)）。完整的審核清單見 [policy.md](policy.md)。

init 產好的 `gateway/L3.yaml` 服務第 6 步的任務 tool（也套用第 4 步的 overlay），用這份 `spec/policy.yaml` 決定每個任務 tool：一個任務 tool 依它每一步呼叫的 API 中最嚴的那個決定，有一步被 hide 就整個不給 agent 看。核准端點在 `approvals.listen`（`127.0.0.1:7402`，和 MCP 不同 port，agent 連不到它）。

```bash
lodog score -c petstore/gateway/L3.yaml
```

預期輸出（節錄）：

```
靜態分數 29.62 / 30（petstore/gateway/L3.yaml，L3，13 個 tool）
 ...
 5.00 / 5  風險標記：7/7 個會改資料的 tool 明確宣告了風險等級；0 個只落在 policy 的 default，不算宣告
 2.00 / 2  Agent 身份：gateway 給 agent 自己的身份，並帶出代表的人
```

起 L3 的 gateway：

```bash
lodog serve -c petstore/gateway/L3.yaml
```

```
level=INFO msg="gateway ready" mcp=http://127.0.0.1:7400/mcp level=L3 tools=13 skipped=2
```

`skipped=2` 是被 policy hide 的兩個任務 tool（`create_user`、`update_user`，`credential`），啟動時各有一行 `WARN operation not served` 說明原因。

用互動模式的 Claude Code（v2.1.76 以上）連上這個 gateway，叫 agent 做一件要核准的事（例如「用 petstore 的工具刪掉 pet id 990013」）。gateway 擋下這個呼叫，Claude Code 跳出確認框：誰要做什麼、對象、風險、完整參數。按核准，這次呼叫才轉發，而且只轉發一次；按拒絕，agent 收到「使用者拒絕，不要重試」，日誌記下是誰核准的：

```
level=INFO msg="approval decided" id=apr_db849fe90ee1e6f1 status=approved by=elicitation:claude-code tool=delete_pet
```

沒人能核准時的行為、兩種核准模式（使用者的確認框、Slack webhook）怎麼選和設定，見 [approvals.md](approvals.md)；`lodog approvals list` 列出等待中的呼叫，但不能核准。核准編號的規則（只能用一次、30 分鐘失效）見 [contracts.md](contracts.md) §4。

| 常見錯誤 | 原因與處理 |
|---|---|
| ``level L3 needs petstore/spec/overlay.yaml, which does not exist: write it with `lodog enrich --case petstore` ...`` | L3 也套 overlay，先做第 4 步 |
| ``level L3 needs the policy petstore/spec/policy.yaml, which does not exist: draft it with `lodog policy draft ...` `` | 還沒草擬 policy；照錯誤訊息的指令草擬、審核 |
| ``level L3 needs the task tools petstore/spec/workflows.arazzo.yaml, which do not exist: ...`` | L3 服務的是 L2 的任務 tool，先做第 6 步 |
| `NOT EXECUTED: approval … cannot be used: approval was already used.` | 一個核准只放行一次呼叫，再做一次要重新核准 |
| `argument "requestBody" is required`（沒有核准編號） | 參數不對，gateway 不開核准；照 tool 的 input schema 修正參數再呼叫。已核准的編號不會因此用掉 |
| `reach the approvals endpoint: ... connect: connection refused` | gateway 沒在跑，或 `approvals.listen` 不是預設的 `127.0.0.1:7402`；用 `--admin http://<listen>` 指定 |

### 接到另一個系統（同一份設定）

在本機用 benchmark 證明 L3 安全後，同一份設定可以接到真系統（例如 stg），不用改檔：

```bash
lodog serve -c cases/backoffice/gateway/L3.yaml \
  --upstream https://stg.example.com --listen 127.0.0.1:7460 --approvals-listen 127.0.0.1:7462
```

`--upstream`、`--listen`、`--approvals-listen`、`--actor`、`--on-behalf-of` 只換設定檔裡對應的值（後兩個是核准人看到的「誰、代表誰」），結果照樣過同一份驗證（L3 的 `approvals.listen` 不能等於 `listen`）；沒帶的維持設定檔的值。帳密仍從設定檔指定的環境變數讀。接到真系統前，確認兩邊的 OpenAPI 文件一致（缺的 operation 不會有 tool；多的不會被服務）。

## 8.（選）用題庫量前後分數

靜態分數只看文件。要證明 agent 真的做得到、做得對，寫幾題你系統的真實任務，用 `lodog bench` 跑：同一套題目，每一級只換 gateway 設定，把各級的分數並排。規則、旗標、報告欄位和只讀模式都在 [benchmark.md](benchmark.md)，流程圖見 [benchmark-flow.md](benchmark-flow.md)。

需要：系統跑得起來、**一行指令就能把測試資料重置回固定狀態**（每次嘗試前執行）。系統只有共用環境（例如 stg）、不能重置時，用 [benchmark.md](benchmark.md)「只讀模式與錄製重播」。

### 寫題庫

`lodog init ... --with-bench` 會多產生 `tasks.md`、`expected/answers.yaml`（一題範例）和 `rubric.yaml`。題庫用 Claude Code 的 `/lodog-bench` skill 一步一步帶著寫（[SKILL.md](../.claude/skills/lodog-bench/SKILL.md)）：它放在 lodog repo 的 `.claude/skills/lodog-bench/`，要在自己的 repo 用，把這個資料夾複製到你 repo 的 `.claude/skills/`。skill 會呼叫這些指令，人負責挑選和確認：

```bash
lodog bench draft scan --spec petstore/spec/openapi.json --case petstore   # 每種題型的候選 API、所有可能改資料的 API
lodog bench draft record --case petstore -c petstore/gateway/L0.yaml       # 重置資料、只打 GET、存到 expected/raw/
lodog bench check --case petstore                                          # 檢查題庫格式
lodog bench check --case petstore -c petstore/gateway/L0.yaml              # 重置後重打一次，確認答案沒過期
```

- 八種題型、`answers.yaml` 的每個欄位見 [benchmark-task-types.md](benchmark-task-types.md)。`bench check` 對還沒出題的題型給 warning，error 才要修。
- **危險呼叫清單（`must_not_call_without_approval`）由懂系統的人逐條決定**，不要照抄 `draft scan` 或第 7 步的 policy：清單和 policy 出自同一個判斷，L3 就是拿自己的答案考自己。
- `case.yaml` 的 `benchmark.reset` 填重置資料的那一行指令（不經 shell，複雜的寫成 script）；`versions` 釘住題庫是對哪個版本寫的，至少釘一項 `lodog bench` 才肯跑（[benchmark.md](benchmark.md)「版本檢查」）：

  ```yaml
  versions:
    - name: api.version
      expect: "1.0.27"
      observe: ./petstore/observe.sh       # 印出正在跑的版本
  ```

### 跑

benchmark 只信錄影 proxy 記下的呼叫，所以要跑的那一級設定的 `upstream` 要改成 proxy（`http://127.0.0.1:7401`），由 proxy 轉到系統：

```bash
lodog bench proxy --upstream https://petstore3.swagger.io/api/v3    # 先起好；本機自簽憑證加 --insecure
lodog bench --case petstore -c petstore/gateway/L0.yaml --attempts 1 --tasks T01
```

先 `--attempts 1`、少數幾題試跑；換一級只換 `-c`。`--attempts`、`--min-attempts`、`--parallel` 的意思見 [benchmark.md](benchmark.md)。各級並排成一頁 HTML：

```bash
lodog report --case petstore -o report.html
```

| 常見錯誤 | 原因與處理 |
|---|---|
| `preflight failed: case.yaml has no benchmark.reset; add one, or pass --no-reset` | 沒填 `benchmark.reset`。`--no-reset` 只適合試跑：寫入題會受前一次嘗試影響 |
| `preflight failed: case.yaml has no versions; ...` | 沒釘版本。沒有略過檢查的旗標 |
| `preflight failed: the running system does not match case.yaml versions; ...` | 正在跑的系統不是釘住的版本：把系統換回那個版本，或確認答案後改 `expect` |
| `upstream … unreachable (is the recording proxy running?)` | proxy 沒起，或設定的 `upstream` 沒指向它 |
| 開跑前的登入失敗 | runner 先登入一次，失敗就整個不跑，不重試（避免鎖帳號）；修好帳密再跑 |

## 下一步

- 各等級的差別：[levels.md](levels.md)。
- 依使用者區分的身份尚未支援：gateway 用一個帳號登入系統，agent 和它代表的人是設定裡固定的標籤（[levels.md](levels.md)）。
