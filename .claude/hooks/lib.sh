#!/usr/bin/env bash
# Helpers compartilhados pelos hooks deste projeto.
#
# O payload JSON da tool chega no stdin, e stdin só pode ser consumido uma vez.
#
# `campo="$(json_field x)"` roda em subshell: se o parse lesse o stdin ali
# dentro, o subshell levaria o payload embora e a chamada seguinte voltaria
# vazia — o hook liberaria tudo sem avisar. Por isso o payload é lido pelo
# shell PAI, uma vez, e as consultas trabalham em cima da variável.
#
# Todo hook que usa json_field precisa chamar read_hook_payload primeiro, fora
# de qualquer $(...).

HOOK_PAYLOAD=""

read_hook_payload() {
  HOOK_PAYLOAD="$(cat)"
}

# Qual parser JSON existe nesta máquina. python3 primeiro: num projeto Go, node
# é que é a dependência estranha — e a versão anterior deste arquivo dependia só
# dele.
json_parser() {
  if command -v python3 >/dev/null 2>&1; then
    printf 'python3'
  elif command -v node >/dev/null 2>&1; then
    printf 'node'
  else
    return 1
  fi
}

# Nenhum parser disponível é **falha da guarda**, não ausência de risco: sem ler
# o payload o hook não sabe o que está sendo pedido. Um `json_field` vazio faz
# todo guard cair no `[ -n "$x" ] || exit 0` e liberar em silêncio — que é
# exatamente como uma guarda desaparece sem ninguém notar.
#
# Guards chamam isto logo após read_hook_payload e morrem FECHADOS.
require_json_parser() {
  json_parser >/dev/null 2>&1 && return 0
  echo "⛔ guarda inoperante: nenhum parser JSON (python3 ou node) nesta máquina." >&2
  echo "   Sem ler o payload da tool o hook não sabe o que autorizar, então bloqueia." >&2
  echo "   Instale python3, ou desligue o hook conscientemente em .claude/settings.json." >&2
  exit 2
}

# Lê um campo de tool_input do payload já capturado. Uso: json_field file_path
json_field() {
  case "$(json_parser 2>/dev/null)" in
    python3)
      printf '%s' "$HOOK_PAYLOAD" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
v = (d.get("tool_input") or {}).get(sys.argv[1], "")
sys.stdout.write(v if isinstance(v, str) else "")
' "$1"
      ;;
    node)
      printf '%s' "$HOOK_PAYLOAD" | node -e '
        const key = process.argv[1];
        let d = "";
        process.stdin.on("data", c => (d += c)).on("end", () => {
          try {
            const j = JSON.parse(d || "{}");
            const v = (j.tool_input && j.tool_input[key]) || "";
            process.stdout.write(typeof v === "string" ? v : "");
          } catch { process.stdout.write(""); }
        });
      ' "$1"
      ;;
    *) printf '' ;;
  esac
}

# Raiz do projeto (a pasta que contém este .claude/).
project_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd
}
