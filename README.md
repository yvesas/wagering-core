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
| 8 | Publicação por registro de saída | pronta |
| 9 | Autenticação OIDC e isolamento entre provedores | pronta |
| 10 | Observabilidade, métricas e reconciliação | pronta |
| 11 | Execução em container e simulação de clientes | pronta |

## Pré-requisitos

- Go (versão declarada em `go.mod`)
- Docker e Docker Compose

## Começar

```sh
git clone <url> && cd wagering-core
make setup            # aponta o git para .githooks e verifica o hook
cp .env.example .env  # ajuste o que precisar; nenhum segredo real entra aqui
make up               # sobe o sistema inteiro em Docker, aplicação incluída
```

A aplicação sobe junto com as dependências, e as migrations rodam no start-up.
Detalhe em [`docs/runbooks/running.md`](docs/runbooks/running.md) — inclusive o
que fazer quando não sobe.

**Todo endpoint de negócio exige um token.** Não há chave que desligue a
autenticação — essa chave é justamente aquela cujo valor errado é invisível.
O `make up` sobe um Keycloak com um realm de desenvolvimento importado, e:

```sh
make token CLIENT=platform        # o serviço interno: abre e lê carteira
make token CLIENT=provider-acme   # um provedor: envia e lê as próprias operações
make token CLIENT=provider-rival  # outro provedor, para tentar o isolamento
```

Como cada credencial é limitada está em `docs/security.md`.

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
make token    # imprime um token do Keycloak local (CLIENT=...)
make simulate # exercita a API como um cliente (SCENARIO=happy-path)
make up       # sobe o sistema inteiro
make up-deps  # só as dependências, para rodar o app no host
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
| Contrato dos eventos | `docs/events.md` |
| Autenticação e isolamento | `docs/security.md` |
| Métricas, logs e reconciliação | `docs/observability.md` |
| Rodar, parar e depurar | `docs/runbooks/running.md` |
| Simular clientes da API | `docs/runbooks/simulating-clients.md` |
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
