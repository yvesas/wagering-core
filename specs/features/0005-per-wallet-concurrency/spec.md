# 0005 — Concorrência por carteira

- **Status:** implementada · 407 casos · `-race` limpo
- **Requisitos cobertos:** `REQ-CON-001..008` · `REQ-WAL-008` · `REQ-TST-005` ·
  `REQ-TST-010` · `REQ-TST-011`
- **Depende de:** features 0001 a 0004 (concluídas)

## O que foi construído

`SELECT ... FOR UPDATE` na linha da carteira, a condição de versão mantida como
segunda garantia, e retry limitado com backoff e jitter no unit of work.

Decisão e trade-offs em
[`docs/adr/0007-per-wallet-concurrency.md`](../../../docs/adr/0007-per-wallet-concurrency.md).

## Os cenários, em três processos

`make test-concurrency` compila o servidor, sobe três instâncias contra o mesmo
banco e dispara nas três. Goroutine não serviria: compartilha pool e memória, e
um bug que dependesse de lock local passaria em todas.

| Cenário | O que prova |
|---|---|
| Duas apostas de 80,00 sobre 100,00 | Uma processada, uma rejeitada, saldo 20,00, um débito |
| Cinquenta cópias idênticas | Um débito, versão 2 |
| Doze carteiras, quatro apostas cada | Carteiras independentes avançam em paralelo |
| Quarenta apostas distintas numa carteira | Contenção real: todas passam |
| Trinta apostas de 10,00 sobre 100,00 | Exatamente dez aceitas, saldo 0,00, nunca negativo |

Toda cena termina conferindo saldo armazenado contra créditos menos débitos, em
unidades mínimas — usar float na verificação faria ela depender justamente do
que está verificando.

## O que mediu a decisão

Removendo o `FOR UPDATE` e deixando só versão e três tentativas, o cenário das
quarenta apostas recusa boa parte com `409 VERSION_MISMATCH`. O dinheiro
continua certo; o que quebra é a disponibilidade, e quebra sob carga — que é a
forma de degradação mais difícil de explicar.
