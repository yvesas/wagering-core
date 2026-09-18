---
description: Especifica uma feature nova em specs/features/NNNN-slug/ (via skill spec-driven)
argument-hint: <descrição da feature>
allowed-tools: Read, Write, Edit, Glob, Grep, Bash(ls*), Bash(git -C *)
---

Especifique uma feature usando a skill **`spec-driven`** (fase Specify).

Entrada do usuário: `$ARGUMENTS`

## Step 0 — Contexto

1. Resolva o projeto (se ambíguo, pergunte).
2. Carregue `specs/project/PROJECT.md`, `../TASKS.md` e `../STATE.md`. Se não
   existirem, o projeto ainda não foi inicializado — rode `initialize project`
   pela skill antes.
3. Descubra o próximo número:

```bash
ls specs/features | sort | tail -3
```

Numeração sequencial de 4 dígitos, contínua no projeto. Nunca renumere.

## Step 1 — Dimensionar antes de escrever

A skill auto-dimensiona a profundidade. Aplique a tabela dela:

| Escopo | O que produzir |
|---|---|
| ≤3 arquivos, uma frase | **Quick mode** — `specs/quick/NNN-slug/`, sem pipeline |
| Feature clara, <10 tasks | `spec.md` breve; design e tasks implícitos |
| Multi-componente | `spec.md` com IDs + `design.md` + `tasks.md` |
| Ambíguo / domínio novo | acrescenta a fase **discuss** e UAT interativo |

Não produza `design.md` e `tasks.md` "por completude" — arquivo vazio de
conteúdo real custa contexto e envelhece.

## Step 2 — Especificar

Siga `references/specify.md` da skill. O essencial:

- **Seja parceiro de raciocínio, não entrevistador.** Comece aberto, siga a
  energia do usuário.
- **Confronte vagueza.** "Bom" é o quê? "Usuário" é quem? "Simples" é como?
- Requisitos com **ID rastreável** (`REQ-001`) e critério de aceite no formato
  **WHEN / THEN / SHALL**.
- Seção **Out of Scope** com o motivo de cada exclusão.
- Área cinzenta com mais de um caminho válido → dispare a fase **discuss** e
  grave a resposta em `context.md`. Não escolha sozinho e siga.
- Decisão tomada no caminho recebe ID (`D-XXX-001`) e vai para o `../STATE.md`;
  se for estrutural, ADR em `docs/adr/`.

## Step 3 — Fechar o laço

- Adicione a feature ao `../TASKS.md` (seção em desenvolvimento / planejado).
- Registre no `../STATE.md` o que ficou **pendente de terceiro** (decisão de
  produto, acesso, credencial) — explicitamente, com quem depende.
- Mostre ao usuário o caminho do `spec.md`, os REQs criados e qual a próxima
  fase (design, tasks ou já implementar).
