#!/usr/bin/env bash
# Regra 3 — Bloquear commit/push direto na branch principal.
# Audiência: Claude (PreToolUse em Bash). Também barra `--no-verify` e `push --force`.
# Falha aberto (exit 0) quando não consegue resolver o repo: o hook `commit-msg`
# e a branch protection do GitHub são as camadas seguintes.
set -uo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

read_hook_payload
require_json_parser
cmd="$(json_field command)"
[ -n "$cmd" ] || exit 0

# `git commit` / `git push` com quantas flags houver no meio — inclusive
# `git -C <dir> commit`, que não casa com uma busca por substring literal.
GIT_VERB='(^|[^[:alnum:]_.-])git([[:space:]]+(-C[[:space:]]+[^[:space:]]+|--[^[:space:]]+|-[[:alnum:]]+))*[[:space:]]+(commit|push)([[:space:]]|$)'
printf '%s' "$cmd" | grep -qE "$GIT_VERB" || exit 0

# --- Bypass do hook de commit-msg: sempre bloqueia -------------------------
#
# `--no-verify` é inequívoco em qualquer posição. Já `-n` só conta quando é
# flag do próprio `git commit` — procurá-lo solto na linha bloqueava comando
# nenhum a ver com git (`bash -n`, `grep -n`, `head -n 5`) sempre que a linha
# também tivesse um commit em algum lugar.
NO_VERIFY_SHORT='git([[:space:]]+-C[[:space:]]+[^[:space:]]+)?[[:space:]]+commit([[:space:]]+-[^[:space:]-][^[:space:]]*)*[[:space:]]+-n([[:space:]]|$)'
if printf '%s' "$cmd" | grep -qE -- '--no-verify' ||
  printf '%s' "$cmd" | grep -qE "$NO_VERIFY_SHORT"; then
  echo "⛔ git commit/push com --no-verify bloqueado." >&2
  echo "   O hook commit-msg valida Conventional Commits e barra atribuição de IA." >&2
  echo "   Se ele reclamou, conserte a mensagem — não contorne. Ver .claude/rules/git-flow.md." >&2
  exit 2
fi

if printf '%s' "$cmd" | grep -qE 'git push[^|;]*--force([^-]|$)'; then
  echo "⛔ 'git push --force' bloqueado. Use --force-with-lease." >&2
  exit 2
fi

# --- Resolver o repo alvo --------------------------------------------------
dir=""
if printf '%s' "$cmd" | grep -qE 'git[[:space:]]+-C[[:space:]]'; then
  dir="$(printf '%s' "$cmd" | sed -nE 's|.*git[[:space:]]+-C[[:space:]]+"?([^"[:space:]]+)"?.*|\1|p' | head -1)"
elif printf '%s' "$cmd" | grep -qE '^[[:space:]]*cd[[:space:]]'; then
  dir="$(printf '%s' "$cmd" | sed -nE 's|^[[:space:]]*cd[[:space:]]+"?([^"&;|]+)"?.*|\1|p' | head -1 | sed 's/[[:space:]]*$//')"
fi
[ -n "$dir" ] || dir="$PWD"
[ -d "$dir" ] || exit 0

# `symbolic-ref` primeiro, e não `rev-parse --abbrev-ref`: num repositório
# ainda **sem commit** o HEAD não resolve para objeto nenhum, e o `rev-parse`
# devolve a string literal "HEAD". O guard não reconhecia a branch, caía no
# `*)` e liberava — justamente na janela em que o repo é novo e um commit
# direto na main é mais provável. O `symbolic-ref` lê a ref, não o commit, e
# acerta com zero commits. Em HEAD destacado ele falha, e aí o `rev-parse`
# responde "HEAD", que não é main nem master: liberar é o certo.
branch="$(git -C "$dir" symbolic-ref --quiet --short HEAD 2>/dev/null ||
  git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '')"
case "$branch" in
  main | master) ;;
  *) exit 0 ;;
esac

# --- Publicar a main já mergeada é diferente de trabalhar nela --------------
#
# O que a regra proíbe é *criar* trabalho direto na main. Enviar para o remote
# o que já foi mergeado localmente não cria nada — e é a única forma de publicar
# num repo que ainda não tem fluxo de PR.
#
# Só passa quando é fast-forward puro: a main local contém o remote inteiro e
# apenas o adianta. Se o remote tiver commit que a local não tem, o push
# reescreveria história alheia e continua bloqueado.
if printf '%s' "$cmd" | grep -qE 'git([[:space:]]+-C[[:space:]]+[^[:space:]]+)?[[:space:]]+push'; then
  upstream="$(git -C "$dir" rev-parse --abbrev-ref '@{upstream}' 2>/dev/null || echo '')"

  if [ -z "$upstream" ]; then
    # Primeiro push da branch: não há remote para reescrever.
    exit 0
  fi

  behind="$(git -C "$dir" rev-list --count "HEAD..$upstream" 2>/dev/null || echo 1)"
  if [ "$behind" = "0" ]; then
    exit 0
  fi

  echo "⛔ push na '$branch' bloqueado: a branch local está atrás do remote." >&2
  echo "   Há $behind commit(s) no remote que você não tem. Rode 'git pull --ff-only'" >&2
  echo "   antes de publicar. Ver .claude/rules/git-flow.md." >&2
  exit 2
fi

echo "⛔ git commit direto na '$branch' bloqueado." >&2
echo "   Crie uma branch: <tipo>/<slug>/<issue> (ex.: feat/user-auth/12)." >&2
echo "   Ver .claude/rules/git-flow.md." >&2
exit 2
