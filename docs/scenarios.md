# 使用情境：人只做判斷，CLI 交給 agent

lodog 有很多 CLI 指令，但**人不需要學它們**。人不擅長讀大量文字，agent 操作 CLI 很容易，所以分工是：

- **人做判斷**：這個系統能不能接、哪些操作動到錢、誰來核准、這個結果合不合理。
- **agent 做操作**：跑指令、讀輸出、整理成人看得懂的幾個問題。

通常是人和 agent 一起做：agent 往前跑，到需要判斷的地方停下來問人，人回答後 agent 繼續。目標見 [goal.md](goal.md)，這份文件說三種人各自怎麼用，以及哪些還沒做到。

## 一、系統 owner：把自己的系統接上來

**人做什麼**：裝上接入 skill（`lodog skill install`，[getting-started.md](getting-started.md)「讓自己的 agent 帶著做」），對自己的 agent 說「幫我把這個系統接上 lodog」，然後在關卡做決定：

- swagger 從哪來（URL 或檔案）
- 登入用哪個帳號
- 哪些 API 動到錢或憑證（agent 草擬的風險分類，人確認）
- 任務 tool 合不合理（agent 草擬的任務，人看會不會做出怪事）
- 誰是核准人

**agent 做什麼**：`lodog score` → `init` → `enrich` → `workflows draft` → `policy draft` → `serve`，需要時再跑 `bench`、`report` 量分數。每一步的指令與輸出見 [getting-started.md](getting-started.md)。審核時跑 `lodog review ask` 把待審項目整理成少數是非題逐題問人，人回答後 `lodog review apply` 寫回檔案、留審核紀錄（[review.md](review.md)）。

**情境**：owner 跟 agent 說接系統。agent 先量分數，回報「幾百支 API、0 支標了風險」，接著問 swagger 在哪、用哪個帳號登入。草擬完 policy，agent 只把「這 12 支看起來動到錢，請確認」拿給 owner，其餘不拿來煩他。owner 回答、指定核准人後，agent 起 gateway，把 Claude Code 接上去。

**還沒做到**：還沒在空資料夾讓一個 agent 只拿 `lodog-onboard` skill 和公開 swagger（petstore3）實際走完到 L3，確認它只在列出的關卡問人（[#47](https://github.com/iml885203/lodog/-/issues/47) 的完成條件，要有人陪同回答關卡）。

## 二、營運、客服、財務：日常操作

**人做什麼**：用自然語言下指令，例如「查一下玩家 A 昨天的存款」「幫這位玩家退款」。

**agent 做什麼**：透過 gateway 操作舊系統。查詢直接放行；動到錢、不可逆的操作被 gateway 擋下，agent 回報「這步要核准」，等核准後再帶 approval id 重呼叫。擋下靠 gateway 強制，不靠 agent 自己守規矩（[policy.md](policy.md)、[approvals.md](approvals.md)）。

**情境**：客服請 agent 刪一筆資料。gateway 回 `needs_approval`，agent 對客服說「已送出、還沒執行，在等核准」。客服不必知道有哪些 API，也不必知道核准怎麼做。

## 三、核准人：只做「同意或不同意」

**人做什麼**：收到通知，一眼看懂「誰要做什麼、影響什麼」，一鍵核准或拒絕。

**agent 做什麼**：什麼都不做，也碰不到：核准管道必須是 agent 碰不到的。管道、取捨與限制見 [approvals.md](approvals.md)。

## 總表

| 角色 | 人做 | agent 做 | 現況 | issue |
|---|---|---|---|---|
| 系統 owner | 提供 swagger 與登入、確認風險分類與任務、指定核准人 | 跑 score → init → enrich → workflows draft → policy draft → serve →（選）bench／report | 可用：`lodog-onboard` skill 帶 agent 跑完，審核用 `lodog review` 回答是非題 | — |
| 營運／客服／財務 | 用自然語言下指令 | 透過 gateway 操作，危險操作被擋下等核准 | 可用（L3 gateway） | — |
| 核准人 | 收通知、核准或拒絕 | 不參與，也碰不到 | 可用（[approvals.md](approvals.md)） | — |

## 已知限制

- **分不出是哪位營運人員**：身份只是設定裡固定的標籤，不是追得到真人（[levels.md](levels.md)）。
- **本機跑不起來的系統**，benchmark 暫時量不了：[#42](https://github.com/iml885203/lodog/-/issues/42)。
