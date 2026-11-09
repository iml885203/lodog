# 審核摘要：人只回答少數問題，不讀 YAML

`enrich`、`workflows draft`、`policy draft` 產出的都是草稿，要人審核後才用。大系統的 policy 草稿一支 API 一條規則，overlay 有上百支 `needsReview`，讓人直接讀 YAML 不實際。`lodog review` 把待審的項目依「照草稿會怎樣」分組，每組一個是非題，附一句為什麼這樣判斷；人只回答，agent 把回答寫回檔案並留下紀錄。情境見 [scenarios.md](scenarios.md)。

```
lodog review ask    →  agent 逐題問人（或人看 HTML 摘要）  →  agent 把回答填進清單  →  lodog review apply
```

## 一、出題：lodog review ask

```bash
lodog review ask --case cases/backoffice -o review.yaml --html review.html
lodog review ask --policy spec/policy.auto.yaml -o review.yaml      # 只審 policy
```

`--case` 讀 case 裡存在的草稿：`spec/policy.auto.yaml`（沒有就讀 `spec/policy.yaml`）、`spec/overlay.yaml`、`spec/workflows.arazzo.yaml`；`--policy`、`--overlay`、`--workflows` 個別指定。清單的 `files` 一律存成完整路徑，所以 `lodog review apply` 在哪個目錄執行都可以。清單（YAML）寫到 `-o`（或 stdout），給 agent 在對話裡逐題問；`--html` 另外寫一頁摘要給人看（見下一節）。stderr 印出幾題、幾項。

**怎麼分組**（題目順序就是優先順序）：

| 來源 | 待審的項目 | 一題問什麼 |
|---|---|---|
| policy | `why` 不是 `reviewed by` 開頭的規則 | 每個風險一題：「這 N 支是 money：每次呼叫都等核准，每一支都對嗎？」模型沒把握的（`needs review`）另成一題、排在最前面；同組內順序照草稿（最嚴的在前），`read`（不經核准就放行）一題批次確認 |
| overlay | 還標著 `x-agent.needsReview` 的 operation | 依 `x-agent.risk` 每個等級一題，順序 irreversible、money、credential、read、write |
| workflows | 還標著 `x-agent.needsReview` 的任務 tool | 一題：照草稿提供給 agent，或拿掉哪幾個；沒有還原的寫入 tool 也在這題，`why` 是 `x-agent.reviewNote`（說明沒有還原、swagger 裡有哪些候選 operation），要補反向 tool 就在 workflows 檔加 workflow |

每題有 `ask`（問句）、`why`（一句為什麼要問、答錯的代價）、`proposed`（答「對」時每項的值）、`choices`（可以改成的值）和 `items`（每項的 key 與草稿的理由：policy 是規則的 `why`，overlay 是 `summary`，workflows 是 `x-agent.reviewNote`，沒有就是 `summary`）。Backoffice 的實際題數和各風險的支數見 [cases/backoffice/README.md](../cases/backoffice/README.md)（policy 草稿那段）。

## 摘要頁：給人看的 review.html

`--html` 的頁面是唯讀的，人不能在頁面上作答；頁面只負責讓人看懂、並告訴人去哪裡答。

