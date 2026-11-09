# `make verify` is the definition of done.
#
# Every quality property is enforced here or in the pipeline, never by asking
# for care: if a rule matters and no target checks it, it is not enforced.

GOLANGCI_VERSION := v2.14.0
GOLANGCI_BIN     := $(CURDIR)/.tools/golangci-lint

.PHONY: help
help:
	@echo "make tools          fetch the pinned golangci-lint into .tools/"
	@echo "make hooks          point git at .githooks (pre-commit, pre-push)"
	@echo "make build          build bin/lodog"
	@echo "make report-preview  write the report page of the synthetic fixtures to bin/report-preview/"
	@echo "make test-report-ui  browser e2e of the report page (e2e/report-ui, needs node)"
	@echo "make verify-fast    static checks + unit tests, no Docker"
	@echo "make verify         definition of done"

.PHONY: tools
tools:
	@if [ -x "$(GOLANGCI_BIN)" ] && \
	    "$(GOLANGCI_BIN)" --version 2>/dev/null | grep -q "$(patsubst v%,%,$(GOLANGCI_VERSION))"; then \
		echo "tools: golangci-lint $(GOLANGCI_VERSION) already installed"; \
	else \
		scripts/install-golangci.sh $(GOLANGCI_VERSION) "$(CURDIR)/.tools"; \
	fi

# Version-controlled hooks: .git/hooks is per clone, so a hook kept there would
# exist only on whichever machine installed it.
.PHONY: hooks
hooks:
	@git config core.hooksPath .githooks
	@echo "hooks installed: pre-commit runs lint and format, pre-push runs build and tests"

.PHONY: build
build:
	go build -o bin/lodog ./cmd/lodog

# The report page on synthetic data (internal/report/testdata/preview): a full
# L0-L3 chain, every level pressed by the gate, an incomplete run, only L0 and
# L3, and no run at all. Open the files to see a change to the page.
REPORT_PREVIEWS := full:shop all-capped:shop incomplete:shop l0-l3:shop-l0-l3 none:shop

.PHONY: report-preview
report-preview:
	@mkdir -p bin/report-preview
	@cd internal/report && for s in $(REPORT_PREVIEWS); do \
		name=$${s%%:*}; case=$${s##*:}; \
		go run ../../cmd/lodog report --case testdata/preview/$$case --runs testdata/preview/scenarios/$$name \
			-o $(CURDIR)/bin/report-preview/$$name.html || exit 1; \
	done

.PHONY: verify-tools
verify-tools:
	@[ -x "$(GOLANGCI_BIN)" ] || { echo "golangci-lint missing: run 'make tools'"; exit 1; }

.PHONY: verify-fmt
verify-fmt: verify-tools
	@out="$$($(GOLANGCI_BIN) fmt --diff)"; \
	if [ -n "$$out" ]; then echo "$$out"; echo "verify-fmt: run '$(GOLANGCI_BIN) fmt'"; exit 1; fi

.PHONY: verify-lint
verify-lint: verify-tools
	go vet ./...
	go vet -tags=acceptance ./test/...
	$(GOLANGCI_BIN) run

.PHONY: verify-mod
verify-mod:
	@go mod tidy -diff || { echo "verify-mod: run 'go mod tidy'"; exit 1; }

# The race detector sleeps a second when each test binary exits; there is
# nothing to flush here, so skip it (it would be 1s on every package).
.PHONY: test-unit
test-unit:
	GORACE=atexit_sleep_ms=0 go test -race -count=1 ./...

# What the hooks run. A hook is a convenience, not a gate: `git commit -n`
# skips it, so the pipeline runs every one of these again.
.PHONY: verify-precommit
verify-precommit: verify-fmt verify-lint verify-mod

.PHONY: verify-prepush
verify-prepush: build test-unit

.PHONY: verify-fast
verify-fast: verify-precommit verify-prepush

# The definition of done: the same steps as verify-fast, held to the time
# budget in CLAUDE.md. It does not run test-accept: the pipeline has no environment tool
# env.
.PHONY: verify
verify:
	@scripts/verify-budget.sh

# Browser e2e of the report page: Playwright opens pages made by the real
# `lodog report` and operates them the way the demo does. Not in verify (a
# browser and node are not in its 60 second budget); the pipeline runs it as
# its own job. First run downloads Chromium (about 100 MB, cached after that).
# LODOG_BIN names a lodog binary to use instead of building one.
.PHONY: test-report-ui
test-report-ui: build
	cd e2e/report-ui && npm ci && npx playwright install chromium && npx playwright test

# Against the Backoffice benchmark env (cases/backoffice/README.md). Not in verify:
# the pipeline has no environment tool env. Run it before closing gateway work.
.PHONY: test-accept
test-accept:
	go test -tags=acceptance -count=1 ./test/acceptance/...
