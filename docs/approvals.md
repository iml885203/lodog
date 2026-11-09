# 核准：把「agent 想做什麼」交給人

L3 gateway 擋下危險呼叫後，要由**人**決定。這份文件講兩種核准模式（elicitation、Slack webhook）怎麼運作、怎麼取捨、怎麼設定。流程本身（核准編號、只放行一次、30 分鐘失效）見 [contracts.md](contracts.md) 第四節。

## 為什麼核准不能走 agent 碰得到的地方

agent（例如 Claude Code）通常和 gateway 在同一台機器上，有 shell：它可以 `curl localhost`、讀檔案、執行 `lodog` 指令。**只要核准的入口在 agent 碰得到的地方，agent 就能自己核准自己**，核准就沒有意義。

| 管道 | 能不能當核准管道 | 原因 |
|---|---|---|
| MCP elicitation（使用者在 client 的確認框） | 能防誤操作；防不了有 shell 的 agent | 問題由 gateway 直接送到 client 的介面，經由 Claude Code 的模型看不到也答不了；但 MCP 端點沒有身份驗證，同機的 agent 可以自己寫 client 去答，見「已知弱點」 |
| Slack webhook 的一次性連結 | 有條件 | 只有收到訊息的人拿得到連結；agent 讀得到那個頻道（例如有 Slack MCP）就拿得到，隔離就破了 |
| 在 agent 的對話框回「同意」 | 否 | 那是 agent 自己的輸入 |
| 核准端點（`approvals.listen`） | 否 | agent 有 shell 就連得到，身份也沒有憑證可驗 |
| 本機網頁＋密碼檔 | 否 | agent 讀得到檔案 |

所以**核准端點只能列出**：`GET /approvals`（`lodog approvals list`）列出等待中的請求，沒有核准或拒絕的路由；核准只走所選模式自己的管道。benchmark runner 在程序內啟動 gateway（`Approvals.InProcess`），不問使用者、不經頻道，用 `DecidePending` 扮演核准人，並且一律拒絕標了 `never_auto_approve` 的風險。

## 模式與取捨

`approvals.mode` 選誰核准、走哪個管道，預設 `elicitation`：

| | `elicitation`（預設） | `webhook` |
|---|---|---|
| 誰核准 | **使用者本人**，在自己的 MCP client 跳出的確認框按核准或拒絕 | 收到 Slack 連結的人 |
| 設定 | 不用設 | 一個 Slack incoming webhook 網址、一個核准頁位址 |
| 身份 | `elicitation:<client 名稱>`（client 沒給就 `elicitation:mcp-user`）：使用者本人確認，不是驗證過的帳號 | 核准頁上自填的名稱（`web:<名稱>`），**弱身份**：gateway 不驗證，能打開連結的人就能核准 |
| 隔離靠什麼 | 問題從 gateway 送到 client 介面，經由 Claude Code 的模型答不了；**擋不住自己寫 MCP client 的 agent** | agent 讀不到核准頻道 |
| 適合 | 使用者信任自己的 agent，要的是防誤操作 | 核准人不是使用者，demo、小團隊 |

### elicitation

gateway 擋下要核准的呼叫時，對 client 發 `elicitation/create`（form，不需要人填欄位），內容是誰、要做什麼、對象、風險、agent 的理由、完整參數。使用者按：

- **核准**：這次呼叫立刻轉發，只轉發一次（沿用 Reserve／Commit），核准人記成 `elicitation:<client>`；agent 不用重送。
- **拒絕**：不轉發，agent 收到「使用者拒絕，不要重試」。
- **取消、逾時（`approvals.elicitation_timeout`，預設 3 分鐘）、client 不支援、無法詢問**（例如 `claude -p` 無人值守）：不放行，維持等待；agent 收到說明原因的 `NOT EXECUTED`，可以請使用者處理後不帶 `approvalId` 重新呼叫再問一次。

需要 client 支援 MCP elicitation（Claude Code v2.1.76 以上）。這個模式的 `/mcp` 使用有 session 的串流回應，因為 gateway 要在工具呼叫中向 client 發請求；webhook 模式使用無 session 的 JSON 回應。

