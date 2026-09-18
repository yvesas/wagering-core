# wagering-core

Serviço distribuído de **carteira e ledger financeiro** para operações de jogo.
Provedores enviam apostas, ganhos, devoluções e desfazimentos; o serviço
movimenta a carteira do jogador e registra cada movimentação num ledger
imutável, por API HTTP e por fila, com o mesmo resultado financeiro nos dois
caminhos.

A propriedade que domina o projeto: **o resultado financeiro permanece correto
com várias instâncias em execução e falhas entre as etapas do processamento.**

Projeto de estudo pessoal, construído em fatias verificáveis.

## Estado

Em construção. A fase atual e o que já está pronto ficam registrados fora deste
repositório, na pasta de controle ao lado.

| Fase | Entrega | Situação |
|---|---|---|
| 1 | Núcleo de domínio: valor monetário, carteira, ledger, transação | pronta |
| 2 | Persistência, migrations e constraints | pronta |
| 3 | Abertura de carteira, HTTP e composição por DI | pronta |
| 4 | Idempotência persistente e replay | pronta |
| 5 | Concorrência por carteira | pronta |

| 6 | Reversões e resolução de referência | pronta |
| 7 | Consumo por fila, com registro de entrada | pronta |

Fases 8 a 10 — publicação por registro de saída, autenticação, observabilidade —
estão especificadas em `specs/project/REQUIREMENTS.md`.

## Pré-requisitos

- Go (versão declarada em `go.mod`)
- Docker e Docker Compose

## Começar

```sh
git clone <url> && cd wagering-core
make setup            # aponta o git para .githooks e verifica o hook
cp .env.example .env  # ajuste o que precisar; nenhum segredo real entra aqui
make up               # sobe PostgreSQL e demais dependências locais
```

> **`make setup` não é opcional.** `core.hooksPath` é configuração local do git
> e não vem no clone. Sem ele, o hook `commit-msg` existe no repositório e nunca
> executa — uma guarda que parece estar ligada e não está.

## Comandos

```sh
make check            # fmt + vet + domain-check + app-check + test + race
make up-test          # sobe o PostgreSQL isolado dos testes
make test-integration # testes contra o banco de verdade
make test-scenarios   # cenários multi-processo, com fila real
make test     # go test ./...
make race     # go test -race ./...
make vet      # go vet ./...
make fmt      # formata
make cover    # cobertura em coverage.html
make up       # sobe o ambiente local
make down     # derruba e apaga os volumes
```

`make help` lista tudo.

## Variáveis de ambiente

Todas em `.env.example`, com um comentário do que cada uma faz. O arquivo é
versionado com valores locais de exemplo; `.env` nunca é.

## Como navegar

| O quê | Onde |
|---|---|
| Contrato da API | `docs/api.md` |
| Visão, princípios e stack | `specs/project/PROJECT.md` |
| Requisitos com ID rastreável | `specs/project/REQUIREMENTS.md` |
| O que está sendo construído agora | `specs/features/NNNN-slug/spec.md` |
| Como o sistema é hoje | `docs/` |
| Decisões estruturais e seus motivos | `docs/adr/` |
| Vocabulário do domínio | `docs/glossary.md` |
| Instruções para agentes de IA | `CLAUDE.md` e `AGENTS.md` |

Plano de ação, ordem das tarefas e andamento ficam **fora** deste repositório.
Aqui vive o produto; lá, a execução.

## Contribuir

Conventional Commits, subject em inglês e imperativo, abaixo de 72 caracteres.
Tudo sai de `main` e volta por Pull Request.

**Nunca** inserir `Co-Authored-By`, "Generated with" ou qualquer assinatura de
ferramenta em commit, PR ou comentário. O hook `commit-msg` rejeita; não
contorne com `--no-verify`.
