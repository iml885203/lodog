# Report page e2e

Playwright opens the report pages that the real `lodog report` writes and
operates them the way the demo does. It is the only test that runs the page's
script in a browser; `internal/report` tests read the HTML as text.

## Run it

```
make test-report-ui
```

This builds `bin/lodog`, runs `npm ci` here, downloads Chromium on the first run
(about 100 MB, kept in the Playwright cache) and runs the tests, about 15
seconds. Needs node 20 or later. `make verify` does not run it: a browser does
not fit the 60 second budget. The pipeline runs it as the `report-ui` job.

To run one file, or to watch it, from this folder:

```
LODOG_BIN=../../bin/lodog npx playwright test tests/improve.spec.js --headed
npx playwright show-trace results/artifacts/<test>/trace.zip   # after a failure
```

`LODOG_BIN` is the lodog binary that makes the pages (default `bin/lodog`);
`REPORT_UI_DIR` is where they are written (default `bin/report-ui/`, open them
to look).

Workers: `playwright.config.js` sizes them from the container's cgroup cpu
limit, and uses two in CI when the pod states none (the runner pod shows 16
cores but gets a share of them; with eight workers every test ran ten times
slower and timed out). The job prints what the pod reports before it starts.

## What it covers

| File | What it holds |
| --- | --- |
| `tests/views.spec.js` | the two views, the L0 to L3 chips, the dial against the page data and the scores table |
| `tests/accordion.spec.js` | categories and their check counts, a check opening, audit details, the folds |
| `tests/improve.spec.js` | the Improve dialog (open, close three ways, both paths) and what Copy all puts on the clipboard |
| `tests/lang.spec.js` | zh/en: same keys in both, no key shown as text, the choice kept |
| `tests/replay.spec.js` | the trap replay: one tab per task, the open side against the guarded side |
| `tests/nobench.spec.js` | a case made without a bench (#67) and a case with no run |
| `tests/clean.spec.js` | every page, both languages, every level, every accordion open: no `NaN`, `undefined`, `<no value>` |
| `tests/review.spec.js` | the review page (`lodog review ask --html`): how to answer, each question in plain words, items folded, the applied line, zh/en, Copy |
| `tests/motion.spec.js` | the same end states with motion on |

Every test also fails on a console error or warning, an uncaught exception or a
failed request (`problems` in `tests/fixtures.js`).

## Pages

`pages.js` lists them. `global-setup.js` writes each once with `lodog report`.

- Fixtures from `internal/report/testdata`: the shop case with every
  level run, every level capped, an incomplete run, only L0 and L3, no run, and
  the recorded L0 runs.
- `cases/backoffice` and `cases/keycloak` with the runs committed under
  `results/`. A re-run replaces them, so no test names a score. A test checks
  that the number on the page is the number in the page's own data and in its
  scores table.
- A case from `lodog init` with no bench.

- The review page (`review`), made by `lodog review ask` from a small policy and workflows draft with a review log beside it. It is not in `pages.js`: the sweeps there read report-page elements.

A new page that has a level chip, a trap or a dialog should be added to
`pages.js`; the sweeps in `clean.spec.js` and `lang.spec.js` take every entry.

## Keeping it honest

Each of these changes to the page turns the suite red (checked by hand when the
suite was written): removing the view switch's click handler, deleting a key
from the English dictionary, making Copy all copy an empty string.

When `@playwright/test` is bumped in `package.json`, bump the image tag of the
`report-ui` job in `.github/workflows/ci.yml` to the same version.
