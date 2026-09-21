#!/usr/bin/env bash
# Turning a run into a file somebody can read later.
#
# Its own file because two things need it: a scenario finishing on its own, and
# all.sh finishing seven of them into one report. Sourcing lib.sh from all.sh
# would start an eighth scenario, so what both share lives here.
#
# Source it, do not run it. It expects REPORT_BODY, REPORT_SUMMARY and
# REPORT_STARTED to be set.

REPORT_DIR="${REPORT_DIR:-/reports}"

# reportable says whether there is somewhere to write. Run outside the compose
# service there may not be, and a missing directory must not fail a scenario
# that otherwise passed.
reportable() { [[ -d "$REPORT_DIR" && -w "$REPORT_DIR" ]]; }

# report_path names a file nobody has to overwrite to keep. Runs are worth
# comparing, and a fixed name makes the last one the only one.
report_path() {
  printf '%s/simulate-%s-%s.md' "$REPORT_DIR" "$(date -u +%Y%m%d-%H%M%S)" "$1"
}

# write_report assembles title, summary table and body into one file.
#
# The table is built at the end rather than as it goes, because a total is only
# a total once everything has run -- and a report whose header disagrees with
# its body is worse than no report.
write_report() {
  local path=$1 passed failed
  passed="$(awk -F'|' '{ s += $3 } END { print s + 0 }' "$REPORT_SUMMARY")"
  failed="$(awk -F'|' '{ s += $4 } END { print s + 0 }' "$REPORT_SUMMARY")"

  {
    printf '# Simulação — %s\n\n' "$REPORT_STARTED"
    printf 'Gerado por `make simulate`. Cada linha abaixo é uma verificação\n'
    printf 'contra o sistema rodando: a API em container, o Keycloak emitindo\n'
    printf 'token de verdade, o emulador de fila — não contra um mock.\n\n'

    printf '| cenário | ok | falhas |\n|---|---:|---:|\n'
    cat "$REPORT_SUMMARY"
    printf '| **total** | **%s** | **%s** |\n' "$passed" "$failed"

    if [[ "$failed" != "0" ]]; then
      printf '\n> **%s verificações falharam.** Procure por `NOT` abaixo: cada\n' "$failed"
      printf '> uma traz o valor obtido e o esperado.\n'
    fi

    cat "$REPORT_BODY"

    printf '\n---\n\n'
    printf 'O que cada cenário prova está em `docs/runbooks/simulating-clients.md`.\n'
  } > "$path"
}
