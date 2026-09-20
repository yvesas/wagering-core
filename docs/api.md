# Contrato HTTP

> O que está implementado hoje. Os endpoints das fases 4 a 10 estão em
> `specs/project/REQUIREMENTS.md` e ainda não existem.

## Convenções

**Dinheiro é sempre `{"amount":"25.00","currency":"BRL"}`** — string decimal com
duas casas e moeda ISO 4217 em maiúsculas. Nunca número JSON: número convida o
decodificador do outro lado a lê-lo como float, que é a única coisa que este
sistema recusa.

**Timestamps** em UTC, RFC 3339 com milissegundos.

**Campo desconhecido no corpo é recusado.** Um cliente que manda `ammount` tem
um bug, e ignorar em silêncio transformaria esse bug num saldo errado que
ninguém explica depois.

**`X-Correlation-Id`** é lido da requisição quando presente e devolvido sempre.
Trace que começou antes mantém a identidade atravessando este serviço.

## Autenticação

**Todo endpoint de negócio exige um `Bearer` token** emitido pelo IdP
configurado, para a audience deste serviço. Só `GET /health/live` e
`GET /health/ready` respondem sem credencial.

```
Authorization: Bearer eyJhbGciOiJSUzI1NiIs...
```

O token diz por qual provedor o chamador age (claim `provider_id`) e o que ele
pode fazer (escopos `wagering:submit`, `wagering:read`, `wallets:manage`).
**O token é a autoridade sobre o `providerId`:** um corpo que nomeie outro
provedor é recusado com 403, e operação de outro provedor some do mapa — 404,
igual a uma que nunca existiu.

Um 401 nunca diz por quê, e vem com
`WWW-Authenticate: Bearer realm="wagering-core", error="invalid_token"`.

Detalhe de escopo, isolamento e rotação de chave em
[`security.md`](security.md).

## Erros

```json
{
  "code": "INVALID_AMOUNT",
  "message": "INVALID_AMOUNT: scale of \"25.000\" exceeds 2 places",
  "correlationId": "f5830987-0c9b-4cca-aec3-c9e729eda2fe"
}
```

O **`code` é estável e é nele que o cliente ramifica.** O `message` é para
humano lendo log e pode mudar. Em 5xx a mensagem é sempre genérica: o texto
original pode carregar nome de constraint, coluna ou pedaço de SQL, e ele vai
para o log junto do `correlationId`.

| Situação | Status | `code` |
|---|---|---|
| Corpo malformado, campo desconhecido, cursor ou `limit` inválido | 400 | `INVALID_INPUT` |
| Valor com escala excedente, notação científica, vazio | 400 | `INVALID_AMOUNT` |
| Valor negativo em entrada externa | 400 | `NEGATIVE_AMOUNT` |
| Valor fora do alcance representável | 400 | `AMOUNT_OVERFLOW` |
| Moeda fora de ISO 4217 maiúsculo | 400 | `INVALID_CURRENCY` |
| Identificador vazio ou grande demais | 400 | `INVALID_IDENTIFIER` |
| Credencial ausente, inválida ou expirada | 401 | `UNAUTHENTICATED` |
| Credencial sem o escopo, ou agindo por outro provedor | 403 | `FORBIDDEN` |
| Carteira não encontrada, **ou operação de outro provedor** | 404 | `NOT_FOUND` |
| Método não suportado no caminho | 405 | — (sem corpo, com `Allow`) |
| Jogador já tem carteira nessa moeda | 409 | `WALLET_ALREADY_EXISTS` |
| Carteira já foi aberta | 409 | `WALLET_ALREADY_OPENED` |
| Versão lida está velha | 409 | `VERSION_MISMATCH` |
| Conflito sem código específico | 409 | `CONFLICT` |
| Saldo insuficiente | 422 | `INSUFFICIENT_FUNDS` |
| Erro interno | 500 | `INTERNAL` |
| Falha transitória; vale repetir | 503 | `TRY_AGAIN` |
| Operação válida que este build ainda não serve | 501 | `NOT_IMPLEMENTED` |
| Referência não chegou no prazo | 422 | `REFERENCE_NOT_FOUND` |
| Referência diverge da reversão | 422 | `REFERENCE_MISMATCH` |
| Valor da reversão diferente do referenciado | 422 | `REFERENCE_AMOUNT_MISMATCH` |
| Referenciado não é reversível | 422 | `REFERENCE_NOT_REVERSIBLE` |
| Já existe reversão bem-sucedida | 422 | `ALREADY_REVERSED` |
| Reversão não cabe no saldo | 422 | `REVERSAL_EXCEEDS_BALANCE` |
| Chave de idempotência usada por outra operação | 409 | `IDEMPOTENCY_KEY_REUSED` |

