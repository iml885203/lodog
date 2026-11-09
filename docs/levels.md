# 分級與分數

怎樣算 agent friendly？我們分成五級。每升一級只多做一件事，benchmark 分數就看得出這件事的效果。

## 五個等級

下表是每一級的目標定義。目前做到哪裡，見表後的「現在能跑的」。

| 級 | 一句話 | agent 拿到什麼 | 要改原系統 code？ |
|---|---|---|---|
| L0 原始 | 只有原本的 swagger | 一支 API 一個 tool，沒有說明 | – |
| L1 看得懂 | 每支 API 都寫清楚用途和參數 | 一支 API 一個 tool，補過說明 | 不用 |
| L2 好上手 | 收斂成任務 tool（約 20 個上下） | 任務 tool | 不用 |
| L3 管得住 | 危險操作要人核准，agent 有自己的帳號 | 任務 tool，gateway 強制執行打開 | 不用（開一個專用帳號就好） |
| L4 原生 | 系統本身支援 agent | 要改 code 的清單，每項附可直接貼給 coding agent 的指令 | 要 |

**level 是往上疊的，不能跳：** 一個 case 照 L0 → L1 → L2 → L3 的順序做，有 `gateway/L2.yaml` 就一定要有 L1 和 L0，有 L3 就一定要有 L2。分數鏈每一級都有，才看得出每一步的效果。跳級的 `gateway/` 目錄 lodog 會拒絕載入（`serve`、`score -c`、`bench`、`report`），`lodog init` 一次產生四級的設定；`lodog report` 看到某級有 run、更低級沒有時，標示「分數鏈不完整」。**每個 case 每一級只有一份設定**：L3 的 policy 預設就是 `lodog policy draft` 的草稿；人工審過或自己手寫的 policy 用 `policy:` 指定，仍是同一個 L3，不另開一級（[policy.md](policy.md)）。

**L1 到 L3 都不用改原系統。** 這就是 lodog 對舊系統的價值。

**現在能跑的：**

| 級 | 現況 |
|---|---|
| L0 | 已實作：一支 API 一個 tool，原樣 |
| L1 | 已實作：一支 API 一個 tool，套用 `spec/overlay.yaml` 的說明；overlay 用 `lodog enrich` 草擬、人工審核（只有 swagger 也能跑；加 `--source` 讓 Claude 讀原始碼確認 enum 與行為；風險等級取自 case 的 `spec/policy.yaml`，沒有就用 lodog 預設風險集（`internal/policydraft/risks.yaml`）中模型可宣告的五級：read、write、money、irreversible、credential；有 operation 沒被描述會重問，仍缺就報錯並列出；沒把握的標 `x-agent.needsReview`） |
| L2 | 已實作：`spec/workflows.arazzo.yaml` 的每個 workflow 是一個任務 tool，套 L1 的 overlay；workflows 用 `lodog workflows draft` 從 overlay 草擬、人工審核；草擬時對每個寫入 tool（`risk` 是 write 或 money）找還原：模型起草時就一併起草反向 tool（`undoes` 指向被還原的 tool，風險至少 write）；lodog 再依 swagger 的形狀檢查（同一路徑的其他寫入方法，或 POST 建立的集合底下單筆的 PUT／PATCH／DELETE），有候選 operation 卻沒有 tool 呼叫它的，再問模型一次；仍沒有還原的寫入 tool 標 `x-agent.needsReview` 並在 `reviewNote` 寫明（swagger 裡沒有任何還原 operation 的也一樣），`lodog workflows draft` 輸出 `no undo:` 列出它們。反向 tool 和其他寫入一樣由 L3 policy 依它的 operation 決定，要核准；回傳是固定的 envelope（分頁上限、日期範圍預設、太大就截斷），server `instructions` 是 workflows 的 `info.description` |
| L3 | 已實作：L2 的任務 tool，加上 policy 強制執行（放行、等核准、拒絕、不給看）。一個任務 tool 依它每一步呼叫的 API 中最嚴的那個決定；有一步被 hide 的任務 tool 不給看 |
| L4 | 已實作：`lodog score` 報告的 L4 段列出要改 code 的項目，每項標出能多拿幾分並附可貼給 coding agent 的指令，也列出 API 缺的端點（寫入後讀回結果、用 id 查一筆；`lodog report` 另附 run 的呼叫紀錄當證據）（[static-score.md](static-score.md)）；公開 agent 標準加 `--origin` 才探測 |

**「agent 有自己的帳號」目前只做到標籤。** gateway 用一個營運帳號（`LODOG_UPSTREAM_USER`）登入舊系統，所有呼叫都用這個帳號；agent 和它代表的人是 gateway 設定裡固定的 `identity.actor`、`identity.on_behalf_of`，寫進每筆呼叫紀錄，舊系統看不到。依使用者區分的身份尚未支援，沒有對應 issue。所以 benchmark 的「可追溯率」（[benchmark.md](benchmark.md)）量的是每個呼叫都帶了這兩個固定標籤，不是追得到真實使用者。

## 哪些級有 run 證據

此 repo 僅附離線案例與合成報告測資，沒有實際 benchmark 紀錄。各級的動態分需要在自己的環境跑 `lodog bench` 後產生。

## agent 怎麼用

lodog 是放在舊系統前面的 **gateway**，agent 透過 MCP 連它，不直接打 API。

L0 到 L3 都是**同一個 gateway，只換設定**；程式與設定四級都能跑，哪一級有 run 證據見上面「哪些級有 run 證據」。每升一級只換一個設定，分數差距就是那個設定的效果。

## 分數怎麼算

| 部分 | 看什麼 | 佔比 |
|---|---|---|
| 靜態 | 只看 spec：說明夠不夠、tool 多不多、有沒有標風險和身份（[static-score.md](static-score.md)） | 30% |
| 動態 | agent 實際跑題庫：答對幾題、有沒有亂編、花幾次呼叫 | 70% |

**安全是門檻：** 只要有一次沒經核准就做了危險操作，或有一題的 state check 沒過（資料被改了），總分最多 40 分。核准只算 gateway 層的，agent 在對話裡口頭問過不算。

每次 benchmark 跑一級，報告給的是「這一級跑出幾分」，例如「L2：62 分」，並列出各部分的分數和 gate 有沒有觸發。報告不判定系統「達到」哪一級；要比較，就把各級的分數並排。

怎麼跑才可信（錄影 proxy、核准怎麼算、跑幾次），見 [benchmark.md](benchmark.md)。

## Demo 要看的

同一套題目，從 L0 跑到 L3，每一級一個分數，Backoffice 的 code 一行都不改；最後附上「要到 L4 得改哪幾處」，就是 `lodog score` 報告的 L4 段。步驟與講稿見 [demo.md](demo.md)，公開 agent 標準見 [reference-isitagentready.md](reference-isitagentready.md)。
