# 0010 — Observabilidade e reconciliação

- **Status:** implementada
- **Requisitos cobertos:** `REQ-OBS-001..004` · `REQ-API-010..011`
- **Depende de:** features 0001 a 0009 (concluídas)

## O que foi construído

Métricas Prometheus atrás de portas do `internal/app`, expostas numa porta
separada; a linha de log que carrega os identificadores de uma operação e
nenhum valor; e `POST /wallets/{walletId}/reconciliation`, que reconstrói o
saldo a partir do ledger e compara sem escrever nada.

Junto, a dívida que o plano mandava resolver nesta fase: o CI existe.

Decisões em [`docs/adr/0012`](../../../docs/adr/0012-observability-and-reconciliation.md);
o manual em [`docs/observability.md`](../../../docs/observability.md).

## A decisão que mais vale explicar

**Transação não é snapshot.**

Reconciliação lê duas coisas — o saldo da carteira e a soma do ledger — e o
reflexo é envolver as duas numa transação e considerar o assunto resolvido. Não
resolve: o padrão do PostgreSQL é `READ COMMITTED`, onde **cada statement** tira
seu próprio snapshot. As duas leituras podem cercar o commit de outra pessoa,
uma aposta caindo no meio é contada numa e não na outra, e o endpoint reporta
uma divergência que **nunca existiu**.

E esse é o pior resultado possível aqui, pior do que não ter reconciliação
nenhuma: uma que grita lobo é uma que as pessoas aprendem a ignorar, e ela vai
estar sendo ignorada no dia em que estiver certa.

Daí a porta `Snapshot`, separada do `UnitOfWork` em vez de um `readOnly bool`
nele — as duas têm garantias diferentes, e um argumento booleano deixa pedir a
errada sem perceber. `REPEATABLE READ` para a vista única, `READ ONLY` porque
dizer ao banco é mais forte que dizer num comentário.

## O teste que falha quando se enfraquece o isolamento

O teste de integração troca a ordem: abre o snapshot, lê o saldo, **comita uma
aposta noutra conexão**, lê de novo. Trocando `pgx.RepeatableRead` por
`pgx.ReadCommitted`, ele falha assim:

```
the view moved under the snapshot: 100.00 then 75.00 -- repeatable read is not in effect
```

Verificado exatamente assim antes de fechar a fase, porque um teste que passa
nas duas versões não prova nada.

E o teste em memória, em `internal/app`, mostra o outro lado **de propósito**:
lá a aposta intercalada produz a divergência. Os dois são um par — o primeiro
diz o que o banco garante, o segundo diz por que a garantia precisa existir.

## O que não entra numa métrica

**Nenhum método da porta aceita id de carteira, provedor ou valor.** Um label
sem limite é como um backend de métrica cai — derrubado pela instrumentação que
existia para observá-lo.

O caso que quase escapou é a rota HTTP. O reflexo é `r.URL.Path`, e isso seria
uma série por carteira. O label sai da **tabela de rotas** — a mesma lista
fechada que a fase 9 usou para decidir o que é público — então o limite é
estrutural, não um sanitizador que alguém precisa lembrar de atualizar.

O cenário de integração confere isso do jeito que dói: os ids de carteira são
únicos por execução, então se o caminho tivesse vazado para um label, a própria
execução teria plantado a prova.

## Três pares que existem porque um número sozinho mente

- **Contenção e retry.** `transaction_retries_total` é a disputa que o retry
  escondeu; `wallet_contention_total` é a que chegou ao cliente. Só as duas
  juntas dizem quanto ficou invisível — o número que o ADR 0007 mediu à mão.
- **`match` e `drift` na reconciliação.** Um contador que só se mexesse na falha
  não distingue "nada está errado" de "nada rodou", e as duas pedem respostas
  bem diferentes às três da manhã.
- **Entrega e operação, na fila.** A duplicata é contada e **não** é
  cronometrada: a segunda entrega é quase de graça, e incluí-la faria uma
  tempestade de reentrega parecer melhora de latência.

## Uma coisa que a primeira execução mostrou

O cenário multi-processo falhou na primeira vez por um motivo que não era o
código sob teste: **três instâncias brigando pela `:9090`**. A porta de métricas
tem de ser única por processo como a de negócio já era, e isso só aparece quando
três sobem de verdade.

No mesmo par de execuções, duas afirmações minhas estavam erradas e o código
certo: contador com label **não existe** até ser incrementado — instância recém
subida não exporta `wagering_operations_total` — e eu afirmei sobre uma rota que
o teste nunca chamava.

## O CI, que era regra sem execução

`.claude/rules/ci-and-minutes.md` descrevia um workflow que não existia desde a
fase 0. É exatamente o que D-010 evitou no `domain-check`: regra que não roda
vira recomendação.

Agora existe, e segue o próprio conselho — `pull_request` apenas, `concurrency`
com cancelamento, `timeout-minutes` em todo job, cache pelo `go.sum`, guarda de
diff por `if:` de step (nunca `paths:`, que num required check deixa o PR
esperando para sempre) e `services:` só no job que abre conexão.

## Como se prova

```sh
make check
make up-test && make test-integration
make test-scenarios
```

À mão:

```sh
make up
curl -s localhost:9090/metrics | grep '^wagering_'
curl -X POST -H "Authorization: Bearer $(make -s token CLIENT=platform)" \
  localhost:8080/wallets/<id>/reconciliation
```
