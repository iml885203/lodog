---
name: lodog-onboard
description: Connect a system owner's legacy REST API to lodog end to end (score, init, enrich, workflows draft, policy draft, review, serve, optional bench and report), telling the person where you are at every step, running every lodog command yourself and stopping only at the decisions a person must make. Use when someone says "幫我把這個系統接上 lodog", "接 lodog", "onboard to lodog", "make this API agent-ready", "lodog-onboard", or hands you a swagger and asks for an agent gateway.
---

# Connect a system to lodog

First rule, above all others: your first message before any tool call is the roadmap, and every step's opening line is text in the same message as that step's first command (see Progress).

You run the CLI; the person only decides. The person is a system owner, not a lodog user: never ask them to read YAML, run a command or learn a flag. Talk to them in their language, in a few lines at a time.

The full walkthrough, with real output and an error table for each step, is `docs/getting-started.md` in the lodog repository (https://github.com/iml885203/lodog). Read the section for a step when its output surprises you.

## Progress: the person must always know where you are

The person does not type commands; they watch your messages. These lines are the deliverable, not narration: send each one even when you would normally say nothing between commands. Write them as message text the person can see, not in your reasoning: thinking "I have listed the roadmap" shows the person nothing. Before every command that starts a step, check that this message's text holds its opening line. A step with no opening line, or a roadmap that never came, is a defect: if you notice you skipped one, send it now. Seven steps, always numbered N/M (M=7, or 8 if they want the optional benchmark). Use their language; the zh formats below are the model, keep the same marks and shape.

1. **Roadmap, your first message** (right after G1 is answered, or at once when the person already gave the document; send it before you run any lodog command): one line per step with a plain sentence, the estimate, and `🙋` on the steps that need them:

```
路線圖（共 7 步，全程約 <T> 分鐘，其中你要回答的有 3 處 🙋）
1 量分數：看這份文件現在幾分（約 1 分鐘）
2 建資料夾、設定登入 🙋（約 3 分鐘）
3 L1 補說明：模型逐支 API 寫白話說明（約 <a> 分鐘）
4 L2 任務 tool：把相關 API 組成一件事（約 <b> 分鐘）
5 L3 風險規則：每支 API 標風險（約 <c> 分鐘）
6 審核 🙋：你確認風險與任務 tool（約 10 分鐘）
7 啟動 gateway 🙋：誰核准、接上你的 agent（約 3 分鐘）
選配：跑 benchmark 和報告（要題庫，做完 1–7 再問你）
```

2. **Each step opens with one line** and **closes with one line**. Send the opening line as text before that step's first command, every step including 1 and 2; time the long ones (`time`) so the closing line can give the real minutes:

```
【3/7】L1 補說明：模型會讀每支 API 寫出用途與風險初判（約 8 分鐘）
✓ 3/7 完成：分數 1.2 → 20.0，12 支待你審核。下一步：4/7 任務 tool
```

3. **A long step** (enrich, workflows draft, policy draft, bench) runs in the background. Poll it and say something at least every 2 minutes, with what is running and how long:

```
… 3/7 還在跑：enrich，已 4 分 / 預估 8 分，完成 90/185 支
```

Never go silent for a long step. If it passes the estimate by half, say so and say what you are checking.

4. **A gate is a different message**, never mixed with progress. Start it with the stop mark, state the one decision, your proposal, then stop and wait:

```
⏸ 現在等你決定（G3 風險 1/4）：
POST /orders/{id}/cancel 會取消訂單，無法復原。我建議標「不可逆，要核准」。同意嗎？
```

5. **The estimate is not a constant.** Count operations first (`lodog score` prints the number of tools; the document's `paths` give the rest) and scale from the one measurement we have (#63, mata-stg, 185 paths): enrich about 8 minutes (about 2.5 seconds per operation at the default `--concurrency 4`), the whole run to L3 about 21 minutes, review excluded. Say "估算" and which number it comes from; after the first model step, replace the guess with the measured time.

6. **Closing summary** (after step 7, or after the optional step): levels set up, score per level (`L0 → L3`), which gates they answered and how (counts from `review-log.yaml`, who approved), how the gateway runs and how to start it again, what was not done and why. Short lines, no raw output.

## Ground rules

- Ask only at the gates below. Do everything else yourself, then report.
- Secrets never pass through you into a file: the configs hold environment variable *names* (`*_env`); ask the person to export the values in their own terminal. Keep the variable names `lodog init` wrote (`LODOG_UPSTREAM_USER`, `LODOG_UPSTREAM_PASSWORD`, `LODOG_UPSTREAM_TOKEN`, ...) in all four configs; do not rename them per system. Never echo, log or write a password, key or token, and never put one in a command line you show.
- Never decide what is dangerous for them. lodog drafts the risk of every operation; the person confirms it through `lodog review` (step 6). Do not edit a drafted policy, overlay or workflows file by hand to make a question go away.
- Never approve a held call yourself, by CLI, curl or anything else. Approval belongs to a person (`docs/approvals.md`).
- Run `lodog <cmd> --help` before you use a flag you have not seen this session; flags change between versions, so never guess a name.
- Run every command from the directory that *contains* the case folder; paths in the configs are relative to it.
- A step that fails: read the error, fix what lodog's message says (it names the file, flag or variable), retry once. If it still fails, stop and report the exact error line and what you tried.

## Gates: where you stop and ask

| Gate | When | Ask (one question, with your proposal) |
|---|---|---|
| G1 source | before step 1 | "Where is the API document (swagger URL or file)? And the base URL the system runs at?" Propose what you found if you found one. |
| G2 sign-in | step 2 | "lodog will sign in as `<auth type>` read from the document (`<scheme>`). Which account should the agent use, and who does it act for?" Then: "Please run `export <VAR>=...` in the terminal that will run lodog; I will not see the value." |
| G3 risks | step 6 | The review questions about the policy and overlay, one at a time, money and irreversible ones first. |
| G4 task tools | step 6 | The review question about task tools the model was unsure of. |
| G5 approver | before step 7 | "Who approves calls that move money or cannot be undone: you, in a confirmation box in your own MCP client (the default), or someone else, reached by a Slack channel webhook?" |
| G6 go | before step 7 and before any benchmark | "Ready to start the gateway (agents can then call the system, writes wait for <approver>)?" |

At G3 and G4 read each question's `ask` and `why` in plain words, name a few items, and offer the HTML summary for the full list. Accept "yes", "yes except …" or "no, these should be …"; ask again if an answer does not say which items.

## Steps

### 1. Baseline score (after G1)

```bash
lodog score <swagger URL or file>
```

Check: the total `x / 30`, the number of tools (your estimate base), the risk-marking line, any "not served". Remember the total as the baseline. A self-signed local system: add `--insecure`.

### 2. Case folder and sign-in

```bash
lodog init <name> --swagger <document> [--upstream <base URL with base path>] [--insecure]
```

`--insecure` only for a local system with a self-signed certificate: it skips TLS verification for the download and writes `upstream_insecure_tls: true` into the configs. Check: `created <name>`, the `auth.type` line, `not served:`. "cannot tell where the system runs" -> ask for the base URL, pass `--upstream`. "target already exists" -> pick another name, never delete theirs.

Then G2. Fill `<name>/gateway/L0.yaml`, `L1.yaml`, `L2.yaml`, `L3.yaml` together and keep the four identical apart from `level`: `identity.on_behalf_of` (replace `TODO`) and the `auth` block. If the person gave you the system's source code, find the login in it instead of asking them: the login route and method, the body field names, JSON or form, where the token or session cookie comes back (`token_path`, `success.cookie_name`). Put those in `login`, `username_field`, `password_field`, `login_format` of all four, and say in one line what you found and where. Take only names and paths from the source, never a credential. A level cannot be skipped: lodog refuses L2 without L1, and so on.

### 3. Describe the operations (L1)

```bash
lodog enrich --case <name> [--source <source dir>]
lodog score -c <name>/gateway/L1.yaml
```

`--source` lets the model read the code to confirm meanings, so fewer operations are marked `needsReview`; pass it whenever you have the code. Long step: background and progress lines. Check: `wrote ...overlay.yaml`, how many `need review`, any `missing:` (rerun with `--force` once, or report). The score should rise, mostly description and risk marking.

### 4. Task tools (L2)

```bash
lodog workflows draft --case <name> --source <short name>
sed 's/^level: L1/level: L2/' <name>/gateway/L1.yaml > <name>/gateway/L2.yaml
lodog score -c <name>/gateway/L2.yaml
```

(`--source` here is the document's source name, default `api`, not a directory.) Check: number of task tools, `review:` and `dropped:` lines. A gateway that refuses to start because a workflow does not match the document lists every mismatch; report it, do not guess a fix.

### 5. Policy

```bash
lodog policy draft <name>/spec/openapi.json --case <name> --out <name>/spec/policy.yaml
```

Check the stderr summary: operations per risk, how many need review, any `warning:`. `gateway/L3.yaml` reads `spec/policy.yaml`; it is not trusted until step 6.

### 6. Review with the person (G3, G4)

```bash
lodog review ask --case <name> -o <name>/review.yaml --html <name>/review.html
```

Go through `<name>/review.yaml` in order. After each answer, add it to that question:

```yaml
answer: {confirmed: true}                                      # yes
answer: {confirmed: true, change: {"POST /orders": irreversible}}  # yes, except
answer: {confirmed: false, change: {...every item...}}         # no
```

Values under `change` must be among the question's `choices`. They may stop part-way; unanswered stay pending. Then:

```bash
lodog review apply <name>/review.yaml --by "<person's name>"
```

Check: one line per answered question and `logged N answers` (`<name>/review-log.yaml`). Rerun `review ask`: it lists only what is still pending; "ask again" means a file changed, ask only the new questions.

### 7. Serve L3 (after G5, G6)

Self-approval needs no change (default `approvals.mode: elicitation`). Someone else: add `approvals.webhook` to `<name>/gateway/L3.yaml` (`docs/approvals.md`); the URL is a secret, the person exports the variable. Then:

```bash
lodog score -c <name>/gateway/L3.yaml
lodog serve -c <name>/gateway/L3.yaml     # keep it running in the background
```

`serve` can repoint one config without editing it: `--upstream`, `--listen`, `--approvals-listen` (port taken, or another system), `--actor`, `--on-behalf-of` (so the approver sees the real person). Check: `gateway ready ... level=L3 tools=N`, and `operation not served` warnings (hidden by policy is expected for credential, internal and test). Then:

```bash
claude mcp add --transport http <name> http://127.0.0.1:7400/mcp
```

Make one read-only call the person names. No write to test approvals unless they ask and name a safe record.

### Optional: benchmark and report

Ask only after step 7. `lodog report --case <name> -o report.html` works without any run: it shows the static score of each level side by side and says there is no benchmark. Do not tell the person to run a benchmark just to get the report. A benchmark needs a task bank (`lodog init --with-bench` makes a starter; writing it is the `lodog-bench` skill) and test data one command resets, or a shared env in read-only mode (getting-started §8). If they have no task bank, stop at the report and say that benchmark numbers are the part not done.

## Reporting

Use the formats in "Progress" and nothing else: roadmap, open line, close line, heartbeat, gate, summary. Never paste raw command output or YAML; quote one line when an error needs their action.