- **頁首「怎麼作答」**：兩種方式。一、在 Claude Code 叫出 `lodog-onboard` skill，由 agent 逐題問、代填、套用。二、自己編輯 `-o` 那份 `review.yaml`：頁面印出它的完整路徑、三段可複製的 `answer` 範例（寫在 `questions:` 底下、第一題的 id 之下，縮排和檔案一致，說明是在既有那題加一行而不是新增項目；註解跟著語言；`<choice>` 旁說明可填哪些值）和可複製的 `lodog review apply <路徑> --by "<你的名字>"`。沒給 `-o`（清單印到 stdout）時路徑欄寫 `review.yaml`。
- **每題先講白話**：「要決定什麼」（一律用「對嗎？」問，和作答的「對／不對」對得上）、「lodog 的建議」「答『對』會怎樣」「答『不對』會怎樣」四行，風險是 read、write、money、credential、irreversible 時再加一行「『風險』是什麼」說明 agent 會怎麼被處理；項目清單預設收合，展開後才看到 lodog 原始的英文問句、可改成的值和每一項。這四行由題目的來源與 id 決定句型（policy、policy 模型沒把握的、overlay、overlay 沒標風險的、workflows），不是翻譯清單裡的英文 `ask`；不認得的題型就直接顯示 `ask`。policy 題會讀清單裡的 policy 檔，把風險的處理方式（直接放行、等核准、等真人核准…）寫進句子。
- **中英文**：沿用 report 頁的做法（[internal/report/i18n.go](../internal/report/i18n.go)）：兩份字典（`internal/review/i18n_zh.go`、`i18n_en.go`）放在頁面裡，預設依瀏覽器語言，右上角 `中`／`EN` 切換，選擇記在 `localStorage`（和 report 頁共用同一個 key）。程式產生的固定句子兩種語言都有；項目的 operation key、模型寫的說明、清單的原始英文問句保留原文。
- **已套用**：`-o` 旁邊有 `review-log.yaml` 時，頁首顯示「已套用：誰（可多人）於何時（最後一筆）回答了幾題」。已回答但還沒套用的題（清單裡有 `answer`）在該題顯示「已回答」。

## 二、回答：填 answer

agent 問完一題，就在該題加上 `answer`：

```yaml
answer: {confirmed: true}                                          # 對，每一項照 proposed
answer: {confirmed: true, change: {"POST /api/x": irreversible}}   # 對，除了這幾項
answer: {confirmed: false, change: {...每一項...}}                 # 不對，每一項該是什麼
```

`change` 的 key 必須是該題的項目、值必須在 `choices` 裡；答「不對」，或該題沒有 proposed 可照（overlay 沒標 risk 的），`change` 要列出每一項。可以只答部分題目，沒答的維持待審，下次 `ask` 會再問。可另加 `note` 記人的說明。

## 三、寫回：lodog review apply

```bash
lodog review apply review.yaml --by "Ada (ops owner)" --policy-out cases/backoffice/spec/policy.reviewed.yaml
```

- **policy**：每條規則的 `risk` 改成回答的值，`why` 改成 `reviewed by <誰> on <日期>: confirmed|changed from <原等級>; drafted as: <原本的理由>`，保留檔案的註解。寫到 `--policy-out`，沒給就寫回原檔。L3 預設用的 `policy.auto.yaml` 要原樣保留，所以 Backoffice 寫到 `policy.reviewed.yaml`（[policy.md](policy.md)）。
- **overlay**：`x-agent.risk` 改成回答的值，刪掉 `needsReview`。回答只確認風險等級；description 裡模型自己標的猜測（例如 `[猜]`）還是原文。
- **workflows**：答 keep 的刪掉 `needsReview` 和 `reviewNote`，答 drop 的整個 workflow 拿掉，其他 tool `x-agent.next` 裡指向它的名字也一併刪掉，保留檔案的註解。`info.description` 裡提到它的文字要人另外改。
- **審核紀錄**：每題一筆，附加到 `--log`（預設是清單旁的 `review-log.yaml`）：時間、誰、哪一題、問句、哪個檔案、寫到哪、幾項、改了哪些、`note`。

回答格式不對（`change` 指到不在題目裡的項目、值不在 `choices`、答「不對」卻沒列完）時什麼都不寫。檔案在出題後被改過（項目已審過、等級和出題時不同、找不到）也拒絕，訊息會說「ask again」：重新 `ask` 再問，不會把回答套到人沒看過的內容上。同一份清單套兩次也會被拒絕。

## 已知限制

- 一題可能有上百項（例如 Backoffice 的 `read`，支數見 [cases/backoffice/README.md](../cases/backoffice/README.md)）。人看 HTML 摘要掃過清單，或由 agent 挑出看起來可疑的幾支先問；格式上仍是一題。
- overlay 的題目只問風險等級，description、參數說明的正確性要人另外看。
- 還沒找一位沒看過 YAML 的人實際只靠摘要走完一輪（#46 完成條件之一）。
- 摘要頁只能看，不能直接在頁面上作答或下載答案檔；要作答得編輯 `review.yaml` 或用 `lodog-onboard`。
