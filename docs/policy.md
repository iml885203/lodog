# Policy：L3 的風險等級怎麼產生

L3 的 gateway 依 policy 決定每支 API 放行、等核准、拒絕或不給 agent 看（格式見 [contracts.md](contracts.md) 第二、三節）。policy 是產品設定：由 lodog 草擬，維運／風險 owner 審核後才用。

```
lodog policy draft  →  人工審核  →  存檔，gateway 設定的 policy 指向它
```

## 一、草擬：lodog policy draft

```bash
lodog policy draft cases/backoffice/spec/openapi.json --case cases/backoffice --out cases/backoffice/spec/policy.auto.yaml
lodog policy draft openapi.json                       # policy 寫到 stdout
lodog policy draft openapi.json --model <model>       # 預設 claude-opus-5-5
lodog policy draft openapi.json --case <case 目錄>    # 加上 case.yaml 的詞表（static-score.md「詞表」）
lodog policy draft openapi.json --risks risks.yaml    # 用系統自己的等級集合，不用 lodog 的預設
```

policy 寫到 `--out`（或 stdout），摘要寫到 stderr：各風險等級幾支、幾支由規則判斷、幾支由模型判斷、幾支要審核；產出的 policy 沒有任何等級標 `never_auto_approve` 時另外印警告。產出一定能通過 `internal/policy` 的驗證載入，載入不了就報錯、不寫檔。

**等級集合**：草稿用哪些等級、各自的動作和屬性，寫在一份可讀的範本。lodog 內建的是 `internal/policydraft/risks.yaml`；系統要用自己的名稱或分法，複製一份改好，用 `--risks` 指給 `lodog policy draft`。內建的等級：

| 等級 | 預設動作 | 屬性 | 意思 |
|---|---|---|---|
| `irreversible` | approval | `never_auto_approve` | 做了就收不回來，或會送到系統外：刪除、付款出去、結清的交易（完成、核准、rollback）、發訊息到系統外 |
| `money` | approval | | 動到錢或價值：支付、存提款、轉帳、入帳、退款、餘額 |
| `credential` | hide | | 密碼、OTP、解鎖、token |
| `write` | approval | | 改一般資料，之後改得回來 |
| `internal` | hide | | 服務之間呼叫，沒有營運人員會用 |
| `test` | hide | | 留在 API 裡的測試端點 |
| `read` | allow | | 查詢 |

`default` 是 `write`（approval）：fail closed。

範本的每個等級寫 `name`、`action`、選填 `never_auto_approve`、`classes`、`means`（給模型和審核的人看的說明）。`classes` 把 lodog 判斷出的類別對到這個系統的等級：類別固定是 `read`、`write`、`money`、`irreversible`、`credential`、`internal`、`test`（也就是 overlay `x-agent.risk` 的值加上 internal、test），每個類別只能、也必須交給一個等級，`read` 的等級不能再收別的類別。例如系統把收不回來的叫 `destructive`、不分 money 和 write：

```yaml
default: change
risks:
  - {name: destructive, action: approval, never_auto_approve: true, classes: [irreversible], means: gone once it runs}
  - {name: change, action: approval, classes: [write, money], means: edits records}
  - {name: secret, action: hide, classes: [credential, internal, test], means: not for agents}
  - {name: lookup, action: allow, classes: [read], means: looks things up}
```

範本的順序就是審核的順序，最嚴的放前面；模型遇到同時符合兩個等級的，選排前面的。

`irreversible` 和 `money` 的動作都是 approval，差別在核准的人：`irreversible` 標了 `never_auto_approve`，benchmark 的 runner 扮演營運人員時一律不核准（[benchmark-task-types.md](benchmark-task-types.md)）。gateway 只看這個屬性，不看等級名稱（[contracts.md](contracts.md) 第三節），所以系統把它改名，保護照樣生效。**同時是錢又收不回來的（付款出去、完成提款）標 `irreversible`**：錯核准一筆 money 還能再改回來，錯核准一筆 irreversible 就沒辦法了，所以取較嚴的那個。Backoffice 的草稿也是這樣（付款、提款、轉帳類是 `irreversible`，標 `never_auto_approve`）。

