# ADR 0008 — Reversões: o que pode desfazer o quê, e uma vez só

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-OPS-004..013
- **Decide:** F6.1

## Contexto

`REFUND` devolve uma aposta. `ROLLBACK` desfaz uma operação aplicando o
movimento contrário. As duas dependem de encontrar a operação que referenciam,
e a referência pode **ainda não ter chegado** — a entrega é at-least-once e sem
ordem.

Três perguntas precisam de resposta antes de qualquer código:

1. **O que pode reverter o quê?**
2. **Como se impede a devolução dupla do mesmo débito?**
3. **O que acontece quando a referência não está lá?**

## Decisão 1 — a tabela de reversões

| Reversão | Pode referenciar | Movimento |
|---|---|---|
| `REFUND` | `BET` | crédito (a aposta debitou) |
| `ROLLBACK` | `BET` | crédito |
| `ROLLBACK` | `WIN` | débito (o ganho creditou) |
| `ROLLBACK` | `REFUND` | débito (a devolução creditou) |

**O movimento da reversão é sempre o oposto do movimento do referenciado**, e
isso é derivado, não configurado: o tipo da operação original decide a direção, e
a reversão inverte. Uma tabela com a direção escrita à mão seria uma segunda
fonte de verdade sobre o que uma aposta faz.

`LOSS` e `OPENING` não são reversíveis. `LOSS` não moveu nada, então não há o que
desfazer; `OPENING` é interno, e reverter a abertura é apagar a carteira por
outro caminho.

Uma reversão nunca referencia outra reversão do mesmo tipo em cadeia: um
`ROLLBACK` de um `ROLLBACK` seria um jeito indireto de reaplicar a operação
original, e quem quer isso deve mandar a operação.

## Decisão 2 — no máximo **uma** reversão bem-sucedida por operação

O requisito mínimo é "uma referência não recebe duas reversões bem-sucedidas **do
mesmo tipo**". Isso é fraco demais e deixa a porta aberta:

```
BET de 25,00   → debitou 25,00
REFUND         → devolveu 25,00   ✓
ROLLBACK       → devolveria 25,00 de novo   ✗ tipos diferentes, e o dinheiro saiu duas vezes
```

A regra é mais forte: **cada operação processada admite no máximo uma reversão
bem-sucedida, de qualquer tipo.** É o que preserva a coerência financeira, e não
apenas a letra do requisito.

Isso é imposto por **índice único parcial** em `resolved_reference_id`, restrito
a reversões `PROCESSED`. No banco, não só no código: duas reversões chegando ao
mesmo tempo pela mesma referência são serializadas pela constraint, do mesmo jeito
que a idempotência (ADR 0006).

### O que continua permitido, e por quê

A cadeia `BET → REFUND → ROLLBACK(do REFUND)` é legítima. O `ROLLBACK` aqui
referencia a **devolução**, não a aposta: ele debita de volta os 25,00 que a
devolução creditou. Cada operação foi revertida uma vez, e o saldo final é o
mesmo de uma aposta que nunca foi devolvida — que é exatamente o que se espera.

A diferença é sutil e é toda a regra: **reverter o `REFUND` é diferente de
reverter a `BET` pela segunda vez.**

## Decisão 3 — quando a referência não está lá

| Situação do referenciado | Resposta |
|---|---|
| Não existe ainda | `PENDING_REFERENCE`, e um worker tenta de novo |
| Existe, `PENDING` ou `PENDING_REFERENCE` | `PENDING_REFERENCE` — ainda pode virar processado |
| Existe, `PROCESSED`, compatível, sem reversão | aplica |
| Existe, `PROCESSED`, já revertido | rejeita, `ALREADY_REVERSED` |
| Existe, `REJECTED` ou `FAILED` | rejeita, `REFERENCE_NOT_REVERSIBLE` — nunca moveu dinheiro |
| Existe, tipo não reversível por esta reversão | rejeita, `REFERENCE_NOT_REVERSIBLE` |
| Existe, mas diverge em provedor, jogador, carteira, moeda ou rodada | rejeita, `REFERENCE_MISMATCH` |
| Existe, valor diferente | rejeita, `REFERENCE_AMOUNT_MISMATCH` — reversão parcial está fora de escopo |

**Referência pendente espera, referência terminal-sem-sucesso rejeita.** A
diferença importa: uma operação `PENDING` ainda pode virar `PROCESSED`, e
rejeitar agora seria decidir cedo demais. Uma `REJECTED` nunca moveu dinheiro, e
esperar por ela seria esperar para sempre.

### O prazo

A espera tem **TTL e número máximo de tentativas**, os dois. Esgotado o que vier
primeiro, a reversão vira `REJECTED` com `REFERENCE_NOT_FOUND`.

Só tentativas seria frágil: com backoff exponencial, cinco tentativas podem ser
trinta segundos ou trinta minutos dependendo da base. Só TTL também: uma janela
longa com backoff curto gera milhares de consultas inúteis. Os dois juntos
limitam tanto o tempo de espera quanto o custo dela.

### O worker

Um worker separado varre as pendências cujo próximo instante de tentativa já
passou. Ele:

- **Sobrevive a reinício**, porque o estado da espera está em coluna, não em
  memória: tentativas, próximo instante, prazo final.
- **Suporta várias instâncias**, reservando linhas com `FOR UPDATE SKIP LOCKED` —
  duas instâncias nunca pegam a mesma pendência, e nenhuma delas espera a outra.
- **Usa backoff exponencial com jitter**, pela mesma razão do ADR 0007: sem
  jitter, as pendências criadas juntas acordam juntas.

## Decisão 4 — saldo insuficiente numa reversão tem código próprio

`ROLLBACK` de um `WIN` debita. Se o jogador já gastou o ganho, o débito não cabe.

O código dessa rejeição é **`REVERSAL_EXCEEDS_BALANCE`**, e não
`INSUFFICIENT_FUNDS`. Os dois dizem "não há saldo" e significam coisas opostas
para quem recebe: uma aposta sem saldo é o jogador tentando gastar o que não tem,
e é normal. Uma reversão que não cabe é dinheiro que **já foi entregue** e não
pode ser recolhido — é um problema de reconciliação, não um limite de jogo, e
alguém precisa olhar.

Código igual para as duas situações é como um alerta que importa se perde no
volume de um evento rotineiro.

## Consequências

**O schema ganha três colunas de espera e um índice.** Tentativas, próximo
instante e prazo final ficam na linha da transação, pelo mesmo motivo do
registro de idempotência (ADR 0006): duas fontes sobre o mesmo fato divergem.

**A resolução acontece dentro da transação que aplica.** Ler a referência fora e
aplicar depois deixaria uma janela onde ela é revertida por outro caminho — e a
constraint pegaria, mas depois de o trabalho ter sido feito.

**Os códigos de falha são contrato.** Estão em `docs/api.md`, e renomear um é
mudança de API, não refatoração.
