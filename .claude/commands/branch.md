---
description: Cria uma branch padronizada a partir de main atualizada
argument-hint: <tipo> <descrição curta> [issue]
allowed-tools: Bash(git -C *), Bash(git status*), Bash(git branch*), Bash(git checkout*), Bash(git pull*), Bash(git fetch*)
---

Crie uma branch seguindo `.claude/rules/git-flow.md`.

Entrada do usuário: `$ARGUMENTS`
- Primeiro token = tipo (`feat`, `fix`, `chore`, `refactor`, `docs`, `test`,
  `perf`, `build`, `ci`, `style`). Omitido → assuma `feat` e confirme.
- Último token, se for número, = issue.
- O resto = descrição.

## Step 0 — Resolver o projeto

O workspace tem vários projetos. Se não estiver claro em qual criar a branch,
**pergunte** antes. Opere com `git -C <projeto>`.

## Step 1 — Checar o estado antes de mexer

```bash
git -C <projeto> status --porcelain
git -C <projeto> branch -a --list "*/<issue>"
```

- **Árvore suja** → não faça checkout. Pergunte ao usuário: commitar, stashar ou abortar.
- **Já existe branch para essa issue** → ofereça continuar nela em vez de duplicar.

## Step 2 — Montar o nome

`<tipo>/<slug>/<issue>` — sem issue, `<tipo>/<slug>`.

Slug: minúsculas, sem acento, espaços → `-`, só `a-z0-9-`, 3–6 palavras, sem `-`
duplicado ou nas pontas.

`feat` + "rotação de refresh token" + `12` → `feat/refresh-token-rotation/12`

## Step 3 — Criar a partir de main atualizada

```bash
git -C <projeto> checkout main && git -C <projeto> pull --ff-only
git -C <projeto> checkout -b <nome>
```

## Step 4 — Confirmar

Mostre projeto, branch criada e a base (`main` no commit X). Se a entrada era
ambígua, pergunte antes de criar — nunca crie branch fora do padrão.