**每支 operation 一條規則，不用萬用字元**，連查詢也是。沒有 `GET /**` 這種規則，所以草擬時沒認出來的「GET 卻會改資料」不會被放行，而是落到 default；文件之後新增的 API 也一樣落到 default。每條規則的 `why` 寫出是誰判斷的、為什麼。規則依等級分組，照等級集合的順序（內建的是 irreversible、money、credential、write、internal、test、read），組內「needs review」排最前面，審核的人一次看完同一類。

**誰判斷**：

1. 規則先判斷出類別，依序第一個成立的決定，再由等級集合的 `classes` 對到等級（下面寫的是內建集合的等級名稱）：
   - operation 自己宣告的 `x-agent.risk`（L1 的 overlay，#5）；宣告 `read` 但會改資料的不採用；宣告 `read` 但還標著 `needsReview`（模型沒把握、沒人審過）的也不採用，未審核的 overlay 只能讓風險更高，不能更低（其他等級照採，因為只會更謹慎）
   - 路徑有一段以 `internal` 開頭 → `internal`
   - 路徑有一段是 `test`／`tests`，或 `Test` 後面接大寫、`-`、`_`（`TestDelete`、`test-voucher`）→ `test`
   - 路徑、operationId 或 summary 有帳密字眼（password、otp、unlock、token…）→ `credential`
   - GET 且看不出會改資料 → `read`。這裡用的是比評分嚴的判斷（`internal/readiness` 的 `MaybeMutating`）：除了名字是動作的 GET，路徑有錢的字眼，或路徑最後兩段、operationId、summary 有 refund、transfer、payout、lock、trigger、generate、sync 或動作字，或有 `action`／`command`／`op`／`operation` 這類挑動作的 query 參數的 GET，也不直接判 `read`，交給下面的模型。為什麼和靜態分不同：[靜態分](static-score.md)要準，這些字單獨出現的 GET 多半唯讀，算成「會改資料」是不公平的扣分；policy 要 fail-closed，誤判成 read 的代價是沒經核准的危險呼叫，誤判成要核准只是多一次核准
   - 會改資料，且符合下列任一 → `irreversible`：
     - method 是 `DELETE`
     - 路徑最後兩段（不算 `{參數}`，camelCase 拆開）有收不回來的動作字：delete、remove、purge、destroy、wipe、terminate、close、payout
     - 有錢的字眼（`internal/readiness` 的 `Money`），且路徑最後兩段有結清的動作字：complete、approve、reject、process、rollback、issue、settle、transfer、refund
   - 會改資料（`Mutating`，GET 要名字是動作）且有錢的字眼（`internal/readiness` 的 `Money`）→ `money`；`irreversible` 先判斷，所以這裡只剩收得回來的（建立、修改設定）
2. 規則判斷不了的（會改資料、但看不出是哪一類）交給模型：一次約 30 支，給 method、path、tag、summary、參數名稱，模型回每支的等級、一句理由、有沒有把握。prompt 列出等級集合裡 `read` 以外的等級和各自的 `means`，要模型在同時符合兩個時選排前面的，所以「動到錢又收不回來」會選 `irreversible`，和規則的順序一致。模型只能選 `read` 以外的等級，唯一例外是 GET：明顯只查詢的 GET（列表、搜尋、報表）可以選 `read`，但理由要說明為什麼只是查詢、而且要有把握；沒把握的 `read` 一律改成 `write`、標 needs review，等人決定。其他 method 答 `read` 視為回答格式錯誤。看起來只是查詢的 POST（報表、檢查名單）也標 `write`（收 `write` 類別的等級）、註明沒把握，由人決定要不要放行。
3. 模型沒把握的，`why` 以 `needs review` 開頭。模型的回答格式不對（不是 JSON、少一支、等級不在清單、沒說有沒有把握）就整個停下報錯，不拿預設值補。

模型透過 headless Claude Code 呼叫，不給任何 tool、在空的暫存資料夾裡跑，和 benchmark 的 judge 共用 `internal/llm`。草稿頂端記下用的模型。

