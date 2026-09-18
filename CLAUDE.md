# wagering-core — instruções para o agente

## O que é

Serviço distribuído de carteira e ledger financeiro para operações de jogo,
com entrada por HTTP e por fila produzindo o mesmo resultado. Projeto de estudo.
Go · Uber Fx · PostgreSQL com `pgx` · Docker Compose · `-race`. Fila e OIDC nas
fases 7 e 9.

## Golden rule

**O domínio não conhece infraestrutura.** Teste:

```sh
go list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'
```

Saída vazia, ou a regra foi quebrada. Há duas portas de entrada que precisam
produzir o mesmo resultado; invariante que mora num handler some na segunda.

## Arquitetura

Hexagonal, dependência sempre para dentro: `adapter → app → domain`.

| Pasta | Responsabilidade |
|---|---|
| `cmd/` | Binários; cada um monta seu grafo Fx e mais nada |
| `internal/domain/` | Entidades, value objects, invariantes, erros — sem infra |
| `internal/app/` | Casos de uso e as portas que exigem |
| `internal/adapter/` | HTTP, PostgreSQL, fila |
| `internal/platform/` | Config, log, métricas, módulos Fx compartilhados |
| `migrations/` · `test/` | SQL versionado · integração com containers reais |

## Comandos

```sh
make setup    # reinstala core.hooksPath — rode logo após clonar
make check    # fmt + vet + domain-check + test + race — o gate antes de commitar
make help     # lista todos os alvos
```

`domain-check` é a golden rule dentro do gate: regra que não roda sozinha vira
recomendação, e recomendação não sobrevive a prazo.

## Convenções

Ficam em `.claude/rules/`, e **são deste projeto** — não vêm mais de baseline
nenhum. Ver `.claude/README.md` para o porquê do fork.

| Regra | Assunto |
|---|---|
| `code-style.md` | Go: interface por quem consome, erro tipado, `context` primeiro, `panic` não é rejeição de negócio |
| `testing.md` | `table-driven`, `-race`, integração com banco real atrás de build tag |
| `docs-and-specs.md` | O que vai em `specs/`, o que vai em `docs/`, o que vive fora do repo |
| `git-flow.md` | Branch, Conventional Commits, PR, atribuição de IA |
| `secrets.md` · `ci-and-minutes.md` | `.env` e segredo · minuto de Actions |

Três que valem repetir aqui, porque são as mais fáceis de violar sem perceber:
**nada de `float32`/`float64` em código que toca dinheiro**, em etapa nenhuma;
**`context.Context` é o primeiro parâmetro** de toda função que faz I/O; e
**inglês em tudo que é código** — identificador, comentário, caminho, build e
mensagem de commit — com o português restrito a `specs/`, `docs/` e `README.md`.

## Commit e PR

Conventional Commits, subject em inglês, imperativo, < 72 caracteres. Branch
`<tipo>/<slug>/<issue>`. PR contra `main`, squash.

**Nunca** `Co-Authored-By`, "Generated with" ou assinatura de ferramenta — o
hook `commit-msg` rejeita e `--no-verify` está negado no `settings.json`.

Este é um projeto de estudo pessoal e se documenta como tal: os requisitos são
requisitos de produto, e nenhum arquivo cita empresa, marca ou terceiro.

## Footguns

- **Não há `package.json`**, então o truque do `prepare` não existe. Quem clonar
  roda `make setup`, ou o `commit-msg` fica no repo sem executar.
- **Invariante só no domínio não basta.** Saldo não negativo, unicidade e
  imutabilidade do ledger também são constraint no banco: o domínio erra, o
  banco é a última linha.
- **`-race` não prova ausência de corrida**, só detecta o que ocorreu naquela
  execução — cenário de concorrência exige disputa real e repetição.
- **Replay devolve o saldo do processamento original**, não o atual. Fácil de
  implementar errado e passar no teste do caminho feliz.

## Onde fica o quê

`specs/project/REQUIREMENTS.md` requisitos com ID · `specs/features/NNNN-slug/` o
que vamos construir · `docs/` como o sistema é hoje · `docs/adr/` decisões
permanentes. Plano, ordem das tarefas e andamento ficam **fora** do repositório.
