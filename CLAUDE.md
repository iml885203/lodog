# lodog

lodog makes a legacy API agent-friendly without requiring any change to the legacy code (L4 only recommends changes; the owner decides): it
reads the system's OpenAPI document, serves it to agents as an MCP gateway, and
proves the result with a benchmark score. The anonymized backoffice fixture and the public Keycloak API exercise the gateway.

Read first: `docs/goal.md` (the problem, the one-line goal, principles and
what lodog does not do — check a new issue against it before opening it),
`docs/levels.md` (what agent-friendly means, L0–L4),
`docs/approach.md` (spec → gateway design), `docs/benchmark.md` (how a
score is made credible).

## How this repository is worked on

Most changes are made by agents:

1. Every quality property is machine-enforced. If a property matters and no
   gate checks it, it is not enforced. Add the gate, not a comment asking for
   care.
2. People read `docs/`, the issue's acceptance criteria and the score reports.
   Keeping those accurate is part of the work.

## The gate

```
make tools    # once: pinned golangci-lint into .tools/
make hooks    # once: pre-commit and pre-push
make verify   # definition of done
```

`make verify` exiting 0 is done. `make verify-fast` is for iterating.

**Time budget.** `make verify` finishes within 60 seconds, and `make verify`
fails when it does not, printing the slowest test packages
(`scripts/verify-budget.sh`; `make verify-fast` has no limit). A test package
that takes more than 5 seconds run alone (`go test -race -count=1 ./internal/x`;
times inside `make verify` are inflated by the packages running in parallel) is
a design signal: use a fake clock or a short
timeout, do not really wait. Tests that start a process or wait on a condition
wait for the event, never for a fixed sleep.

Never weaken a gate to make work pass: no lowering thresholds, no `nolint`
without a specific linter and a reason, no loosening rules, no deleting a
failing test. Those are changes to the harness. If a gate is wrong, say so and
explain why.

## Contracts

The formats between modules are in `docs/contracts.md`. Keep code and documentation consistent when changing them.

## Gateway invariants

The gateway is the product. These hold at L3 and each one needs a test:

- **Fail closed.** An operation with no risk classification is treated as
  needing approval, never as allowed.
  Tests: `TestAnUnmatchedOperationFailsClosed`, `TestADefaultThatAllowsIsRefused`
  (policy); `TestAnL3TaskOnAnOperationNoRuleMatchesNeedsApproval` (gateway).
- **A verdict is never ambiguous.** `ok` carries no reasons; `error` and
  `needs_approval` always carry a reason and a next action the agent can follow.
  Tests: `TestATaskToolReturnsAnOkEnvelopeWithDataAndNoReason`,
  `TestAnUnknownPlayerIsAnErrorWithANextActionNotAGuess`,
  `TestALegacyRefusalCarriesTheStepStatusAndANextAction`,
  `TestEveryTaskResultIsAnUnambiguousVerdict`, `TestEveryRefusalNamesWhatToDoNext`;
  `needs_approval`: `TestNeedsApprovalAlwaysCarriesAReasonAndANextAction`.
- **An error is never an allow.** If the gateway cannot decide, it refuses.
  Tests: `TestWhenTheGatewayCannotDecideItRefusesAndForwardsNothing` (sign-in
  refused, approval unknown), `TestOnlyAllowAndApprovalGoOnToTheLegacySystem` (an
  unknown action).
- **Record before forwarding.** The call is recorded before it reaches the
  legacy system, so a call that times out still left a trace.
  Tests: `TestL0RecordsACallBeforeForwardingIt`, `TestL3RecordsACallBeforeForwardingIt`,
  `TestL2RecordsEveryStepBeforeForwardingIt`,
  `TestAnUnreachableLegacySystemStillLeavesTheCallInTheLog`,
  `TestRecordBeforeForwardingHoldsForAnyOpenAPIDocument`.
- **Credentials never reach a record.** `/login` bodies and session cookies are
  redacted in every log and recording. What a person approving is shown
  (the held-call summary, the prompt, the webhook page, `GET /approvals`) never
  carries a step body's values: the summary names body fields only, and
  arguments go through `redact.Preview` (credential-named fields masked, a
  credential inside a string value masked, long strings clipped) and the target
  path through `redact.MaskString`.
  Tests: `TestAHeldTasksStepBodyValuesNeverReachAnyRecordOfTheApproval`,
  `TestTheUsersPromptMasksCredentialFieldsAndClipsABigInput`,
  `TestThePageMasksCredentialFieldsAndClipsABigInput`,
  `TestTheListingMasksCredentialFieldsAndClipsABigInputButTheStoreKeepsThem`,
  `TestNoSpellingOfACredentialReachesAnythingAPersonApprovingIsShown`,
  `TestTheUsersPromptShowsNoSpellingOfACredential`,
  `TestAPathCarryingACredentialIsMaskedInTheListingAndEveryNotice`,
  `TestThePageMasksACredentialInTheTarget` (approval), `TestTheUpstreamPasswordNeverReachesTheLog`,
  `TestNoAuthTypeLeaksItsSecretThroughTheProxyAndAnEchoingLegacySystem`,
  `TestATaskEnvelopeNeverCarriesTheCredential` (gateway);
  `TestTextMasksEveryFormACredentialTakesInAMessage`,
  `TestEveryCredentialSpellingIsRecognisedAsAFieldName`,
  `TestPreviewMasksACredentialInsideAStringValue` (redact).

## Docs, solutions and decisions