**只讀 OpenAPI 文件、詞表和等級集合。** 詞表是 `--case` 的 `case.yaml` 裡的 `signals`，等級集合是 `--risks` 的檔案或內建範本；不讀 benchmark 的 `answers.yaml`（`must_not_call_without_approval`），也不讀 case 的其他檔案：policy 是維運確認的產品設定，must_not 是 bench 作者確認的測試答案，兩者要各自產生；policy 從答案推出來，L3 就是拿自己的答案評自己，安全永遠滿分。`internal/policydraft` 的測試確認它不依賴任何 benchmark 套件。

## 二、人工審核

**誰**：系統的維運或風險 owner，也就是決定「這支 API 讓 agent 做要不要人核准」的人。不是 bench 作者。

**看什麼**：

- 每一條 `irreversible`、`money` 和 `credential` 規則：收不回來的有沒有都標成 `irreversible`（只有它標 `never_auto_approve`）、動到錢的有沒有都要核准、帳密相關的有沒有都藏起來，反過來也看有沒有標錯。`DELETE` 一律是 `irreversible`；系統有軟刪除、刪了能復原的，可以改成 `write`。
- 每一條 `needs review`：模型自己說沒把握的。
- 被 hide 的（`credential`、`internal`、`test`）：營運人員真的會用的不要藏。
- 標成 `write` 但其實只是查詢的 POST：要放行就改成 `read`。
- 名字看不出來、但其實收不回來或會送到系統外（付款閘道、發訊息）的操作：規則只看名字，模型沒把握的會標 `needs review`，其餘要審核的人補成 `irreversible`。

**不可以看** benchmark 的 `must_not_call_without_approval` 清單或題目來決定等級，理由同上。

**不讀 YAML 也能審**：`lodog review ask` 把上面要看的分成少數是非題（每個風險一題，`needs review` 的排最前面），人回答後 `lodog review apply` 把等級寫回、`why` 記下誰在何時審過，並留審核紀錄（[review.md](review.md)）。

審核完存成 `cases/<system>/spec/policy.yaml`，也就是 gateway 設定沒寫 `policy` 時讀的檔案。Backoffice 的 L3 讀的是原樣的草稿 `spec/policy.auto.yaml`（見下節），審核版另存 `spec/policy.reviewed.yaml`，要用時在 gateway 設定的 `policy:` 指向它。

## 三、L3 的 policy 與分數

每個 case 每一級只有一份 gateway 設定，L3 也是。它的 policy 由 gateway 設定的 `policy` 欄位決定（相對於 case，沒寫就讀 `spec/policy.yaml`）：

| policy 來源 | 做法 | 分數代表 |
|---|---|---|
| 草稿（repo 內 case 的預設） | `lodog policy draft` 的產出，原樣使用、不經人工修改 | lodog 自己（產品）的分數 |
| 審核後 | 同一份草稿經維運／風險 owner 審核後的版本，`policy:` 指向它 | 加上人工審核的分數；和草稿的差距就是審核的價值 |
| 自己手寫 | `policy:` 指向自己手寫的檔 | 自己負責的分數；policy 和 benchmark 的 must_not 同一人寫，不當產品分數 |

repo 內不放手寫的 policy 給 case 用；測 gateway 攔截機制的測試用 testdata 自帶的 fixture。

Backoffice 的 L3 用 `spec/policy.auto.yaml`（草稿的數字與分數見 [cases/backoffice/README.md](../cases/backoffice/README.md)）。審核後的 policy 尚未產生（#36）：要維運／風險 owner 審核它，存成 `spec/policy.reviewed.yaml`。

重新草擬會得到不完全一樣的結果（模型的判斷不固定）。`spec/policy.auto.yaml` 是 commit 進來的那一份，原樣提交、不手改，L3 的分數都以它為準。

## 已知限制：路徑詞表外的動詞

GET 的路徑、operationId、summary 都看不出動作時（例如 `GET /orders/{id}/capture`，`capture`、`void`、`run`、`confirm`、`submit`、`redeem` 不在詞表裡），heuristic 會判成 `read`，gateway 就放行。詞表不可能列完所有動詞，所以這是已知限制，補位的方式是 L1 overlay 的人工審核：審核者看到 summary、確認 `x-agent.risk` 後，才是有人負責的判斷；系統用得到的動詞也可以加進自己的 `case.yaml`（[static-score.md](static-score.md) 詞表）。讓規則永遠不判 `read`、`read` 只來自模型有把握的判斷或審過的 overlay，尚未支援（#38）。
