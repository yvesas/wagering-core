---
description: Sincroniza ../STATE.md e ../TASKS.md com a realidade do código
argument-hint: [projeto]
allowed-tools: Read, Write, Edit, Glob, Grep, Bash(git -C *), Bash(git log*), Bash(git status*), Bash(make *), Bash(go *), Bash(ls*)
---

Atualize a memória do projeto — `../STATE.md` e `../TASKS.md`.

Entrada do usuário: `$ARGUMENTS`

## A regra que faz esse comando valer

**Confira contra o código, não contra os specs.** Spec descreve o que se queria;
`../STATE.md` descreve o que existe. Quando divergirem, o código ganha — e a
divergência vira uma linha no `../STATE.md`, não uma correção silenciosa.

## Step 1 — Levantar a realidade

```bash
git -C <projeto> branch --show-current
git -C <projeto> log --oneline -30
git -C <projeto> status --porcelain
ls specs/features
```

Rode os gates do projeto para medir a saúde de verdade (typecheck, lint, testes)
e anote os números — não escreva "suíte verde" sem ter rodado.

Para cada feature com `tasks.md`, confira se as tasks marcadas `[x]` de fato
existem no código. Task marcada e não implementada é o erro mais caro aqui.

## Step 2 — Reescrever o `../STATE.md`

Estrutura:

```markdown
# Estado atual do projeto

**Última atualização:** <AAAA-MM-DD>
**Branch:** `<branch>` (<N> commits à frente de main)
**Status:** <uma linha>

## O que está implementado
<por área/produto, com o caminho no código>

## O que está pendente
<o que falta, com o bloqueio nomeado — "aguardando credencial X do Y">

## Saúde da suíte (medido em <data>)
<typecheck / testes N de M / lint — números reais, e a causa das falhas>

## Decisões recentes
<D-XXX-NNN — título, o que foi decidido e o trade-off aceito>

## Decisões abertas
<pergunta + de quem depende a resposta>
```

Se o documento estava desatualizado há muito tempo, **diga isso no topo** e
registre contra o que a revisão foi conferida (schema, código, histórico).

## Step 3 — Atualizar o `../TASKS.md`

Mova para "Concluído" o que saiu, com a linha do que foi entregue. Ajuste "Em
desenvolvimento" e "Planejado". O roadmap deve poder ser lido sozinho por quem
chega novo.

## Step 4 — Promover o que é permanente

Decisão estrutural registrada no `../STATE.md` que vai sobreviver a várias
reescritas → abra um ADR em `docs/adr/NNNN-title.md` e deixe o `../STATE.md`
apontando para ele. `../STATE.md` é memória de trabalho; ADR é permanente.

## Step 5 — Resumir

Mostre o diff conceitual: o que mudou de status, o que foi promovido a ADR, e
que pendências continuam bloqueadas e por quem.
