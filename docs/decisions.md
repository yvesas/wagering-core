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
| 2026-09-18 | UUIDv7 via `google/uuid`, não escrito à mão (F3.1) | Ordena por tempo, o que mantém o insert na borda direita do índice. Escrever é trinta linhas que parecem inofensivas — e um contador sutilmente errado dentro do mesmo milissegundo apareceria tarde, como chave duplicada sob carga. |
| 2026-09-18 | Abertura sem `PENDING` intermediário | A operação não depende de nada e é confirmada junto da carteira: não há janela entre aceitar e aplicar, e nada para outra instância retomar. |
| 2026-09-18 | Handlers dependem de interfaces pequenas, não dos casos de uso concretos | Declaradas por quem consome. O ganho é prático: teste de handler precisa de dois stubs em vez de banco, pool e grafo. |
| 2026-09-18 | Conflito responde com código da API, não com nome de constraint | Devolver o nome vaza schema e faz o cliente depender dele. Renomear constraint passaria a ser mudança de contrato. |
| 2026-09-18 | `postgres.NewPool` deixou de usar o tipo de config do `platform` | Não foi preferência: virou ciclo de import quando `platform` passou a montar o adaptador. Adaptador que importa quem o monta não pode ser montado por mais ninguém — nem pelo próprio teste de integração. |
| 2026-09-18 | goose como biblioteca, migrations embarcadas por `go:embed` (F2.2) | CLI externa exigiria instalar binário. Embarcado, o binário carrega o próprio schema e não há passo de "copiou a pasta sql?". O goose pega advisory lock antes de rodar, que é o que torna seguro toda réplica chamar no start-up. |
| 2026-09-18 | Coluna de id é `TEXT`, não `UUID` (F2.3) | Id externo é o que o provedor mandou. `UUID` rejeitaria `transaction-123`, que é entrada legítima. |
| 2026-09-18 | Imutabilidade do ledger por trigger **e** `REVOKE` (F2.5) | `REVOKE` não alcança o dono da tabela, e migration roda como dono. São dois triggers: `TRUNCATE` não dispara trigger de linha, então sem o de statement a guarda teria uma porta ao lado. |
| 2026-09-18 | Ordenação do ledger por `seq`, não por `created_at` | Duas entradas do mesmo commit compartilham o timestamp, e empate faz cursor pular ou repetir linha. `seq` é único e monotônico, então a ordem é total. |
| 2026-09-18 | Diretiva do módulo subiu para Go 1.26 | Veio do `go get` do goose, não de escolha deliberada; aceito depois do fato. A toolchain é baixada automaticamente e `go-version-file: go.mod` mantém o CI alinhado. Dockerfile terá de usar imagem 1.26. |
| 2026-09-18 | Movimentação devolve carteira nova, não muta no lugar (F1.7) | Um débito recusado tem de deixar a carteira do chamador intacta. Com receptor por ponteiro isso é promessa que todo retorno antecipado precisa lembrar de cumprir; com receptor por valor é o compilador que garante. O custo é alocação, e ela é irrelevante no tamanho destes agregados. |
| 2026-09-18 | Um tipo de transação para as duas origens (F1.8) | Dois tipos duplicariam a máquina de estados, que é a parte arriscada, e a cópia divergiria no primeiro conserto aplicado só de um lado. A segurança que tipos separados comprariam fica de pé pelos campos não exportados e por dois structs de parâmetro distintos: uma abertura não tem onde pôr um id de provedor. |
| 2026-09-18 | Identificadores são strings opacas, não UUID | O domínio não decide como uma identidade é gerada: as internas são UUIDv7 cunhados por um adaptador e as externas são o que o provedor mandou (`transaction-123`). Validar formato aqui rejeitaria entrada legítima. |
| 2026-09-18 | Escala fixa de duas casas no tipo, não na moeda | Exclui JPY (zero casas) e KWD (três); aceito porque os fluxos são em BRL. O tipo carrega a moeda, então o que muda no dia em que outra escala entrar é o fator, não o desenho. |
| 2026-09-18 | `.claude/` bifurcado do `yas-claude-base` v0.6.4 | O baseline assume TypeScript, e a suposição matava guardas em silêncio. Detalhe em `.claude/README.md`. |
| 2026-09-18 | Branch resolvida por `symbolic-ref`, não `rev-parse` | Em repositório sem commits o `rev-parse --abbrev-ref HEAD` devolve `"HEAD"`, e o guard de commit na `main` liberava em silêncio. |
| 2026-09-18 | Guards de hook falham fechados sem parser JSON | Guarda que não lê o pedido não sabe o que autorizar. A versão anterior liberava tudo, inclusive `cat .env`. |
| 2026-09-18 | `make setup` como porta de entrada do clone | Não há `package.json` para o `prepare` que reinstala o `core.hooksPath`. Sem um alvo explícito, o hook fica no repositório sem nunca executar. |

## `docs/architecture.md`

Nasceu com a feature 0001, quando passou a existir sistema para descrever. Ele
documenta o que **existe** — hoje, só o pacote de domínio — e não o que está
planejado. O desenho pretendido continua em `specs/project/PROJECT.md` e no
`spec.md` de cada feature.