**已知弱點：elicitation 只證明「有某個 MCP client 回答了」。** gateway 的 MCP 端點沒有身份驗證：同機同使用者下，有 shell 的 agent 可以自己寫一個 MCP client 連上 `listen`、宣告支援 elicitation、自己按核准，gateway 照樣轉發。Claude Code 的 `Elicitation` hook 也可以自動回答確認框，agent 若能改使用者的 Claude Code 設定就能裝一個。所以這個模式適合「使用者信任自己的 agent、要的是防誤操作」，不能防 agent 本身，也不適合需要第三方核准的場景（那種情況用 `webhook`）。

**遮罩的限制：** 給人看的內容依欄位或參數名稱、以及常見的 query 形式判斷要不要遮。刻意混淆的寫法（例如 base64、多層跳脫的 JSON、同形字）不保證遮得掉。

要防 agent 本身，gateway 必須與 agent 分開部署：不同的系統帳號、容器或機器，而且 MCP 端點只讓使用者的 client 連得到。

### webhook

gateway 擋下呼叫時，向 Slack incoming webhook 發一則訊息：一句話摘要加一個一次性連結，連結指向 gateway 提供的核准頁。核准頁顯示摘要與完整參數、名稱欄位、「核准」「拒絕」兩個按鈕。按下後 agent 重送同一個呼叫（帶 `approvalId`）才轉發，只轉發一次。

**硬性要求：跑 agent 的環境不能有讀取該頻道的權限**（例如 Slack MCP、有 `channels:history` 的 token、能開瀏覽器登入 Slack 的 session）。agent 讀得到頻道，就拿得到連結，能自己核准自己。gateway 能做的只有：連結的 token 不回給 agent、不寫進日誌或任何檔案；其餘靠環境隔離，gateway 無法替你驗證。

**同機部署限制**：gateway 與 agent 在同一台機器、同一個使用者時，agent 可以用 `ps eww` 之類的方式讀到 gateway 行程的環境變數，拿到 `LODOG_SLACK_WEBHOOK_URL`。它不能因此核准（連結的 token 不在環境變數裡），但可以用這個網址往核准頻道偽造訊息：假的「已核准」、釣魚連結。gateway 與 agent 要用不同的系統帳號或容器跑。

連結的規則：

- 每個核准請求各自一組隨機 token（256 bits），只出現在 webhook 訊息裡；不回給 agent、不進日誌、不落地（記憶體只存雜湊，比對用 constant-time）。
- 用過（核准或拒絕）、逾時，連結立刻失效；錯的、用過的、逾時的連結回一模一樣的頁面，猜的人學不到東西。
- 核准頁是 `POST`，帶只有該頁面載入時才有的一次性 nonce，並拒絕跨站來源（`Origin`／`Sec-Fetch-Site`）。頁面不載入任何外部資源，不送 Referer，不可被嵌入。
- agent 給的理由與所有參數在頁面上都會跳脫，不會變成頁面的標記。

**通知沒送出時**（webhook 網址錯、Slack 不通）：沒有其他路徑能核准，所以 gateway 不假裝有人被問到。日誌寫「approval notification was not sent: nobody has been asked」，回給 agent 的訊息說「核准通知沒送出，沒有人被問到」，下一步是請操作人處理後，不帶 `approvalId` 重新呼叫，重新發送通知。

## 核准人看到什麼

webhook 的 Slack 訊息（elicitation 的確認框內容相同，但沒有連結）：

```
🔒 需要核准  apr_0123456789abcdef

user:alice（agent:bench）的 agent 想要：Reject a withdrawal.
• 對象：POST /api/payment/transaction/withdraw/reject
• 風險：money
• agent 自述的理由（不是 gateway 查證過的）：
> Customer asked support to reject W-1.

[ 開啟核准頁 ]
連結只能用一次，到 14:30 前有效。拿到連結的人就能核准，請不要轉貼。
```

