# `.claude/` — configuração do agente neste projeto

Este diretório foi **bifurcado** do baseline `yas-claude-base` v0.6.4 e agora
pertence ao projeto. `update-baseline.sh` foi removido de propósito: ele
sobrescreveria tudo o que está aqui na próxima execução.

## Por que bifurcar

O baseline assume TypeScript, e a suposição não era cosmética — três guardas
estavam mortas ou perigosas neste projeto:

| Antes | O problema |
|---|---|
| `hooks/typecheck.sh` | `[ -f "$proj/package.json" ] \|\| return 0` — retornava zero antes de checar coisa alguma. O gate do `Stop` nunca rodou. |
| `hooks/format-lint.sh` | Só `.ts`/`.js`, e exigia `package.json`. Idem. |
| `hooks/lib.sh` | Parseava o payload com **node**. Sem node, `json_field` volta vazio, todo guard cai no `[ -n "$x" ] \|\| exit 0` e **libera tudo em silêncio** — inclusive `cat .env`. |
| `hooks/guard-main-bash.sh` | Resolvia a branch com `rev-parse --abbrev-ref HEAD`, que devolve `"HEAD"` num repo **sem commits**. O guard não reconhecia a `main` e liberava — na janela em que o repo é novo e o commit direto é mais provável. |
| `settings.json` | 11 permissões de `npm`/`npx`/`pnpm`, nenhuma de `go` ou `make`. |
| `rules/code-style.md` | `strict: true`, Zod, ESM, `tsc --noEmit`. Nada aplicável. |
| `rules/docs-e-specs.md` | Mandava `STATE.md` e `ROADMAP.md` para `specs/project/`, e aqui a execução mora fora do repositório. |

Regra que não se aplica é pior que regra ausente: ela ocupa o lugar da regra
certa e ninguém percebe que o gate sumiu.

## O que mudou

**Hooks** — `format-lint.sh` e `typecheck.sh` removidos. No lugar do segundo,
`build-vet.sh`: com a árvore suja roda `gofmt -l`, `go build ./...`,
`go vet ./...` e a golden rule, e devolve `exit 2` ao Claude se qualquer um
falhar. `lib.sh` passou a tentar `python3` antes de `node` e ganhou
`require_json_parser`, que faz os guards **falharem fechados** quando não há
parser nenhum.

**Rules** — nomes em inglês (`docs-and-specs.md`, `ci-and-minutes.md`);
`code-style.md`, `testing.md` e `ci-and-minutes.md` reescritas para Go;
`docs-and-specs.md` reescrita para a divisão real de três lugares, com a
execução fora do repositório.

**Commands** — `new-project.md` removido: ele instalava o baseline do qual
acabamos de bifurcar. Os demais foram retargetados de `specs/project/STATE.md`
e `ROADMAP.md` para `../STATE.md` e `../TASKS.md`.

**Skill `spec-driven`** — a seção de adaptação local descreve este projeto, e as
referências apontam para os arquivos que existem.

## O custo, que é real

Correção feita no `yas-claude-base` não chega mais aqui sozinha. Três das que
estão acima — o parser que falha aberto, o gate que exigia `package.json` e a
branch não reconhecida em repo sem commits — são bugs **do baseline**, não só
deste projeto, e valeria levá-las de volta para lá.

## Como verificar que as guardas estão vivas

```sh
printf '{"tool_input":{"command":"cat .env"}}' | .claude/hooks/guard-env.sh;      echo $?   # 2
printf '{"tool_input":{"command":"ls -la"}}'   | .claude/hooks/guard-env.sh;      echo $?   # 0
printf '{"tool_input":{"command":"git commit -m x --no-verify"}}' | \
  .claude/hooks/guard-main-bash.sh; echo $?                                                 # 2
printf '{"tool_input":{"command":"git commit -m x"}}' | \
  .claude/hooks/guard-main-bash.sh; echo $?     # 2 na main — inclusive sem commits ainda
.claude/hooks/build-vet.sh </dev/null; echo $?                                              # 0 com a árvore sã
make setup                                                                                  # verifica o commit-msg
```

Hook que ninguém exercita é hook que se quebra em silêncio — foi exatamente como
os dois anteriores morreram.
