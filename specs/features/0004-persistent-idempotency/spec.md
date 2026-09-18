# 0004 — Idempotência persistente

- **Status:** implementada · 390 casos no total · `-race` limpo
- **Requisitos cobertos:** `REQ-IDE-001..008` · `REQ-API-007..008` ·
  `REQ-OPS-001..003` (BET, WIN, LOSS) · `REQ-TST-009` (parcial)
- **Depende de:** features 0001 a 0003 (concluídas)

## O que foi construído

`POST /wagering/transactions` com idempotência persistente, mais as duas
consultas de operação. `BET` debita, `WIN` credita, `LOSS` não movimenta.

## O que ficou de fora, de propósito

**`REFUND` e `ROLLBACK` respondem 501.** Eles resolvem uma referência antes de
aplicar, e essa máquina é a fase 6. Um endpoint que aceita e faz outra coisa é
pior que um que recusa dizendo o porquê.

**Retry sob conflito de versão.** `UpdateBalance` já é condicionado à versão, o
que impede lost update; o que falta é repetir a transação quando o conflito
acontece, e esse é o assunto da fase 5.

## Decisões

Todas em [`docs/adr/0006-idempotency-hash.md`](../../../docs/adr/0006-idempotency-hash.md):

- O registro de idempotência **é a própria transação** — não há tabela de chaves.
- **Inserir e tratar o conflito**, não consultar antes de escrever.
- O hash cobre os campos de negócio e **exclui a chave e o transporte**.
- A única normalização é o valor monetário, e ela está documentada.
- Codificação canônica **com prefixo de comprimento**, escrita à mão.

## Como se prova

```sh
make check
make up-test && make test-integration
```

O teste que fecha a fase derruba o grafo inteiro, sobe outro contra o mesmo banco
e reenvia — é a única forma honesta de mostrar que a idempotência não depende de
memória de processo. E ele confere o detalhe fácil de errar: o replay devolve o
saldo observado na hora, não o saldo atual.

Um segundo teste solta vinte cópias concorrentes da mesma requisição e confere
que o dinheiro se moveu uma vez.
