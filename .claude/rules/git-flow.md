# Regra — Fluxo Git

> Enforçada por: o hook `commit-msg` em `.githooks/`, o hook `guard-main-bash`
> do Claude, os `deny` do `.claude/settings.json` e branch protection no GitHub.
>
> `core.hooksPath` é configuração local do git e não vem no clone: rode
> `make setup` logo após clonar, ou o `commit-msg` fica versionado sem nunca
> executar.

## Atribuição de IA — inegociável

**Nunca** inserir em mensagem de commit, título ou corpo de PR, comentário de
issue ou release note:

- `Co-Authored-By:` (qualquer co-autor de ferramenta)
- `🤖 Generated with [Claude Code](https://claude.com/claude-code)`
- "Generated with / by …", assinatura de ferramenta, endereço de bot

O código é do time e a responsabilidade é de quem committa. O hook `commit-msg`
rejeita; **não contorne com `--no-verify`** — o `settings.json` também nega isso.

## Branches

- Tudo sai de `main` e volta para `main` via Pull Request. Nunca commit ou push
  direto na `main`.
- Formato: `<tipo>/<slug-curto>/<issue>` → `feat/refresh-token/12`
- Sem issue, omita o sufixo: `chore/bump-deps`
- Slug em kebab-case, 3–6 palavras, sem acento.

## Commits

- **Conventional Commits**: `<tipo>(<escopo>): <descrição>`
- Tipos: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf`, `build`,
  `ci`, `style`
- Descrição em **inglês**, imperativo, minúscula, subject < 72 caracteres.
- `feat(auth): add refresh token rotation` · `fix(kanban): keep sprint counters on conclude`
- Commits **atômicos e agrupados por assunto**. Se o diff mistura assuntos,
  divida. Poucos commits coerentes > um commit por arquivo.
- Rodar `make check` **antes** de commitar — `gofmt`, `go vet`, `domain-check`,
  testes e `-race`. Falhou, conserta; não bypassa.

## Pull Requests

- Sempre contra `main`, **squash merge**. O título do PR vira o commit na `main`,
  então segue o mesmo formato de commit.
- **Ao mergear, informe o corpo do commit explicitamente** (`gh pr merge --squash
  --subject … --body …`, ou editando o campo na interface). Deixado em branco, o
  GitHub monta o corpo sozinho e acrescenta `Co-authored-by:` de quem autorou os
  commits — o que põe atribuição na `main` sem passar por hook nenhum, porque
  merge pelo servidor não roda `commit-msg`.
- **Rebase, nunca merge** de `main` na branch: `git fetch origin && git rebase origin/main`.
- Se o rebase reescreveu commits já enviados: `--force-with-lease`, nunca `--force`.
- Um assunto por PR. Referenciar a issue no título ou corpo.
- **"Um assunto" não é "um arquivo".** Cada PR paga um pipeline de CI inteiro,
  então mudança pequena espera e vai junto com a do mesmo assunto — agrupar
  economiza mais minuto que qualquer ajuste dentro do workflow. PR não é
  checkpoint: rode `make check` localmente e empurre quando o assunto fechar.
  Ver `.claude/rules/ci-and-minutes.md`.
- Corpo do PR: o que, por quê, como testar. Sem footer de ferramenta.

## Releases

Quando o projeto usa deploy por tag: `v<semver>-<AAAA-MM-DD>`, criada por script
versionado (nunca versão calculada à mão), com `CHANGELOG.md` atualizado e
mergeado **antes** da tag.
