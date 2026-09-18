# ADR 0009 — Registro de entrada, e quando a mensagem é apagada

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-MSG-001..012, REQ-IDE-001, REQ-PER-005
- **Decide:** F7.1

## Contexto

A fila entrega **pelo menos uma vez**. A mesma operação chega repetida, e chega
também por HTTP — as duas portas precisam produzir o mesmo resultado financeiro.

Três perguntas antes do código:

1. **O que identifica uma mensagem já tratada?**
2. **Como o registro de entrada e o efeito no domínio ficam no mesmo commit?**
3. **Quando a mensagem sai da fila?**

## Decisão 1 — a identidade é o `messageId` do envelope, não o da fila

O SQS dá à mensagem um `MessageId` próprio, e ele **muda a cada reentrega** em
alguns cenários de redrive. Usá-lo como identidade do registro de entrada faria
uma reentrega parecer mensagem nova.

A identidade é o **`messageId` do envelope da aplicação**, que o produtor
escolhe e que sobrevive a qualquer coisa que o transporte faça. A unicidade é
`(consumerName, messageId)`: dois consumidores diferentes devem poder processar a
mesma mensagem, e um deles não pode roubar o trabalho do outro.

O hash do payload é guardado junto. Reentrega com o mesmo `messageId` e conteúdo
diferente é o produtor reusando um identificador, e isso precisa ser visível em
vez de silenciosamente aceito.

### Duas camadas de deduplicação, e por que as duas

O registro de entrada dedup**a a mensagem**; a idempotência (ADR 0006) dedup**a a
operação financeira**. Não são a mesma coisa:

- Duas mensagens diferentes carregando a mesma operação — porque o produtor
  reenviou com outro `messageId` — passam pelo registro de entrada e são barradas
  pela idempotência.
- A mesma mensagem entregue duas vezes é barrada pelo registro de entrada, sem
  chegar a tocar o domínio.

Só a segunda camada seria suficiente para a correção financeira, e ainda assim o
registro de entrada vale: ele evita o trabalho, e responde "isto já foi tratado"
sem precisar deduzir isso de um conflito.

## Decisão 2 — um commit só, e por isso o caso de uso participa da transação de quem chama

O requisito é explícito: o registro de entrada e a conclusão durável do
tratamento compartilham a transação SQL das alterações de domínio.

Isso força um ajuste no caso de uso. Ele abria a própria transação, o que serve
ao HTTP e não serve aqui. Passa a ter duas formas:

```go
// Abre a transação. É o que o HTTP usa.
func (uc *SubmitTransaction) Execute(ctx, cmd) (SubmitResult, error)

// Roda dentro da transação de quem chama. É o que o consumidor usa.
func (uc *SubmitTransaction) ExecuteIn(ctx, repos, cmd) (SubmitResult, error)
```

A alternativa seria o consumidor receber um "gancho de inbox" para o caso de uso
chamar. Isso inverte a responsabilidade — o caso de uso passaria a saber que
existe uma fila — e o que ganharíamos é não escrever um método.

**As duas portas compartilham `ExecuteIn`**, então não existe versão da regra
para fila e versão para HTTP. Era o requisito, e é também a única forma de as
duas não divergirem no primeiro conserto aplicado de um lado só.

## Decisão 3 — apagar só depois do commit

A ordem é: **commit primeiro, apagar depois**. Nunca o contrário.

Apagar antes é perder a operação se o commit falhar: a mensagem já não existe
para ser reentregue. Apagar depois pode entregar duas vezes — o processo morre
entre o commit e o `DeleteMessage` —, e isso é **exatamente o que o registro de
entrada absorve**. A duplicata chega, encontra o registro, e é apagada sem tocar
em nada.

A assimetria é a razão de existir do registro de entrada: ele torna a entrega
dupla barata e a perda impossível.

| Desfecho | Mensagem |
|---|---|
| Aplicada | apaga |
| Já tratada (registro de entrada recusa) | apaga |
| Rejeição de negócio | apaga — é resultado terminal, e reentregar não muda |
| Envelope inválido | apaga, depois de registrar; reentregar um payload quebrado é repetir o mesmo erro |
| Falha transitória | **não apaga** — devolve a visibilidade para reentrega |

Rejeição de negócio ser terminal é o ponto menos óbvio. Saldo insuficiente não
melhora na quinta tentativa, e manter a mensagem na fila transformaria uma recusa
numa mensagem que só sai pela fila de descarte, muito depois, parecendo falha de
infraestrutura.

## Decisão 4 — a fila de descarte é do broker, não nossa

`maxReceiveCount` e o redrive são configurados na fila. Não há contador de
tentativas no nosso código para decidir descarte.

Contar do nosso lado exigiria persistir a contagem, e aí ela estaria em dois
lugares — o nosso e o do broker — que discordariam no primeiro reinício. O broker
já conta; a política é dele.

O que **é** nosso: devolver a visibilidade cedo quando a falha é transitória, em
vez de segurar a mensagem invisível até o tempo acabar. Um consumidor que morre
sem devolver a visibilidade faz a mensagem esperar o `visibilityTimeout` inteiro
antes de alguém tentar de novo.

## Decisão 5 — FIFO, e o que o `MessageGroupId` significa aqui

A fila é FIFO e o **`MessageGroupId` é a carteira**. É a granularidade que o
sistema já usa para coordenar (ADR 0007): operações da mesma carteira são
ordenadas entre si, e carteiras diferentes são processadas em paralelo.

Agrupar por provedor serializaria todas as carteiras de um provedor atrás de uma
só. Agrupar por operação não ordenaria nada.

O `MessageDeduplicationId` é o `messageId` do envelope — a mesma identidade do
registro de entrada. A deduplicação do SQS FIFO é uma janela de cinco minutos e
**não é garantia suficiente**: é uma conveniência que reduz tráfego. A garantia é
a constraint do registro de entrada, que não tem janela.

## Consequências

**O encerramento para de buscar antes de terminar.** Em `SIGTERM` o consumidor
deixa de chamar `ReceiveMessage` e conclui o que está em voo dentro do prazo. O
que não couber no prazo tem a visibilidade devolvida, para outra instância pegar
— e não fica esperando o timeout inteiro.

**O consumidor não tem estado.** Tudo que decide reentrega está na fila ou no
banco. Duas instâncias consumindo a mesma fila é a configuração normal, não um
caso especial.

**O provisionamento das filas é feito pela aplicação no start-up**, de forma
idempotente. Um `CreateQueue` com os mesmos atributos é um no-op, então toda
réplica pode chamá-lo — pelo mesmo motivo que as migrations rodam em todas.