O mapa completo de código de domínio para status vive em
`internal/adapter/http/errors.go`, e um teste lê o código-fonte do domínio para
provar que ele é exaustivo — código sem mapeamento viraria 500 em silêncio.

Os códigos de conflito são nossos, não nomes de constraint. Renomear uma
constraint é migration, não mudança de API.

---

## `POST /wallets`

Abre uma carteira.

```json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" }
}
```

**201 Created**

```json
{
  "id": "01a0b58c-ed8d-7dbf-9aa7-42e37489f9e0",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1
}
```

Com saldo positivo, a carteira, a transação `OPENING` e o lançamento de crédito
são confirmados no **mesmo commit**. A versão é `1` nos dois casos: o crédito
inicial é parte de criar a carteira, não movimentação aplicada a uma existente.

**Saldo inicial `"0.00"` não cria `OPENING` nem lançamento** — a carteira nasce
vazia, na versão 1, e o extrato vem vazio.

Um jogador tem no máximo uma carteira por moeda. A segunda é **409
`WALLET_ALREADY_EXISTS`**.

## `GET /wallets/{walletId}`

**200 OK** com o mesmo corpo do `POST`. **404 `NOT_FOUND`** se não existir.

## `GET /wallets/{walletId}/ledger`

Extrato paginado. Query: `cursor` (opaco) e `limit` (padrão 50, máximo 200).

```json
{
  "entries": [
    {
      "id": "01a0b58c-ed8d-7dcb-8e08-2dea8ba849ad",
      "transactionId": "01a0b58c-ed8d-7dc7-9aa0-6b64f1b8f23a",
      "direction": "CREDIT",
      "money": { "amount": "1000.00", "currency": "BRL" },
      "balanceBefore": { "amount": "0.00", "currency": "BRL" },
      "balanceAfter": { "amount": "1000.00", "currency": "BRL" },
      "createdAt": "2026-09-18T17:25:07.085Z"
    }
  ],
  "nextCursor": "djE6Nw",
  "hasMore": false
}
```

**O cursor é opaco.** O cliente devolve o que recebeu e nada mais; construir um
à mão não é suportado. `nextCursor` só aparece quando `hasMore` é `true`.

A ordenação é estável e total, por sequência interna e não por `createdAt` —
duas entradas do mesmo commit empatam no timestamp, e empate faz cursor pular ou
repetir linha.

Carteira inexistente é **404**, não página vazia: "sem movimentação ainda" e
"essa carteira não existe" são respostas diferentes.

## `POST /wagering/transactions`

Envia uma operação. **O header `Idempotency-Key` é obrigatório.**

O servidor nunca substitui a chave recebida por uma calculada. Um cliente pode
montá-la como `{providerId}:{externalTransactionId}`, mas calcular isso quando o
header falta seria aceitar uma requisição que o cliente nunca tornou idempotente.

```http
POST /wagering/transactions
Idempotency-Key: provider-a:transaction-123
```

```json
{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}
```

**200 OK**

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

### Os tipos

`BET` debita, `WIN` credita, `LOSS` exige `"0.00"` e não movimenta nada — sem
lançamento e sem incrementar a versão da carteira.

`REFUND` e `ROLLBACK` são reversões e exigem
`referenceExternalTransactionId`. O movimento é sempre o **oposto** do que a
operação referenciada fez:

| Reversão | Pode referenciar | Movimento |
|---|---|---|
| `REFUND` | `BET` | crédito |
| `ROLLBACK` | `BET` | crédito |
| `ROLLBACK` | `WIN` | débito |
| `ROLLBACK` | `REFUND` | débito |

