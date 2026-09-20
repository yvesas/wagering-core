# Observabilidade

> O que este serviço mostra sobre si mesmo, e como descobrir que algo está
> errado. Decisões em [`adr/0012`](adr/0012-observability-and-reconciliation.md).

## As três coisas, e por que são três

| Quando | O quê | Responde |
|---|---|---|
| Alerta dispara | **Métrica** | "Algo está errado" |
| Investigando | **Log** | "Onde, e com qual operação" |
| Confirmando | **Reconciliação** | "O dinheiro bate?" |

Nenhuma substitui as outras. Métrica não diz qual carteira; log não dispara
alerta; reconciliação não roda sozinha.

## Log

JSON sempre, inclusive local — formato que muda por ambiente é formato que só se
depura num deles.

A linha que importa é `operation settled`, escrita uma vez por operação, na
mesma forma para as duas portas de entrada:

```json
{
  "time": "2026-09-20T10:41:24Z", "level": "INFO", "msg": "operation settled",
  "source": "http",
  "transactionId": "01a0bf0c-b80a-7510-92b4-140c791ce672",
  "walletId": "01a0bf0c-b606-77c8-ad11-ae976207576f",
  "playerId": "player-1", "providerId": "acme",
  "externalTransactionId": "bet-42",
  "kind": "BET", "status": "PROCESSED", "replay": false,
  "took": 4200000,
  "correlationId": "bed29c08-00b8-4e20-b6b5-7333ef40b995"
}
```

**O que não está lá:** valor, saldo e qualquer credencial. Um agregador de log
não é lugar de payload financeiro, e o `transactionId` acha os dois no banco
para quem tiver direito de ver. `app.Identity` nem guarda o token, então o
método que o imprimiria não teria o que imprimir.

**`correlationId`** vem do header `X-Correlation-Id` quando o chamador manda, e
é gerado quando não manda. Na porta de fila, é o `messageId` do envelope — o que
liga a linha da entrega à linha da operação.

Um teste lê a linha de volta e falha se um identificador sumir ou se um valor
aparecer. Os dois requisitos puxam para lados opostos e por isso são o mesmo
teste.

## Métricas

`GET /metrics` em **porta separada** (`APP_METRICS_ADDR`, padrão `:9090`), sem
credencial, não publicada para fora da rede local. Um scraper não é um provedor:
não tem client no realm e não tem token para renovar.

```sh
curl -s localhost:9090/metrics | grep '^wagering_'
```

| Série | Tipo | Labels | Para quê |
|---|---|---|---|
| `wagering_operations_total` | counter | `source`, `kind`, `status` | Desfecho por porta de entrada. Rejeição conta aqui, não como erro |
| `wagering_operation_duration_seconds` | histogram | `source` | Latência de processamento |
| `wagering_wallet_contention_total` | counter | — | Disputa que chegou ao cliente |
| `wagering_transaction_retries_total` | counter | — | Disputa que o retry absorveu |
| `wagering_queue_duplicates_total` | counter | — | Entrega que o registro de entrada já tinha tratado |
| `wagering_queue_discarded_total` | counter | `reason` | Mensagem descartada de vez |
| `wagering_queue_released_total` | counter | — | Entrega devolvida para reentrega |
| `wagering_outbox_publish_lag_seconds` | histogram | — | Do fato até a publicação |
| `wagering_outbox_publish_failures_total` | counter | — | Publicação recusada pelo broker |
| `wagering_reconciliations_total` | counter | `result` | `match` e `drift`, os dois |
| `wagering_http_request_duration_seconds` | histogram | `route`, `method`, `status` | Latência por rota |

Mais os coletores de runtime do Go — heap, goroutines, descritores — que
respondem "o processo está bem" antes de qualquer métrica de negócio.

### Três detalhes que não são óbvios

**`route` é o padrão registrado, nunca o caminho.** `/wallets/{walletId}`, não
`/wallets/01a0…`. Uma série por carteira é como um backend de métrica é
derrubado pela instrumentação que existia para observá-lo. O label sai da tabela
de rotas, que é lista fechada — o limite é estrutural, não um sanitizador que
alguém precisa lembrar de atualizar. Nenhuma métrica leva id de carteira, de
provedor ou valor; isso é trabalho do log.

**Contenção e retry são duas séries.** `transaction_retries_total` é a disputa
que o retry escondeu; `wallet_contention_total` é a que chegou ao cliente como
409. Juntas dizem quanto da disputa ficou invisível.

**O atraso de publicação é medido do instante do fato**, não de quando o
publisher pegou a linha. A segunda leitura responderia "com que rapidez
publicamos o que escolhemos publicar", que não é pergunta de ninguém. E entrega
duplicada é contada, não cronometrada: a segunda é quase de graça, e incluí-la
faria uma tempestade de reentrega parecer melhora.

### Por onde começar num incidente

