#!/usr/bin/env bash
# Gate do Stop — trabalho não está pronto se não compila.
# Audiência: Claude (hook Stop).
#
# Substitui o `typecheck.sh` do baseline, que exigia `package.json` e por isso
# retornava zero antes de checar coisa alguma num projeto Go: um gate que
# parecia ligado e nunca rodou.
#
# Só roda com a árvore suja — checar projeto sem alteração é caro e ruidoso.
# exit 2 = devolve o erro ao Claude.
set -uo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(project_root)"

[ -f "$ROOT/go.mod" ] || exit 0
command -v go >/dev/null 2>&1 || exit 0
[ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ] || exit 0

cd "$ROOT" || exit 0
problems=""

# --- Formato ---------------------------------------------------------------
if unformatted="$(gofmt -l . 2>/dev/null)" && [ -n "$unformatted" ]; then
  echo "⛔ fora do formato (rode 'make fmt'):" >&2
  printf '%s\n' "$unformatted" >&2
  problems="$problems gofmt"
fi

# --- Compilação ------------------------------------------------------------
if ! out="$(go build ./... 2>&1)"; then
  echo "⛔ 'go build ./...' falhou:" >&2
  printf '%s\n' "$out" | tail -30 >&2
  problems="$problems build"
fi

# --- Análise estática ------------------------------------------------------
if ! out="$(go vet ./... 2>&1)"; then
  echo "⛔ 'go vet ./...' falhou:" >&2
  printf '%s\n' "$out" | tail -30 >&2
  problems="$problems vet"
fi

# --- Golden rule -----------------------------------------------------------
#
# Só acusa quando o grep REALMENTE casa. Se o `go list` falhar por outro motivo
# — módulo sem dependência baixada, por exemplo — o build acima já terá
# reclamado, e transformar isso aqui num segundo erro só confundiria.
if leaked="$(go list -deps ./internal/domain/... 2>/dev/null |
  grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go')" && [ -n "$leaked" ]; then
  echo "⛔ o domínio importa infraestrutura:" >&2
  printf '%s\n' "$leaked" >&2
  echo "   Ver docs/adr/0001-hexagonal-architecture.md." >&2
  problems="$problems domain"
fi

if [ -n "$problems" ]; then
  echo "⛔ pendências:$problems — o trabalho não está pronto." >&2
  echo "   Rode 'make check'. Ver .claude/rules/code-style.md." >&2
  exit 2
fi
exit 0
