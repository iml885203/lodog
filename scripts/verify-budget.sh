#!/bin/sh
# Runs the verify steps and fails when they take longer than the time budget
# (CLAUDE.md "Time budget"), naming the slowest test packages so the cause is
# visible. verify-fast does not go through here and has no limit.
set -u

budget="${VERIFY_BUDGET:-60}"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
start="$(date +%s)"

status=0
make verify-precommit && make build && make test-unit >"$out" 2>&1 || status=1
cat "$out"
elapsed=$(($(date +%s) - start))

echo "verify: ${elapsed}s (budget ${budget}s)"
if [ "$status" -ne 0 ]; then
	exit 1
fi
if [ "$elapsed" -gt "$budget" ]; then
	echo "verify: over the time budget. Slowest test packages:"
	awk '$1 == "ok" && $3 ~ /s$/ { t = $3; sub(/s$/, "", t); print t, $2 }' "$out" | sort -rn | head -5
	echo "verify: a package over 5s is a design signal: use a fake clock or a short timeout instead of waiting."
	exit 1
fi