- **誰**：`identity.on_behalf_of`（這個 gateway 代表的人）和 agent 的 `identity.actor`。
- **要做什麼**：任務 tool 取 workflow 的 `summary`，一對一的 tool 取 overlay 的 `summary`；沒有就退回 `METHOD path`。
- **對誰**：呼叫實際會打的路徑（輸入已決定路徑時是實際路徑，否則是樣板）。
- **風險**：policy 的等級名稱。
- **理由**：agent 呼叫時可以帶選填的 `approvalReason`（一句話，最多 300 字）。它是 agent 自己的話，標明「自述」，Slack 標記（@、連結、`*`）都會被跳脫。它不會轉發給舊系統，也不影響核准綁定的參數。
- **完整參數**：JSON（過長會截斷）；上傳檔案只顯示大小，不顯示內容。核准頁和確認框都有。

webhook 不能改已發出的訊息，結果以後續短訊息發出：「已核准（頁面上自填的名稱：…）」「已拒絕」「已用掉：核准過的呼叫已轉發一次」「逾時沒人處理，連結已失效」。

agent 的下一步（文字都在 tool 結果裡）：被擋下 → 等人；核准 → 用同樣參數加 `approvalId` 重送（elicitation 不必重送）；拒絕 → 不要重試，告訴使用者；逾時 → 不帶 `approvalId` 重新請求。

## 設定

### elicitation（預設）

```yaml
approvals:
  listen: 127.0.0.1:7402          # 只能列出，不能核准或拒絕
  mode: elicitation               # 預設值，可省略
  elicitation_timeout: 3m         # 使用者回答的時間，預設 3m
```

沒有其他要設的。

### webhook

```yaml
approvals:
  listen: 127.0.0.1:7402
  webhook:
    url_env: LODOG_SLACK_WEBHOOK_URL    # 預設值，可省略；網址本身是祕密，只放環境變數
    listen: 127.0.0.1:7403              # gateway 提供核准頁的位址
    base_url: http://localhost:7403     # 訊息裡的連結指向這裡；核准人要連得到
```

- `listen` 要和 `listen`（MCP）、`approvals.listen` 都不同。demo 用 localhost；核准人不在同一台機器時，`base_url` 要填他們連得到的位址（內網網址、反向代理），並用 https。
- 設了 `webhook` 區塊就是 webhook 模式（也可以寫 `mode: webhook`）；`mode: elicitation` 加 `webhook` 區塊會被拒絕。
- webhook 網址不會出現在日誌或錯誤訊息裡（共用 `internal/redact` 的遮罩）。缺環境變數就在啟動時失敗並指出變數名稱。
- 建立 incoming webhook：Slack → Your Apps → Create New App → From scratch（只為了 webhook，不需 scope、不需 Socket Mode）→ Incoming Webhooks 開啟 → Add New Webhook to Workspace → 選核准頻道 → 複製網址。`export LODOG_SLACK_WEBHOOK_URL=https://hooks.slack.com/services/...`。這個頻道只給核准人，不要把 agent 能讀的東西接上去。
- 通知管道是 `approval.Notifier` 介面（送出、更新、收決定），webhook 是目前唯一的實作（`internal/approval`）。

## 實機驗證

自動化測試用假 MCP client 涵蓋核准、拒絕、取消、不支援、逾時、並發；真的 Claude Code 互動模式要有人按確認框，agent 自己按不了，所以要人手動驗：

- **elicitation**：起 petstore3 的 L3 gateway，Claude Code（≥ v2.1.76）互動模式連上，請 agent 刪一個 pet。確認框要有誰、要做什麼、對象、風險、完整參數；核准後只刪一次，拒絕後沒有轉發；不回答超過 `elicitation_timeout` 維持等待；`claude -p` 下被擋的呼叫維持等待、不放行。
- **webhook**：設好 webhook 與環境變數，讓 agent 呼叫要核准的 tool。頻道出現訊息和連結（agent 的回應裡沒有連結）；核准後 agent 重送只轉發一次、再重送被擋、同一個連結再開是無效頁面；拒絕、逾時、webhook 網址錯（agent 收到「核准通知沒送出」）各確認一次。
