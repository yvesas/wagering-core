# 0006 — Reversões e resolução de referência

- **Status:** implementada · 436 casos · `-race` limpo
- **Requisitos cobertos:** `REQ-OPS-004..013` · `REQ-TRX-007` · `REQ-API-007`
- **Depende de:** features 0001 a 0005 (concluídas)

## O que foi construído

`REFUND` e `ROLLBACK` — que antes respondiam 501 — com resolução de referência,
`PENDING_REFERENCE` para chegada fora de ordem, e um worker que resolve as
pendências com backoff, TTL e limite de tentativas.

Decisões em
[`docs/adr/0008-reversals.md`](../../../docs/adr/0008-reversals.md).

## A pergunta difícil

O requisito pede que "uma referência não receba duas reversões bem-sucedidas **do
mesmo tipo**". Isso é fraco demais:

```
BET 25,00  → debitou
REFUND     → devolveu 25,00   ✓
ROLLBACK   → devolveria de novo   ✗ tipo diferente, dinheiro em dobro
```

A regra implementada é **uma reversão bem-sucedida por operação, de qualquer
tipo**, por índice único parcial. E a cadeia `BET → REFUND → ROLLBACK(do REFUND)`
continua valendo: o rollback aponta para a devolução, não para a aposta.

## Um bug que só a integração pegou

O `UPDATE` da transação ficou com nove argumentos contra seis placeholders. Os
testes de unidade usam o fake em memória, que não tem SQL para conferir — o
sintoma apareceu no worker rodando contra PostgreSQL de verdade, com
`mismatched param and argument count` repetindo no log a cada segundo.

É o argumento para os testes de integração, demonstrado.

## Como se prova

```sh
make check
make up-test && make test-integration
make test-concurrency
```

Os cenários de ponta a ponta cobrem a reversão que ultrapassa o que desfaz, a
que expira sem nunca encontrar a referência, e um `REFUND` com um `ROLLBACK` da
mesma aposta soltos juntos em instâncias diferentes.
