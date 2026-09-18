---
description: Abre o Pull Request da branch atual contra main, sem atribuição de IA
argument-hint: [contexto extra / número da issue]
allowed-tools: Bash(git -C *), Bash(git log*), Bash(git diff*), Bash(git status*), Bash(git branch*), Bash(git fetch*), Bash(git rebase*), Bash(git push*), Bash(gh pr*)
---

Abra o PR seguindo `.claude/rules/git-flow.md`.

Contexto extra do usuário: `$ARGUMENTS`

## Regras de atribuição

⛔ O corpo do PR **não** leva `🤖 Generated with Claude Code`, `Co-Authored-By`
nem qualquer assinatura de ferramenta. O corpo do PR alimenta o commit de squash
na `main` — o que entra aqui vira histórico.

## Step 0 — Preflight

```bash
git -C <projeto> branch --show-current
git -C <projeto> status --porcelain
gh pr view --json url,state 2>/dev/null
```

- **Em `main`?** PARE — crie a branch primeiro (`/branch`).
- **Mudança não commitada?** Pergunte antes de seguir (sugira `/commit`).
- **PR já aberto para essa branch?** Só empurre os commits novos e mostre a URL.
  Não crie duplicado.

## Step 1 — Sincronizar com main (rebase, nunca merge)

```bash
git -C <projeto> fetch origin
git -C <projeto> rebase origin/main
```

- Conflito: resolva o arquivo, `git add <arquivo>`, `git rebase --continue`.
  Para desistir, `git rebase --abort` e avise o usuário.
- **NUNCA** `git merge main` na branch — não queremos merge commit.

## Step 2 — Push

```bash
git -C <projeto> push -u origin $(git -C <projeto> branch --show-current)
```

Se o rebase reescreveu commits já enviados: `--force-with-lease`. **Nunca `--force`.**

## Step 3 — Reunir os commits

```bash
git -C <projeto> log origin/main..HEAD --pretty=format:"%s%n%n%b%n----%n" --reverse
git -C <projeto> diff origin/main...HEAD --stat
```

## Step 4 — Título

O título vira o commit de squash na `main`, então segue o formato de commit:
`<tipo>(<escopo>): <descrição>`.

- **Um commit** → use o subject dele como está.
- **Vários commits** → resuma em um subject só, no mesmo formato.

## Step 5 — Corpo

```markdown
## O que
<resumo do escopo — um assunto só>

## Por quê
<motivação; link da issue>

## Como testar
<passos concretos>

## Checklist
- [x] Lint, typecheck e testes verdes
- [x] Escopo focado, título no padrão de commit
- [x] Assunto fechado — não é um PR de checkpoint (`.claude/rules/ci-and-minutes.md`)
- [ ] Issue referenciada
- [ ] `../STATE.md` e `../TASKS.md` atualizados (ou N/A — motivo: ___)
- [ ] ADR aberto em `docs/adr/` se houve decisão estrutural (ou N/A)
```

## Step 6 — Criar

```bash
gh pr create --base main --head <branch> \
  --title "<título>" \
  --body "$(cat <<'EOF'
<corpo>
EOF
)"
```

## Step 7 — Resumo

Mostre: URL do PR, branch → main (squash após CI), número de commits, e sinalize
todo item do checklist que você **não** conseguiu confirmar sozinho. Sugira
acompanhar com `gh pr checks <branch> --watch`.

## Ao mergear: sempre com `--subject` e `--body` explícitos

```bash
gh pr merge <n> --squash --delete-branch \
  --subject "<tipo>(<escopo>): <descrição>" \
  --body "<o porquê, em prosa>"
```

**Sem `--body`, o GitHub gera o corpo sozinho — e acrescenta
`Co-authored-by:` de quem autorou os commits.** Num PR do dependabot isso põe
`Co-authored-by: dependabot[bot]` na `main`, violando a regra de atribuição sem
que hook nenhum veja: merge pelo servidor não roda `commit-msg`.

Isso não é hipótese. Num único dia, cinco merges sem `--body` entraram com o
trailer, e os dois feitos com `--body` não. É a diferença entre passar o corpo e
deixar o GitHub inventá-lo.

O mesmo vale ao mergear pela interface do GitHub: apague o
`Co-authored-by:` que ele preenche antes de confirmar.
