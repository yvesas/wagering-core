# Rodar o sistema

> Subir, parar, olhar, e o que fazer quando não sobe.

## Em uma linha

```sh
make up
```

Sobe PostgreSQL, o emulador de fila, o Keycloak e **a aplicação**, nessa ordem,
esperando cada dependência ficar saudável antes da próxima. As migrations rodam
no start-up, então não há passo separado.

```
api       http://localhost:8080
keycloak  http://localhost:8081   (admin / local-dev-only)
```

Até a fase 10 isto não existia: o `make up` subia só as dependências e a
aplicação rodava no host com `go run`.

## O que sobe

| Container | Porta publicada | Para quê |
|---|---|---|
| `wagering-api` | 8080 | A API de negócio |
| `wagering-keycloak` | 8081 | O emissor de token |
| `wagering-postgres` | 5432 | O banco |
| `wagering-localstack` | 4566 | O emulador de SQS |

**A porta de métricas (9090) não é publicada**, e isso é a decisão D-052 valendo
na prática: ela responde sem credencial, então o que a alcança deve ser o que
esta rede já confia. Para olhar:

```sh
docker compose exec api wget -qO- http://127.0.0.1:9090/metrics | grep '^wagering_'
```

## Chamar a API

Todo endpoint de negócio exige um `Bearer` token — não existe chave que desligue
isso. O realm local traz três clients, todos com segredo `local-dev-only`:

```sh
make token CLIENT=platform         # abre e lê carteira, reconcilia
make token CLIENT=provider-acme    # envia e lê as próprias operações
make token CLIENT=provider-rival   # outro provedor, para tentar o isolamento
```

```sh
TOKEN=$(make -s token CLIENT=platform)
curl -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"playerId":"player-1","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

O que cada credencial pode está em [`../security.md`](../security.md); para
exercitar tudo de uma vez, veja
[`simulating-clients.md`](simulating-clients.md).

## O `iss`, que é a armadilha desta configuração

Um token é aceito só se o `iss` dele bater com `OIDC_ISSUER_URL`, e o discovery
recusa um documento que se diga de outro emissor. Dentro de um container,
`localhost:8081` é **o próprio container** — então uma configuração ingênua
falha em silêncio, com todo token sendo recusado e nada explicando por quê.

A saída aqui não contorna o problema, resolve: **o Keycloak escuta na 8081 dos
dois lados.** `--http-port=8081` com `ports: 8081:8081`, e `--hostname` fixando
o emissor em `http://keycloak:8081`. Assim `keycloak:8081` é a mesma URL de
dentro e de fora da rede, e o token vale nos dois lugares.

Consequência prática: **para rodar a aplicação fora do Docker** contra este
Keycloak, o host precisa resolver o nome:

```sh
echo '127.0.0.1 keycloak' | sudo tee -a /etc/hosts
```

Feito isso, `make up-deps` sobe só as dependências e `go run ./cmd/api` funciona
com `OIDC_ISSUER_URL=http://keycloak:8081/realms/wagering`. Sem isso, o boot
falha — de propósito: um processo que subisse assim mesmo se diria saudável e
recusaria todo mundo.

## Comandos

```sh
make up          # o sistema inteiro
make up-deps     # só as dependências, para rodar o app no host
make logs        # os logs da aplicação
make logs-all    # os logs de todos os containers
make down        # derruba tudo e apaga os volumes
```

`make down` **apaga o volume do PostgreSQL**. É o que se quer para recomeçar do
zero e não é o que se quer no meio de uma investigação; para só parar, use
`docker compose stop`.

## Configuração

Tudo por variável de ambiente, com o nome e o propósito de cada uma em
`.env.example`. Os valores que o compose usa estão no próprio
`docker-compose.yml`, apontando para os nomes da rede — `postgres`,
`localstack`, `keycloak` — porque `localhost` dentro de um container é o
container.

Para mudar uma sem editar o compose:

```sh
APP_LOG_LEVEL=debug make up
APP_PORT=9000 make up
```

## Quando não sobe

**Primeiro, olhe quem está saudável:**

```sh
docker compose ps
```

O `api` só começa depois que os três anteriores estão `healthy`. Se ele nem
aparece como `Starting`, o problema é de uma dependência.

| Sintoma | O que é |
|---|---|
| `dependency failed to start: container ... is unhealthy` | Uma dependência não passou no healthcheck. `docker compose logs <nome>` |
| O `api` reinicia em laço | Leia `make logs`: configuração faltando e IdP inalcançável falham o boot de propósito |
| `missing identity settings: OIDC_ISSUER_URL` | Não há modo sem autenticação; a variável é obrigatória |
| `reaching the identity provider at ...` | O Keycloak não respondeu o discovery. Ele demora ~15s na primeira subida |
| Todo request responde 401 | O `iss` do token não bate. Compare o `iss` (decodifique o token) com o `OIDC_ISSUER_URL` do container |
| `bind: address already in use` | Outra coisa está na 8080 ou 8081. `APP_PORT=9000 make up` |

**Para ver o que a aplicação acha da própria configuração**, o boot registra o
que encontrou:

```sh
make logs | grep -E "identity provider ready|database ready|queues ready|listening"
```

**Para entrar no container** — é para isso que a imagem é alpine e não
distroless:

```sh
docker compose exec api sh
```

## Reconstruir depois de mexer no código

```sh
docker compose up -d --build api
```

O `Dockerfile` copia `go.mod`/`go.sum` e baixa as dependências numa camada
própria, antes do código. Uma edição normal reaproveita essa camada; mudar uma
dependência é o que a invalida.

O binário final não leva toolchain, é estático (`CGO_ENABLED=0`) e roda como
usuário sem privilégio. O que a imagem carrega além dele é `ca-certificates` —
sem a raiz de confiança, toda chamada TLS ao IdP falha com um erro que parece
bug nosso.

## Em produção isto não serve como está

O compose é ambiente local. O que muda fora dele:

- **Segredo vem do secret manager**, não do arquivo. Os valores aqui são de
  desenvolvimento e o realm existe para ser jogado fora.
- **O realm não é nosso para importar.** `OIDC_ISSUER_URL` e `OIDC_AUDIENCE`
  passam a apontar para o IdP de verdade.
- **O emulador de fila vira SQS**, e `QUEUE_ENDPOINT` fica vazio: o SDK acha a
  AWS sozinho.
- **A porta de métricas continua não publicada**, e quem raspa entra na rede.
