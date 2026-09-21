#!/usr/bin/env bash
# Every scenario, in the order a person would read them, into one report.
#
#   make simulate SCENARIO=all

set -uo pipefail
cd "$(dirname "$0")"

SCENARIOS=(happy-path idempotency security concurrency queue reversals reconciliation)

REPORT_DIR="${REPORT_DIR:-/reports}"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# One body and one summary for the whole run. Each scenario appends to them,
# which is why they are exported: a scenario left to itself would write a
# report of its own and this run would end with eight files instead of one.
REPORT_BODY="$(mktemp)"
REPORT_SUMMARY="$(mktemp)"
export REPORT_BODY REPORT_SUMMARY REPORT_DIR
trap 'rm -f "$REPORT_BODY" "$REPORT_SUMMARY"' EXIT

FAILED=()
for scenario in "${SCENARIOS[@]}"; do
  printf '\n\e[1m########  %s  ########\e[0m\n' "$scenario"
  # Not `set -e`: one failing scenario must not hide the six after it, and the
  # report at the end is the answer.
  if ! bash "./$scenario.sh"; then
    FAILED+=("$scenario")
  fi
done

printf '\n\e[1m########  summary  ########\e[0m\n'
# Formatted here rather than piped through `column`, which busybox does not
# have -- and a summary that falls back to raw pipe characters is the one
# people see most often.
awk -F'|' '
  { name = $2; ok = $3 + 0; bad = $4 + 0
    total_ok += ok; total_bad += bad
    printf "  %-18s %4d ok  %4d failed\n", name, ok, bad }
  END { printf "  %-18s %4d ok  %4d failed\n", "total", total_ok, total_bad }
' "$REPORT_SUMMARY"

if [[ -d "$REPORT_DIR" && -w "$REPORT_DIR" ]]; then
  # write_report lives in lib.sh, and lib.sh is written to be sourced by a
  # scenario. Sourcing it here would start a ninth one; running the assembly in
  # a subshell that has the finished files is what keeps one copy of the logic.
  REPORT_PATH="$REPORT_DIR/simulate-$(date -u +%Y%m%d-%H%M%S)-all.md"
  (
    REPORT_STARTED="$STARTED_AT"
    source ./report.sh
    write_report "$REPORT_PATH"
  )
  printf '\nreport: %s\n' "$REPORT_PATH"
else
  printf '\nno report: %s is not writable\n' "$REPORT_DIR"
fi

if (( ${#FAILED[@]} == 0 )); then
  printf '\e[32mall %d scenarios passed\e[0m\n' "${#SCENARIOS[@]}"
  exit 0
fi
printf '\e[31mfailed: %s\e[0m\n' "${FAILED[*]}"
exit 1
