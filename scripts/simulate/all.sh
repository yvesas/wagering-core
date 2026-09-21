#!/usr/bin/env bash
# Every scenario, in the order a person would read them.
#
#   make simulate SCENARIO=all

set -uo pipefail
cd "$(dirname "$0")"

SCENARIOS=(happy-path idempotency security concurrency queue reversals reconciliation)
FAILED=()

for scenario in "${SCENARIOS[@]}"; do
  printf '\n\e[1m########  %s  ########\e[0m\n' "$scenario"
  # Not `set -e`: one failing scenario must not hide the six after it, and the
  # summary at the end is the answer.
  if ! bash "./$scenario.sh"; then
    FAILED+=("$scenario")
  fi
done

printf '\n\e[1m########  summary  ########\e[0m\n'
if (( ${#FAILED[@]} == 0 )); then
  printf '\e[32mall %d scenarios passed\e[0m\n' "${#SCENARIOS[@]}"
  exit 0
fi
printf '\e[31mfailed: %s\e[0m\n' "${FAILED[*]}"
exit 1
