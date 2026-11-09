# 參考：Is It Agent Ready

[isitagentready.com](https://isitagentready.com/) 是 Cloudflare 做的網站檢查工具。丟一個網址進去，它會打分數，並給修正建議。

## 跟我們哪裡不一樣

| | Is It Agent Ready | lodog |
|---|---|---|
| 問的問題 | agent **找不找得到**這個網站 | agent **進來後能不能做對、會不會闖禍** |
| 輸入 | 網址 | swagger + 題庫 |
| 怎麼檢查 | 看標準檔案在不在（robots.txt、sitemap、MCP 卡片…） | 看 spec，再讓 agent 實際跑任務 |
| 安全 | 不看 | 是門檻 |

簡單說：它檢查門口有沒有掛招牌，我們檢查進門後能不能把事做好。

## 我們拿來用的兩點

**1. 報告附「貼給 coding agent 的修正指令」**

它的報告有「Copy all instructions」，複製後貼進 coding agent 就能開始改。

lodog 的 L4 清單要這樣做：每一項附一段可以直接貼的指令。`lodog score` 的 L4 段每項都附這樣的指令（[static-score.md](static-score.md)），例如：

> 把 `GET /api/customer/{customerId}/resetPassword` 改成 POST，並加上權限檢查。

團隊拿到報告就能動手，不用自己想怎麼改。

**2. 用「公開的 agent 標準」當 L4 的加分項**

它會檢查系統有沒有公開這些東西：

- **MCP Server Card**：告訴 agent「這裡有 MCP server，能做什麼」
- **OAuth Protected Resource**（RFC 9728）：告訴 agent「要去哪裡拿權限」
- **API Catalog**：列出這個系統有哪些 API

有這些，代表系統本身就準備好給 agent 用，正好當作 L4 的客觀證據。`lodog score --origin <網址>` 會探測這三個，結果列在 L4 段，不計分。

## 不拿來用的

- **robots.txt、sitemap、bot 規則**：這是給公開網站的，backoffice API 用不到。
- **商務協定**（x402 等）：跟付款有關，不是我們的題目。
- **評分方式**：它沒有公開怎麼算分，沒辦法參考。
