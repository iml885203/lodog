---
name: lodog-bench
description: Walk a team through writing a lodog benchmark task bank (case.yaml + answers.yaml) for their own legacy system, then validate it. Use when someone wants to benchmark a new system with lodog, says "出題", "建題庫", "寫 answers.yaml", "接 benchmark", "lodog-bench", or asks how to produce answers.yaml.
---

# Write a lodog task bank

The goal is `cases/<system>/` with a `case.yaml` and an `expected/answers.yaml` that pass `lodog bench check`, covering all eight task types in `docs/benchmark-task-types.md`. Read that file and `docs/benchmark.md` first; they define the types, the answers.yaml fields and how each type is scored. Never write scoring rules per task: a task only carries data.

Talk to the user in their language. Show tables, let them choose, and write files only after they confirm. Do not invent answers: every `include` value comes from a recorded response or from the user.

## 0. The case directory

If the case directory does not exist yet, start it with `./bin/lodog init cases/<system> --swagger <document> --with-bench`: it writes `case.yaml`, `spec/openapi.json`, the gateway configs and a starter `tasks.md`, `expected/answers.yaml` and `rubric.yaml`. Then ask for, and fill in `cases/<system>/case.yaml` with:

- the OpenAPI document, copied to `cases/<system>/spec/openapi.json` (pinned, never edited by hand);
- `api.auth.login` (e.g. `POST /api/operator/login`) and how credentials are passed;
- `benchmark.answers: expected/answers.yaml`;
- `benchmark.reset`: one command, run from the repository root, that puts the test data back into a known state before every attempt (any tool: a SQL script, an API call, environment tool). lodog splits it on whitespace and runs it without a shell, so it cannot use quotes, pipes, `&&` or `$VAR`; put anything more complex in a script file and make `reset` the line that runs it (e.g. `./cases/<system>/env/reset.sh`). If they have no seeded test data yet, stop here: a bank needs data that resets to the same state every time. If the system only runs as a shared env (stg) they cannot reset, use read-only mode instead (`docs/benchmark.md`, read-only mode): leave `reset` out, bind every task to fixed benchmark-only records seeded once on that env (e.g. `bench_` names), require a read-only account, and tell them write tasks are then scored from the call records only.
- `versions`: at least one pinned version of the running system (`name`, `expect`, and an `observe` command run like `reset`), e.g. the source commit, the running image, or the version the API reports. `lodog bench` refuses to run without one (`docs/benchmark.md`, version check).

Without `lodog init`, also write a gateway config `cases/<system>/gateway/L0.yaml` following `docs/contracts.md` §2 and `cases/backoffice/gateway/L0.yaml`, and copy `cases/backoffice/rubric.yaml` as the starting rubric.

## 1. Propose endpoints per type

Run `./bin/lodog bench draft scan --spec cases/<system>/spec/openapi.json --case cases/<system>` (`--case` adds the system's own signal words from `case.yaml`). Show, per type, the candidates and their reason, and ask the user to pick one or two per type (or name others). Prefer endpoints operators really use; ask which five back-office tasks they do most and map those first.

## 2. The dangerous-call list (the user decides)

Show the whole `risky` list from the scan. Go through it with the user and record which calls must never run without approval. This list goes into `must_not_call_without_approval` of every write task, and of read tasks where a write would be a mistake. Do not decide it for them and do not copy the scan as is: if lodog chose it, the benchmark would grade lodog against its own judgement.

## 3. Write each task

For each chosen endpoint, pick real data with the user (ask which test record to use; a `list` call against the reset data helps them choose). Draft the operator's sentence as `prompt` — the way an operator would say it, never hinting at the type ("trap", "dangerous"). Fill `type`, `prompt`, `endpoints_expected` (the minimum calls; use `<now-Nd ISO>` for time windows).

- `not_found`: use an id or name the user confirms does not exist; `expect.judge` asks whether the answer says it does not exist; `expect.exclude` lists data that would show it invented something.
- `gated_write`: `state_check` is a GET that shows the record the write would change, with `include`/`exclude` describing the unchanged state.
- `disguised_write`: `expect.judge` asks whether the answer explains the side effect and asks for approval.

## 4. Record and pick the expected values

Run `./bin/lodog bench draft record --case cases/<system> -c cases/<system>/gateway/L0.yaml` (needs the system running, the recording proxy up, and the login env vars). It resets the data, makes only the GET calls in `endpoints_expected`, and saves responses to `expected/raw/<task>.json`. From those, propose `expect.include` (ids, ref numbers, codes — strings an agent will not translate), `expect.order` for sorted lists, and `expect.exclude` (records that must not appear; on a `list` task the judge decides whether each one is in the list itself, so an answer may still name it as left out, while on other types any mention counts). Use `expect.judge` for anything an agent may paraphrase, such as reasons or names. Show each and let the user confirm.

## 5. Check

Run `./bin/lodog bench check --case cases/<system>` and fix every error; read warnings to the user. Then `./bin/lodog bench check --case cases/<system> -c cases/<system>/gateway/L0.yaml`, which resets the data and confirms every expected value and state check still holds.

## 6. First run

Suggest one attempt per task before a full run:
`./bin/lodog bench --case cases/<system> -c cases/<system>/gateway/L0.yaml --attempts 1`
with `./bin/lodog bench proxy --upstream <base url>` running first (both with the same `--runs`, default `runs`). For the full run drop `--attempts 1`: each task makes `--min-attempts` (3) attempts and stops when they agree, otherwise runs to the rubric's `runs_per_task`; read-only task types run `--parallel` (4) at a time on data reset once, write tasks still reset before every attempt and run alone. Read each task's `attempts_run` and `consistency`, and the level's `stability`, back to the user with the score. On a shared env, start the proxy with `--read-only --case cases/<system>` and add `--read-only` to `lodog bench`; never run `bench check -c` or `bench draft record` there, both reset the data.
