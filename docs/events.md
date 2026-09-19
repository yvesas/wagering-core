# Contrato dos eventos

> O que este serviço publica, e o que um consumidor precisa saber para tratar.

## Destino e roteamento

Os eventos saem numa fila FIFO própria (`QUEUE_EVENTS_NAME`, por padrão
`wager-events.fifo`), provisionada pela aplicação no start-up.

**O `MessageGroupId` é o agregado.** Um ouvinte que acompanha uma carteira vê as
mudanças dela em ordem, e agregados diferentes seguem em paralelo — a mesma
granularidade que o resto do sistema usa para coordenar.

O `MessageDeduplicationId` é o `eventId`. Isso é **conveniência, não garantia**:
a janela do broker é de cinco minutos e uma republicação pode acontecer depois.

## O envelope

```json
{
  "eventId": "01a0baa2-16a8-7daf-b010-49444527914c",
  "eventType": "WagerTransactionProcessed",
  "version": 1,
  "aggregateId": "01a0baa2-1693-7d2b-a2bb-d9e417751385",
  "correlationId": "bed29c08-00b8-4e20-b6b5-7333ef40b995",
  "causationId": "...",
  "occurredAt": "2026-09-19T17:06:19.923861Z",
  "data": { }
}
```

**O `eventId` é estável e é por ele que se deduplica.** Ele é cunhado quando o
evento é gravado, nunca quando é publicado, e uma republicação mantém o mesmo.

**Entrega é at-least-once, e o consumidor precisa deduplicar.** Um processo que
morre entre publicar e marcar como publicado publica de novo. Evitar isso exigiria
confirmação em duas fases com o broker, que é o problema que o registro de saída
existe para não resolver.

`version` é o esquema do `data`. Roteie por `eventType` **e** `version`.

`occurredAt` é o instante em que o fato aconteceu, **não** o da publicação. Os
dois podem estar bem distantes se o broker esteve fora.

## Os quatro eventos

### `WagerTransactionProcessed`

Operação concluída com sucesso. `aggregateId` é a transação.

```json
{
  "transactionId": "…", "kind": "BET", "status": "PROCESSED",
  "walletId": "…", "playerId": "…",
  "money": { "amount": "25.00", "currency": "BRL" },
  "providerId": "provider-a", "externalTransactionId": "tx-1",
  "roundId": "round-1", "gameId": "fortune-chimp",
  "balanceAfter": { "amount": "75.00", "currency": "BRL" }
}
```

**Sai para `LOSS` também**, que não move dinheiro. Por isso não é o mesmo evento
que a mudança de saldo.

### `WagerTransactionRejected`

Recusa definitiva por regra de negócio. Mesmo payload, com `failureCode`
preenchido e sem `balanceAfter` — a operação não chegou a produzir saldo.

O `failureCode` é o contrato: `INSUFFICIENT_FUNDS`, `REVERSAL_EXCEEDS_BALANCE`,
`REFERENCE_NOT_FOUND`, `ALREADY_REVERSED` e os demais de `docs/api.md`.

### `WalletBalanceChanged`

Saldo efetivamente alterado. `aggregateId` é a **carteira**, não a transação:
quem acompanha um saldo quer todas as mudanças dele em ordem.

```json
{
  "walletId": "…", "transactionId": "…",
  "direction": "DEBIT",
  "money": { "amount": "25.00", "currency": "BRL" },
  "balanceBefore": { "amount": "100.00", "currency": "BRL" },
  "balanceAfter": { "amount": "75.00", "currency": "BRL" },
  "walletVersion": 2
}
```

**Não sai para `LOSS` nem para operação rejeitada.**

### `WagerTransactionPendingReference`

Reversão registrada à espera do que ela desfaz. Acrescenta
`referenceExternalTransactionId`, `attempts` e `deadlineAt` — este último é
quando a espera acaba, para um ouvinte poder alertar sobre reversão travada.

## Dinheiro no payload

Sempre `{"amount":"25.00","currency":"BRL"}` — string decimal, nunca número
JSON. Número convidaria o consumidor a lê-lo como float.

## O que o payload é

Um **retrato dos valores no instante do fato**, serializado na hora. Não é uma
referência para reler depois: um payload montado na hora de publicar leria o
estado atual, e o mesmo evento diria coisas diferentes dependendo de quando o
worker acordou.