**Cada operação admite no máximo uma reversão bem-sucedida, de qualquer tipo.**
Não "uma por tipo": um `REFUND` e depois um `ROLLBACK` da mesma aposta são tipos
diferentes e devolveriam o mesmo dinheiro duas vezes. A segunda recebe
`ALREADY_REVERSED`.

Reverter um `REFUND` continua permitido e **não** é reverter a aposta de novo: o
`ROLLBACK` aponta para o id da devolução, cada operação foi revertida uma vez, e
o saldo acaba onde uma aposta nunca devolvida deixaria.

### Quando a referência ainda não chegou

A entrega não tem ordem, então uma reversão pode ultrapassar o que ela desfaz.
Nesse caso a resposta é **202** com `status: "PENDING_REFERENCE"` — nada se moveu
ainda, e ainda pode. Um worker tenta de novo com backoff exponencial, e a espera
sobrevive a reinício porque o estado está em coluna, não em memória.

Esgotado o prazo ou o número de tentativas, a reversão vira `REJECTED` com
`REFERENCE_NOT_FOUND`.

| Situação do referenciado | Resposta |
|---|---|
| Não existe ainda | 202 `PENDING_REFERENCE` |
| Existe, ainda não terminou | 202 `PENDING_REFERENCE` |
| `REJECTED` ou `FAILED` | 422 `REFERENCE_NOT_REVERSIBLE` — nunca moveu dinheiro |
| Tipo não reversível por esta reversão | 422 `REFERENCE_NOT_REVERSIBLE` |
| Diverge em jogador, carteira, moeda ou rodada | 422 `REFERENCE_MISMATCH` |
| Valor diferente | 422 `REFERENCE_AMOUNT_MISMATCH` |
| Já revertido | 422 `ALREADY_REVERSED` |
| Reversão não cabe no saldo | 422 `REVERSAL_EXCEEDS_BALANCE` |

`REVERSAL_EXCEEDS_BALANCE` é **deliberadamente diferente** de
`INSUFFICIENT_FUNDS`. Uma aposta sem saldo é o jogador tentando gastar o que não
tem, e é rotina. Uma reversão que não cabe é dinheiro **já entregue** que não
pode ser recolhido — problema de reconciliação, não limite de jogo, e alguém
precisa olhar. Código igual perderia o segundo no volume do primeiro.

Detalhes em [`docs/adr/0008-reversals.md`](adr/0008-reversals.md).

### Idempotência

| Situação | Resposta |
|---|---|
| Mesma chave, mesmo conteúdo | O resultado guardado, com `idempotentReplay: true` |
| Mesma chave, conteúdo diferente | 409 `IDEMPOTENCY_KEY_REUSED` |
| Mesma operação sob outra chave | 409 `CONFLICT` |
| Regra de negócio recusou | 422 com `failureCode` — e o replay devolve 422 também |

**O replay devolve o saldo observado no processamento original**, não o saldo
atual da carteira. Se outra operação caiu no meio, os dois diferem, e o chamador
tem direito à resposta que a requisição dele produziu.

Uma **rejeição é resultado gravado**, não erro: um reenvio lê a rejeição em vez
de tentar de novo. Um reenvio que de repente respondesse 200 diria ao provedor
que a aposta passou.

O que entra no hash do payload e o que fica de fora está em
[`docs/adr/0006-idempotency-hash.md`](adr/0006-idempotency-hash.md).

## `GET /wagering/transactions/{transactionId}`

**200 OK** com a operação, incluindo `status`, `failureCode` quando houver e
`balanceAfter` quando tiver sido processada.

## `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}`

O mesmo corpo, pelo par que identifica a operação para o provedor — os
identificadores que ele já tem.

## `GET /health/live`

**200** `{"status":"ok"}`. Não toca em dependência nenhuma. Ligar o banco aqui
é como um soluço de banco vira todas as réplicas reiniciando ao mesmo tempo.

## `GET /health/ready`

**200** quando as dependências respondem, **503** quando não.

```json
{ "status": "ok", "dependencies": { "postgres": "ok" } }
```