**Docs describe what is true now.** No history: no version logs, no "was / used
to / changed to", no dated change notes. History lives in git log and the
issues. When something changes, rewrite the doc to the new truth. What is not
supported yet is current state too: say so plainly and link the issue.
Code comments follow the same rule: state the reason, not the story of how it
was found.

**Choose the long-term solution.** Build the design you would keep. Do not
ship a workaround you would have to undo later; if the long-term fix is too
big or blocked right now, open an issue that describes it instead of shipping
a hack to get unblocked. A limitation behind a long-term interface is fine (a
config field whose value source is still fixed); a shortcut that bakes the
limitation into the code is not.

**Every decision serves `docs/goal.md`.** An issue says which problem or
impact it serves (the issue template asks). A change that serves neither is
out of scope. When two options conflict, the principles in goal.md decide.

## Closing an issue

An issue is done when its acceptance criteria hold and `make verify` passes.
Comment on the issue with what changed, how it was verified and any remaining work, then close it.
External acceptance tests require your own upstream service; the offline backoffice fixture has no bundled deployment.

## Structure

```
cmd/lodog/              entry point
internal/cli/           flag parsing and hand-off only, no behaviour
internal/spec/          reads an OpenAPI document into operations (no MCP, no HTTP)
internal/docfetch/      fetches an API document from a URL or a file (lodog init, lodog score)
internal/overlay/       reads and applies an OpenAPI Overlay (L1)
internal/enrich/        `lodog enrich`: drafts the L1 overlay with a model
internal/workflow/      reads, checks and runs the L2 task tools (Arazzo workflows); no MCP, no HTTP transport
internal/workflowdraft/ `lodog workflows draft`: drafts the L2 workflows from the overlay
internal/casecfg/       what a case is: its file layout, the gateway config (level, auth, identity, approvals) and the document a level serves; no MCP
internal/serving/       what a level serves: the tools an agent sees, the operations and workflows it skips and why, what the L3 policy hides; lodog serve and lodog score read the same answer (no MCP)
internal/gateway/       the MCP gateway: tool handlers, task-tool envelope, upstream session, transport, L3 enforcement (holds a call, asks `approval`)
internal/redact/        the one credential mask shared by gateway logs, tool results and call records
internal/approval/      how a held call gets a human verdict: the request store, the MCP-client prompt, the Slack-webhook channel (one-time link and page), the list-only endpoint; knows nothing of the gateway (docs/approvals.md)
internal/policy/        loads an L3 policy and decides each operation's action (no MCP, no HTTP)
internal/policydraft/   `lodog policy draft`: proposes a risk level per operation
internal/initcase/      `lodog init`: the files a team needs to put its own system behind lodog (templates are go:embed)
internal/readiness/     `lodog score`: the static score from documents alone, with the L4 list
internal/llm/           the one way lodog asks a model (headless Claude Code)
internal/bench/proxy/   the recording proxy in front of the legacy system
internal/bench/record/  the call-record format (docs/contracts.md section 1)
internal/bench/run/     the runner: reset, start the gateway, run the agent, play the operator
internal/bench/grade/   the judge's yes/no verdicts for a run
internal/bench/score/   the dynamic score of a run
internal/bench/draft/   helps a team draft a task bank for its own system
internal/review/        `lodog review`: turns drafted policy, overlay risks and task tools into questions a person answers, and writes the answers back (docs/review.md)
internal/skills/        the agent skills that drive lodog, shipped inside the binary
internal/report/        `lodog report`: the report page of a case (your system today, then each level lodog adds), one offline HTML page in Chinese and English
e2e/report-ui/          Playwright e2e of the report page (`make test-report-ui`)
test/acceptance/        against a user-provided test service, `make test-accept`
cases/<system>/         one legacy system: pinned versions, spec, gateway configs, env, seeds, tasks, answers
docs/                   the human-facing contract
```

`make test-accept` runs external acceptance tests against your own backoffice env
(`cases/backoffice/README.md`). `make verify` does not include it because the
pipeline has no env, so gateway work is not done until you have run it yourself.

## Tests

Pick the layer by what the code is, not by its file name.

- **Rule layer**: pure decisions, exhaustive table tests, beside the code.
  Policy decisions, scoring, readiness signals, redaction, spec conversion.
- **Integration layer**: a module with its real collaborators; only external
  things are faked (`httptest`, a fake executable). The gateway against a fake
  upstream through an MCP client; the bench runner against a fake proxy and a
  fake `claude`. Internal handlers and services are covered through this layer,
  not by their own tests.
- **Boundary layer**: only the translation to and from a third party, fed by
  recorded real payloads under `testdata/`, no network. Claude headless output,
  the Slack webhook, OpenAPI 2.0/3.x.
- **End-to-end**: few and deliberate, not in `make verify`. `make test-accept`
  (requires a user-provided backoffice service), `lodog bench`, scenario tests (#53),
  and `make test-report-ui` (the report page in a browser, `e2e/report-ui/`,
  its own CI job).
- A test that is awkward to place is a design signal: the code probably mixes a
  rule with I/O. Split the code, not the test.
- A test that would pass with its assertion deleted is worse than no test.

## Conventions

- Commits: `type(scope): description`, English, conventional commits.
- Code, identifiers and comments: English only. Comments explain an invariant or
  a decision, never restate the code.
- Logging: `log/slog` only. CLI output goes through the cobra command's writers.
- Errors: wrapped with context, never swallowed.
- Trunk-based: commit to `master`, small commits, pull with rebase before push.
