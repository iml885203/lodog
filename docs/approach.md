# Approach：怎麼讓 API 變成 agent-native

框架分三層：**Spec（格式）→ Compiler（產 tools）→ Runtime（執行把關）**。這份只寫現在做到的樣子；還沒做的設計都在 issue，文件最後列出連結。

## 一、架構

```
舊專案: Swagger + LLM 推斷 ─→ Agent Spec（overlay、workflows、policy）─→ lodog gateway（MCP）─→ 舊系統（不動）
                + 人工審核                   唯一來源                    風險、核准、身份標籤、紀錄
```

- **Spec**：原本的 Swagger，加上 LLM 讀 code、文件推斷出描述、任務、風險，產生 overlay、workflows、policy，再經人工審核。唯一來源是 spec，不是 tools：1:1 converter（FastMCP、Stainless）把 tool 當產物，資訊不夠時只能照抄 endpoint。
- **Gateway**：agent 不直接打 API，所有呼叫經過 gateway，由它擋風險、核准、留紀錄。MCP 只是它跟 agent 溝通的協定。
- **新專案**：從 code annotation 輸出同一份 spec，尚未支援（[#61](https://github.com/iml885203/lodog/-/issues/61)）。

```
人 ──對話──→ Agent（Claude Code 等）──MCP──→ lodog gateway ──→ 舊系統（不動）
                                            風險、核准、身份標籤、紀錄
```

風險控管放在 gateway 而不是 prompt：描述只是提示，gateway 才是強制執行。舊系統的 code 不用改，但要處理兩件事：

- **agent 的帳號**：至少開一個 service account 給 gateway。
- **網路**：舊 API 只開放給 gateway，agent 和使用者不能繞過。

## 二、格式：用既有標準組起來

| 需求 | 標準 | 現況 |
|---|---|---|
| 舊 API 不能改，要外掛補充資訊 | **OpenAPI Overlay 1.0**（JSONPath 對原 spec 加欄位） | 已實作（L1） |
| 多支 endpoint 組成一個任務 | **Arazzo 1.0** | 已實作子集（L2，格式見 [contracts.md](contracts.md) 第三節） |
| 風險分級與核准規則 | 自己定的 `x-agent` 與 policy | 已實作（[policy.md](policy.md)） |
| 輸出給 agent 的標記 | MCP tool annotations | 尚未支援（[#58](https://github.com/iml885203/lodog/-/issues/58)） |
| 錯誤回應 | RFC 9457 | 尚未支援（[#58](https://github.com/iml885203/lodog/-/issues/58)） |
| agent 自己的身份 | OAuth token exchange（RFC 8693） | 尚未支援（[#60](https://github.com/iml885203/lodog/-/issues/60)） |

標準沒涵蓋、自己定的是 `x-agent`：風險分級、核准政策、使用時機。`x-agent` 在 overlay 與 workflow 裡目前有 `risk`、`whenToUse`、`whenNotToUse`、`next`、`needsReview`；其餘欄位（`exposed`、`approval`、`idempotent`、`dryRun`、`errors`、`inputs` 的 `source`）見 [#57](https://github.com/iml885203/lodog/-/issues/57)。

## 三、怎樣算 agent-native

1. **清楚**：寫出用途，**什麼時候用、什麼時候不要用**；參數有語意；**ID 從哪裡來**。
2. **整合**：以 task 為單位，控制在約 20 個以內（L2、L3）。
3. **安全**：
   - 風險分級由 policy 定義，名稱各系統自己定；每級對應 allow／approval／deny／hide，可標 `never_auto_approve`（只有人能核准）；gateway 只看屬性不看名稱（[policy.md](policy.md)、[contracts.md](contracts.md) 第三節）。
   - 危險操作要人核准（[approvals.md](approvals.md)）。
   - 身份目前只做到標籤：agent 和代表的人是設定裡固定的 `identity.actor`、`identity.on_behalf_of`（[levels.md](levels.md)）。
4. **錯誤可恢復**：L2、L3 的 `error` 一定帶 `reason` 和 `nextAction`；被擋下的呼叫回 `NOT EXECUTED` 和下一步（[contracts.md](contracts.md) 第三、四節）。
5. **可驗證**：用題庫跑 benchmark，每一級一個分數（[benchmark.md](benchmark.md)）。

## 四、轉完之後的產出

| 產出 | 內容 |
|---|---|
| Agent spec | overlay + workflows，放 repo、經人工 review |
| lodog gateway | MCP 介面、tools、核准、紀錄；L2 以上有 server `instructions`（workflows 的 `info.description`） |
| Policy | 風險等級對應核准規則，和 spec 分開，維運可以自己調 |
| 分數報告 | 各級 benchmark 分數（[levels.md](levels.md)） |
| 轉換報告 | `lodog score`：沒說明的 API、GET 卻會改資料等原系統的危險設計；L4 段列出要改 code 的項目和修正指令（[static-score.md](static-score.md)） |

L0 到 L3 都是同一個 gateway，只換設定：L0、L1 一支 API 一個 tool，L2 收斂成任務 tool，L3 用同樣的任務 tool 把強制執行打開。每一級的內容見 [levels.md](levels.md)。Skill 和 CLI 是選配：Skill 只放 API 沒有的業務知識（例如退款 SOP），不能拿來做風險控管。

## 五、MCP 對應：spec 編譯後放在哪

目前 gateway 的 tool 有 `name`、`description`、`inputSchema`。L2、L3 的任務 tool 回傳 `structuredContent` envelope（`status`、`data`、`truncated`、`next`、`error`，同一份 JSON 也放在 `content` 文字裡，格式見 [contracts.md](contracts.md) 第三節）；L0、L1 回傳 `HTTP <status>` 加原始 body 的文字。

給誰讀要分清楚：`description` 與參數說明給**模型**讀（用途、什麼時候用、風險摘要）；風險的強制執行在 gateway，不靠 agent 讀到。人工核准用 MCP elicitation，或核准人不是使用者時走 Slack webhook（[approvals.md](approvals.md)）。`annotations`、`_meta`、`outputSchema`、`preview`／`needs_approval` 狀態、`audit` 尚未支援（[#58](https://github.com/iml885203/lodog/-/issues/58)）。

## 六、Tool discovery：LLM 怎麼找到適合的 tool

MCP 本身沒有搜尋機制，模型靠名字和描述自己挑，所以 `description` 裡的「什麼時候用、什麼時候不要用」本身就是路由規則。tool 一多就出問題：幾百個 operation 的系統，L0 全放進 context 會吃掉大量空間、選擇準確率也掉，這是業界建議約 20 個上限的原因。

lodog 的做法：L2、L3 收斂成約 20 個任務 tool，全部放進 context，再配一段 server `instructions` 描述 domain 地圖和常見流程。備案是 `list_domains` 加 `load_domain` 的 progressive disclosure，尚未支援（[#59](https://github.com/iml885203/lodog/-/issues/59)）。
