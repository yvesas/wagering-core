---
description: Cria commits atômicos no padrão Conventional Commits, sem atribuição de IA
argument-hint: [tipo] [descrição opcional]
allowed-tools: Bash(git -C *), Bash(git status*), Bash(git diff*), Bash(git add*), Bash(git commit*), Bash(git log*), Bash(git branch*), Bash(make *), Bash(go *)
---

Crie o(s) commit(s) seguindo `.claude/rules/git-flow.md`.

Entrada do usuário: `$ARGUMENTS` (pode estar vazia).

## Step 0 — Resolver o projeto e a branch (guarda)

O workspace tem vários projetos; cada um é um repo. Descubra em qual operar:

```bash
git -C <projeto> rev-parse --show-toplevel
git -C <projeto> branch --show-current
```

- Se houver mais de um projeto com árvore suja, **pergunte** em qual commitar.
  Nunca commite em dois repos na mesma passada sem avisar.
- **Se a branch for `main` ou `master`, PARE.** Crie a branch primeiro
  (`/branch`), depois volte. O hook `guard-main-bash` bloqueia de qualquer jeito.

## Step 1 — Ler o que existe

```bash
git -C <projeto> status
git -C <projeto> diff --stat
git -C <projeto> diff --cached --stat
```

Entenda o que está sendo commitado. Não invente mudança: commite só o que existe.

## Step 2 — Rodar os gates antes de stagear

Rode o que o `CLAUDE.md` do projeto define (tipicamente lint + typecheck, e os
testes da área tocada).

**Se algum gate falhar, conserte antes.** Não commite com `--no-verify` — está
negado no `settings.json` e o hook `commit-msg` bloqueia de todo jeito.

## Step 3 — Agrupar por assunto

Se o diff mistura assuntos independentes, **proponha dividir** em commits, um por
assunto, e mostre o agrupamento antes de executar. Mudança de lockfile/dependência
vai em commit próprio.

## Step 4 — Escrever a mensagem

```
<tipo>(<escopo>): <descrição curta em inglês, imperativo, minúscula>

<corpo opcional: o porquê, não o quê — o diff já diz o quê>

Refs #<issue>   ← quando houver issue
```

- Tipos: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf`, `build`, `ci`, `style`
- Escopo = área do código (`auth`, `kanban`, `deps`). Omita se não ajudar.
- Subject < 72 caracteres.
- Se o usuário passou tipo/descrição em `$ARGUMENTS`, respeite.

⛔ **Sem rodapé de ferramenta.** Nada de `Co-Authored-By`, "Generated with",
emoji de bot ou assinatura. A mensagem termina no conteúdo.

## Step 5 — Stagear e commitar

Prefira stagear arquivo por arquivo a `git add .`. Antes de stagear, varra o
`git status` por `.env*`, credencial, chave, token e binário grande — se
aparecer, exclua e avise o usuário.

```bash
git -C <projeto> add <arquivos>
git -C <projeto> commit -m "$(cat <<'EOF'
<mensagem completa>
EOF
)"
```

## Step 6 — Resumir

```bash
git -C <projeto> log -1 --pretty=format:"%h %s"
```

Mostre: projeto, branch, hash e mensagem de cada commit, e sugira o próximo passo
(`/pr`, ou atualizar `../STATE.md` se a feature fechou).
