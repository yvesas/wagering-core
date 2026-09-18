# Regra — Onde vive cada documento

> A divisão evita o problema clássico: plano e verdade misturados no mesmo
> arquivo, os dois envelhecendo juntos até ninguém confiar em nenhum.

## A divisão em uma linha

**`specs/` é o plano — muda o tempo todo. `docs/` é o produto acabado — muda
quando o sistema muda. A execução mora fora do repositório.**

Este projeto tem **três** lugares, não dois. O terceiro é a pasta de controle
que contém o repositório: é lá que ficam plano de ação, ordem das tarefas e
andamento. Eles mudam a cada sessão e não descrevem nem o produto nem o sistema.

| Pergunta | Onde |
|---|---|
| O que o sistema precisa fazer? (com ID rastreável) | `specs/project/REQUIREMENTS.md` |
| Qual a visão, os princípios e a golden rule? | `specs/project/PROJECT.md` |
| O que vamos construir nesta fatia e por quê? | `specs/features/NNNN-slug/spec.md` |
| Como vamos construir? | `specs/features/NNNN-slug/design.md` |
| Quais os passos desta fatia? | `specs/features/NNNN-slug/tasks.md` |
| Como o sistema é, hoje? | `docs/architecture.md` |
| Por que decidimos assim, para sempre? | `docs/adr/NNNN-title.md` |
| Decisão menor, uma linha? | `docs/decisions.md` |
| Qual o vocabulário do domínio? | `docs/glossary.md` |
| Como rodo / opero / debugo isso? | `docs/runbooks/`, `README.md` |
| Qual o contrato da API? | `docs/api.md` (ou OpenAPI gerado) |
| Qual a estratégia e as fases? | `ACTION-PLAN.md`, **fora do repo** |
| O que falta e em que ordem? | `TASKS.md`, **fora do repo** |
| Em que pé estamos? Que decisão foi tomada? | `STATE.md`, **fora do repo** |

## Estrutura

```
<pasta de controle>/          # não é repositório
├── ACTION-PLAN.md            # estratégia, fases, o que está fora de escopo
├── TASKS.md                  # backlog ordenado, com dependências e status
├── STATE.md                  # memória de trabalho: estado, decisões, blockers
└── wagering-core/            # o repositório
    ├── CLAUDE.md             # índice para o agente
    ├── AGENTS.md             # os inegociáveis, em ~15 linhas
    ├── README.md             # para humano: o que é, como rodar
    ├── docs/
    │   ├── architecture.md   # como o sistema é hoje
    │   ├── glossary.md       # linguagem ubíqua
    │   ├── decisions.md      # decisões menores, uma linha por row
    │   ├── adr/              # decisões estruturais, uma por arquivo
    │   └── runbooks/         # operação, deploy, troubleshooting
    └── specs/
        ├── project/
        │   ├── PROJECT.md      # visão, golden rule, princípios, stack
        │   └── REQUIREMENTS.md # requisitos com ID (REQ-MON-001…)
        ├── features/
        │   └── NNNN-slug/
        │       ├── spec.md     # o que e por quê, citando os REQ que cobre
        │       ├── design.md   # arquitetura e componentes (features grandes)
        │       └── tasks.md    # tasks atômicas com critério de verificação
        └── quick/NNN-slug/     # tarefas ad-hoc (≤3 arquivos)
```

**Os três de cima não têm cópia dentro de `specs/project/`.** Um
`specs/project/STATE.md` ou `ROADMAP.md` seria a mesma informação um nível
abaixo, e duas cópias do andamento é como o andamento passa a estar errado nas
duas. Se uma ferramenta pedir `specs/project/STATE.md`, o arquivo que ela quer é
o `STATE.md` da pasta de controle.

## Regras de uso

- **Nomes de caminho em inglês; conteúdo em português.** Slug de feature,
  arquivo de ADR e nome de doc seguem o código; a prosa dentro deles, não.
- **Numeração de feature é sequencial e contínua no projeto**, 4 dígitos:
  `0001-`, `0002-`… Nunca renumere.
- **Requisito recebe ID** por área (`REQ-MON-001`, `REQ-CON-006`). O `spec.md`
  de cada feature declara quais cobre, e é assim que se sabe o que falta.
- **`STATE.md` é memória de trabalho e é reescrito.** Decisão que precisa
  sobreviver a uma reescrita vira ADR em `docs/adr/`.
- **`docs/` descreve o que existe.** Escrever ali o que ainda vamos construir é
  registrar plano no lugar da verdade — que é o problema que esta regra inteira
  existe para evitar. Enquanto não há sistema, `docs/architecture.md` não nasce.
- **Registrar o que ficou pendente de terceiro** (decisão, acesso, credencial)
  explicitamente, em `STATE.md`. O que não está escrito vira retrabalho.
- Ao terminar uma feature, `TASKS.md` e `STATE.md` são atualizados. Isso faz
  parte da feature, não é opcional.
- A skill `spec-driven` conduz o fluxo e dimensiona a profundidade pelo tamanho
  da mudança — mudança pequena não precisa de `design.md` nem `tasks.md`. Ela
  fala em `ROADMAP.md`: aqui esse papel é do `TASKS.md`, fora do repositório.
