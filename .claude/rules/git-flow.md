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

- Sempre contra `main`. **Squash é o padrão**, e aí o título do PR vira o commit
  na `main`, seguindo o mesmo formato de commit.
- **Merge commit quando os commits da branch são assuntos distintos e cada um
  passa sozinho.** O squash existe para a `main` não herdar commit de
  checkpoint — "wip", "fix typo", "agora vai". Quando a branch traz, por
  exemplo, um ADR, três camadas e a documentação, squashar não limpa nada:
  joga fora um `git bisect` que funcionava.

  A permissão tem um preço, e ele é verificável. Antes de pedir merge commit,
  prove que cada um passa isolado:

  ```bash
  for sha in $(git rev-list main..HEAD); do
    wt=$(mktemp -d)
    git worktree add -q --detach "$wt" "$sha"
    (cd "$wt" && make check >/dev/null 2>&1) \
      && echo "✓ $sha" || echo "⛔ $sha"
    git worktree remove --force "$wt"
  done
  ```

  Um `⛔` na lista significa que os commits não eram atômicos, e aí **squash**.
  Sem essa verificação, "são assuntos distintos" é opinião, e a `main` herda um
  histórico que parece bissectável e não é.
- **Ao mergear, informe o corpo do commit explicitamente** — vale para
  `--squash` e para `--merge` (`gh pr merge --merge --subject … --body …`, ou
  editando o campo na interface). Deixado em branco, o GitHub monta o corpo
  sozinho e acrescenta `Co-authored-by:` de quem autorou os commits — o que põe
  atribuição na `main` sem passar por hook nenhum, porque merge pelo servidor
  não roda `commit-msg`. Confira depois: `git log -1 --format='%B' origin/main`.
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