| Sintoma | Olhe |
|---|---|
| Apostas sendo recusadas | `operations_total{status="REJECTED"}` por `kind` |
| Lentidão | `operation_duration_seconds` por `source` — separa fila de HTTP |
| Carteira quente | `wallet_contention_total` e `transaction_retries_total` juntas |
| Eventos atrasados | `outbox_publish_lag_seconds`, depois `publish_failures_total` |
| Fila repetindo | `queue_duplicates_total` subindo sem `operations_total` subir |
| Mensagem sumindo | `queue_discarded_total` por `reason` |

## Health checks

`GET /health/live` e `GET /health/ready`, na porta de negócio e **sem
credencial** — um load balancer não tem token.

Significam coisas diferentes: liveness diz que o processo não travou e **não
toca dependência nenhuma**; readiness diz que ele consegue servir e pinga o
banco. Ligar o banco no liveness é o jeito clássico de transformar uma piscada
do PostgreSQL em todas as réplicas reiniciando ao mesmo tempo.

## Reconciliação

```
POST /wallets/{walletId}/reconciliation
```

Reconstrói o saldo a partir do ledger — crédito menos débito, **a abertura
incluída**, porque a abertura é um lançamento comum — e compara com o
armazenado. **Não altera nada.**

Restrita ao serviço interno (`wallets:manage`): lê todo movimento que a carteira
já teve.

```json
{
  "walletId": "01a0bf0c-…", "consistent": false,
  "storedBalance":  { "amount": "40.00",  "currency": "BRL" },
  "rebuiltBalance": { "amount": "100.00", "currency": "BRL" },
  "difference":     { "amount": "-60.00", "currency": "BRL" },
  "entries": 3, "walletVersion": 4,
  "checkedAt": "2026-09-20T12:00:00.000Z"
}
```

**Divergência responde 200.** Quem chamou perguntou se os dois batem, e
responder "não batem" é o endpoint funcionando — um não-2xx faria todo monitor
tratar uma verificação bem-sucedida como requisição quebrada. O veredito está no
corpo, numa métrica e num log em nível `ERROR`.

**Os dois números sempre**, e não só quando diferem: "eles discordam" é o começo
de uma investigação, e o sinal da diferença diz de que lado — dinheiro faltando
ou dinheiro inventado.

**É `POST` embora não mude nada**, porque não é de graça: lê todo lançamento da
carteira. `GET` convidaria cache, prefetch e retry-por-timeout, e nenhum deles
devia decidir a frequência disso.

### A leitura acontece numa vista só

As duas leituras — saldo e soma — acontecem dentro de uma transação
`REPEATABLE READ READ ONLY`. **Não é detalhe de performance.**

O padrão do PostgreSQL é `READ COMMITTED`, onde cada statement tira seu próprio
snapshot. As duas leituras cercariam o commit de outra pessoa, uma aposta caindo
no meio seria contada numa e não na outra, e o resultado seria uma divergência
que nunca existiu. Reconciliação que grita lobo é reconciliação que se aprende a
ignorar.

O teste de integração prova isso contra PostgreSQL de verdade, e **falha** se o
nível de isolamento for enfraquecido.

### O que fazer com uma divergência

1. **Não corrigir o saldo.** A reconciliação não escreve, e é de propósito:
   corrigir apaga a evidência de como os dois se separaram.
2. Pegar o `walletId` e ler o ledger inteiro. Ele é imutável — o banco revoga
   `UPDATE` e `DELETE` e tem trigger — então ele é a versão confiável.
3. Procurar o intervalo: `wallet_contention_total`, `queue_discarded_total` e
   as linhas `operation settled` daquela carteira no mesmo período.
4. Correção é **lançamento novo**, levantado por uma pessoa.

## Rodando com isso ligado

```sh
make up                                 # dependências locais
curl -s localhost:9090/metrics | head   # as métricas do processo

TOKEN=$(make -s token CLIENT=platform)
curl -X POST -H "Authorization: Bearer $TOKEN" \
  localhost:8080/wallets/<id>/reconciliation
```

Não há Prometheus no `docker-compose.yml`: a aplicação não roda em container
aqui, e um scraper apontando para o host seria configuração específica de
sistema operacional em troca de nada que um `curl` não mostre.

## Onde isso está provado

| Arquivo | O que prova |
|---|---|
| `internal/app/observability_test.go` | Desfecho contado por status e por porta; duplicata contada e não cronometrada; a linha de log com identificadores e sem valores |
| `internal/app/reconcile_test.go` | Concordância, divergência plantada, escopo, e o que acontece sem vista única |
| `internal/adapter/metrics/prometheus_test.go` | Os nomes de série exatos, os dois desfechos de reconciliação, atraso negativo, registries independentes |
| `internal/adapter/postgres/snapshot_integration_test.go` | A vista única contra PostgreSQL — falha com `READ COMMITTED` |
| `internal/adapter/http/reconciliation_test.go` | 200 na divergência, 405 no `GET`, recusa repassada |
| `test/observability_test.go` | Porta separada sem credencial, contador subindo, id de carteira fora dos labels, reconciliação ponta a ponta |
