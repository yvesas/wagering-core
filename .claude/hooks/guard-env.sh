#!/usr/bin/env bash
# Regra 2 — Proteger segredos / .env.
# Audiência: Claude. Registrado em DOIS matchers:
#   PreToolUse Edit|Write|MultiEdit → recebe tool_input.file_path
#   PreToolUse Bash                 → recebe tool_input.command
#
# Cobrir só as ferramentas de arquivo deixava duas portas abertas: escrever o
# segredo com `echo ... > .env` e — pior — vazá-lo para o contexto com
# `cat .env`, que o `deny: Read(./.env)` do settings.json não alcança.
#
# exit 2 = bloqueia a tool call e devolve a mensagem ao Claude.
set -uo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

deny() {
  echo "⛔ $1" >&2
  echo "   Segredo vai em variável de ambiente / Secret Manager — nunca em arquivo versionado," >&2
  echo "   e o conteúdo de um .env nunca entra no contexto do agente." >&2
  echo "   Para documentar uma var nova, edite .env.example (só o nome, sem valor)." >&2
  echo "   Se precisar mesmo mexer no .env, rode você mesmo no terminal: ! <comando>" >&2
  echo "   Ver .claude/rules/secrets.md." >&2
  exit 2
}

read_hook_payload
require_json_parser

# --- Caminho 1: Edit / Write / MultiEdit -----------------------------------
file="$(json_field file_path)"
if [ -n "$file" ]; then
  case "$(basename "$file")" in
    .env.example | .env.sample | .env.template) exit 0 ;;
    .env | .env.*) deny "Edição de arquivo de ambiente bloqueada: $file" ;;
  esac
  exit 0
fi

# --- Caminho 2: Bash -------------------------------------------------------
cmd="$(json_field command)"
[ -n "$cmd" ] || exit 0

# Os arquivos de exemplo são livres: neutraliza-os antes de procurar um .env real.
probe="$(printf '%s' "$cmd" | sed -E 's/\.env\.(example|sample|template)/ENV_TEMPLATE/g')"

# O comando sequer menciona um .env real? Então não é problema nosso.
printf '%s' "$probe" | grep -qE '(^|[^[:alnum:]_.-])\.env([^[:alnum:]_.-]|$)' || exit 0

# Menciona. Só passa se for inteiramente inspeção de metadado — nada que leia
# conteúdo, escreva ou copie. A lista é curta de propósito: em dúvida, bloqueia.
if printf '%s' "$probe" | grep -qE '^[[:space:]]*(ls|stat|test|git[[:space:]]+check-ignore)([[:space:]][^;|&<>`$]*)?$'; then
  exit 0
fi

deny "Comando que toca um arquivo .env bloqueado."
