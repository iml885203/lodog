# lodog 的目標

這份是整個專案的北極星。開 issue、做取捨之前，先對照這裡。

## 問題

營運、客服、財務每天在 backoffice 做大量重複操作：查玩家、看存提款、處理紅利、回客訴。我們想讓 agent 來做這些事（**agent operation**），人只負責核准。

但 agent 做不到，卡在舊系統：

- **看不懂**：API 是給人點 UI 用的，沒有說明，agent 只能從網址猜每支 API 在做什麼。
- **選不準**：幾百支 CRUD，一件營運工作要組合好幾支，agent 要自己猜順序。
- **不安全**：查詢和出款看起來一樣，agent 沒有自己的身份，危險操作也沒人把關，所以沒人敢真的交給它。

改舊系統讓它適合 agent，成本高，也沒人想做。

## 目標

> **讓 agent 能安全地在舊系統上做營運操作：不需要改舊系統的 code，任何有 swagger 的系統都接得上，而且能用分數證明 agent 做得到、做得對。**

## Impact

| 對誰 | 改變 | 怎麼量 |
|---|---|---|
| 營運、客服、財務 | 重複的 backoffice 操作交給 agent，人只負責核准 | 任務成功率、每件工單省下的時間 |
| 系統 owner | 不用改 code 就能接 agent；想再進步，有一份「改哪裡能多幾分」的清單 | 接入要花的時間、L0 → L3 的分數 |
| 公司 | agent 的每次操作都有人核准、有紀錄，追得到是誰 | 未經核准的危險呼叫 0 次、可追溯率（定義與限制見下） |

三種角色各自怎麼用（人只做判斷、CLI 交給 agent）見 [scenarios.md](scenarios.md)。

**這兩個量的意義：**

- 「0 次」是 benchmark 的 gate 數字，每個 case 每一級各自有結果；各 case 現在量到什麼，見 [cases/backoffice](../cases/backoffice/README.md)、[cases/keycloak](../cases/keycloak/README.md)。
- 可追溯率量的是「每個呼叫都帶 gateway 設定的 `identity.actor` 與 `on_behalf_of`」，這兩個是設定裡固定的標籤，依真實使用者區分身份尚未支援；所以 100% 代表全部呼叫都帶了標籤，不代表已追得到是哪一個人（[levels.md](levels.md)）。

分級和分數怎麼算，見 [levels.md](levels.md)。

## 原則

遇到岔路時照這個選：

- **不需要改舊系統**：L0 到 L3 全部在 gateway 完成，舊系統一行都不用動。想再進步，L4 告訴 owner 改哪裡能多拿幾分，改不改、何時改由 owner 決定。
- **安全靠強制，不靠提示**：核准和權限由 gateway 擋下，不靠 agent 自己守規矩。
- **用分數說話**：每個改進都要能在 benchmark 上看到差距。
- **使用者只碰 CLI**：不需要懂我們的 source。

## 不做的事

- 不取代舊系統，也不重寫它的商業邏輯。
- 不做通用的 agent 平台或聊天介面，只做「系統和 agent 之間」這一層。
- 不替舊系統改 code，也不幫它修 bug。L4 只列出建議。

## 範例

[匿名後台測資](../cases/backoffice/README.md) 用於離線回歸測試；[Keycloak](../cases/keycloak/README.md) 使用公開 API。此 repo 不包含實際 benchmark 紀錄，請在自己的測試環境產生證據。

## 目標和排程是兩件事

目標決定**做不做**，排程決定**什麼時候做**。符合目標、但不擋住眼前 demo 的工作（例如把 XML request body 轉成 tool 的參數），照樣開 issue，只是排在後面。
