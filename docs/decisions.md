# Decisões menores

> Uma linha por decisão. Decisão **estrutural** — que molda o sistema e seria
> cara de reverter — vira um arquivo em `docs/adr/`.
>
> Este arquivo é histórico e só cresce. Decisão revista ganha linha nova
> apontando para a antiga, em vez de a antiga ser editada.

| Data | Decisão | Motivo |
|---|---|---|
| 2026-09-18 | Go com Uber Fx | A stack é parte do objetivo: concorrência, `context`, `errors.Is`/`As` e composição por construtor são o que este problema exercita. `fx.Lifecycle` dá encerramento observável aos workers. |
| 2026-09-18 | Requisitos com ID versionados em `specs/project/REQUIREMENTS.md` | Precisam viajar no clone para o `spec.md` de cada feature poder citá-los. Ficariam órfãos fora do repositório. |
| 2026-09-18 | Plano, ordem das tarefas e andamento fora do repositório | Plano e verdade no mesmo arquivo envelhecem juntos e ninguém confia em nenhum dos dois. O repositório guarda o produto; a pasta de controle, a execução. |
| 2026-09-18 | Convenções de Go no `CLAUDE.md` do projeto | A rule `code-style.md` do baseline é de TypeScript e não se edita dentro de um projeto — a próxima instalação sobrescreve. O `CLAUDE.md` é o arquivo que o instalador nunca toca. |
| 2026-09-18 | `.claude/` bifurcado do `yas-claude-base` v0.6.4 | O baseline assume TypeScript, e a suposição matava guardas em silêncio. Detalhe em `.claude/README.md`. |
| 2026-09-18 | Branch resolvida por `symbolic-ref`, não `rev-parse` | Em repositório sem commits o `rev-parse --abbrev-ref HEAD` devolve `"HEAD"`, e o guard de commit na `main` liberava em silêncio. |
| 2026-09-18 | Guards de hook falham fechados sem parser JSON | Guarda que não lê o pedido não sabe o que autorizar. A versão anterior liberava tudo, inclusive `cat .env`. |
| 2026-09-18 | `make setup` como porta de entrada do clone | Não há `package.json` para o `prepare` que reinstala o `core.hooksPath`. Sem um alvo explícito, o hook fica no repositório sem nunca executar. |

## `docs/architecture.md`

Ainda não existe, de propósito. `docs/` descreve o sistema **como ele é**, e
hoje não há sistema. O arquivo nasce junto com o primeiro código que o torne
verdadeiro. O desenho pretendido vive em `specs/project/PROJECT.md` e no
`design.md` de cada feature, que é onde plano deve morar.
