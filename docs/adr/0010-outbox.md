# ADR 0010 — Registro de saída: onde o evento nasce e como o publisher se recupera

- **Data:** 2026-09-19
- **Status:** aceita
- **Requisitos relacionados:** REQ-EVT-001..009
- **Decide:** F8.1

## Contexto

Banco e broker não compartilham transação. Publicar antes do commit é publicar
um fato que pode não ter acontecido; publicar depois, fora da transação, é
perder o evento se o processo morrer no meio.

O registro de saída resolve isso gravando o evento **na mesma transação** que a
mudança que o originou, e deixando a publicação para depois. Três perguntas:

1. **Onde o evento é construído?**
2. **O payload é um retrato ou uma referência?**
3. **Como o publisher recupera trabalho abandonado?**

## Decisão 1 — o evento nasce no domínio

Tipo e versão são definidos pelo construtor do evento, e o construtor mora em
`internal/domain`. É lá que o fato é conhecido: quem sabe que uma aposta foi
processada é o domínio, não o caso de uso que o orquestrou.

A tentação é construir o evento no caso de uso, onde já se tem tudo à mão. O
preço aparece no segundo caminho: a mesma operação chega por HTTP e por fila, e
um evento montado no caso de uso teria de ser montado igual nos dois — ou, mais
provavelmente, num helper que acaba sendo o construtor que deveria estar no
domínio desde o começo.

`encoding/json` é biblioteca padrão, então serializar ali não fere a golden rule.

## Decisão 2 — o payload é um retrato imutável

O evento carrega **os valores no instante em que o fato aconteceu**, serializados
na hora. Não carrega o id do agregado para alguém reler depois.

A diferença importa e é fácil de errar: um publisher que montasse o payload na
hora de publicar leria o estado **atual**. Um `WalletBalanceChanged` publicado
três segundos depois anunciaria o saldo de agora, não o que a operação produziu —
e como o atraso da publicação varia, o mesmo evento diria coisas diferentes
dependendo de quando o worker acordou.

É a mesma regra do replay de idempotência (ADR 0006), aplicada à publicação.

## Decisão 3 — o `eventId` é cunhado uma vez e nunca muda

O identificador é gerado quando o evento é **gravado**, não quando é publicado.
Republicar preserva o mesmo `eventId`, que é o que permite ao consumidor
deduplicar.

Isso é o que torna a entrega at-least-once utilizável do outro lado: o consumidor
não precisa adivinhar se duas mensagens são a mesma coisa.

## Decisão 4 — o lock da linha é o lease

O publisher reserva linhas com `FOR UPDATE SKIP LOCKED`, publica **dentro da
transação**, e marca como publicado no mesmo commit.

Isso segura um lock de linha durante uma chamada de rede, o que normalmente é
cheiro ruim. A alternativa — reservar numa transação, publicar fora, confirmar
noutra — exige um **lease com expiração**: um campo dizendo "estou trabalhando
nisto até tal hora", e um valor de timeout que precisa ser maior que a pior
publicação e menor que a paciência de quem espera. E exige confiar no relógio de
máquinas diferentes.

O lock de linha dá a mesma garantia de graça: **ele morre com a conexão**. Um
publisher que trava, é morto, ou perde a rede libera as linhas no instante em
que o Postgres percebe — sem campo, sem timeout para calibrar, sem relógio para
sincronizar. "Recuperação de trabalho abandonado" deixa de ser código nosso.

O custo é contido por três coisas: lote pequeno, **timeout de publicação
explícito** — sem ele o lock dura o que o broker quiser — e o fato de a
transação não fazer mais nada além de publicar e marcar.

## Decisão 5 — duplicata na publicação é esperada, e é responsabilidade do consumidor

Os dois pontos de interrupção que o requisito pede para demonstrar:

| Morre entre | Consequência |
|---|---|
| commit do domínio e publicação | o evento continua pendente e é publicado depois |
| publicação e marcação de publicado | o evento é publicado **de novo**, com o mesmo `eventId` |

O segundo caso é entrega dupla, e é por isso que o `eventId` é estável. Tentar
evitá-lo exigiria confirmação de duas fases com o broker, que é exatamente o
problema que o registro de saída existe para não resolver.

## Os quatro eventos

| Evento | Gatilho |
|---|---|
| `WagerTransactionProcessed` | operação concluída, incluindo a que não move dinheiro |
| `WagerTransactionRejected` | rejeição definitiva por regra de negócio |
| `WalletBalanceChanged` | saldo efetivamente alterado |
| `WagerTransactionPendingReference` | reversão registrada à espera da referência |

**`LOSS` produz `WagerTransactionProcessed` e não produz `WalletBalanceChanged`**,
porque não moveu nada. É o caso que separa "a operação terminou" de "o dinheiro
mudou", e tratá-los como um só evento apagaria a distinção.

A abertura de carteira com saldo positivo produz os dois, no mesmo commit da
carteira e do lançamento.

## Consequências

**A ordem de publicação é por sequência de gravação.** Eventos do mesmo agregado
saem na ordem em que os fatos aconteceram, porque é a ordem em que foram
gravados. Entre agregados diferentes não há ordem, e nem deveria haver.

**O destino é uma fila própria**, provisionada no start-up como as de entrada. O
agrupamento é pelo agregado, pela mesma razão do ADR 0009: ordena o que precisa
de ordem e deixa o resto em paralelo.

**Publicar é a última coisa.** Nenhum caso de uso chama o broker. Eles gravam no
registro de saída e seguem; se o broker estiver fora, a operação financeira
acontece do mesmo jeito e os eventos saem quando ele voltar.
