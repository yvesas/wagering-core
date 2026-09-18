# Glossário do domínio

> Linguagem ubíqua. Os nomes aqui são os mesmos usados no código, nas specs e nos
> contratos externos. Nome que não está aqui não deveria aparecer no domínio.

## Entidades e valores

**Valor monetário** (`Money`) — value object imutável com quantia e moeda.
Imutável porque dinheiro compartilhado e mutável é como se perde uma
movimentação sem que ninguém veja. Nunca representado em ponto flutuante.

**Carteira** (`Wallet`) — raiz do agregado financeiro. Um jogador tem no máximo
uma carteira por moeda. Guarda o saldo e a versão; o saldo só muda por dentro do
agregado, e toda mudança produz um lançamento.

**Lançamento** (`WalletLedgerEntry`) — registro imutável de uma movimentação:
direção, valor, saldo antes e saldo depois. É a prova auditável de que o saldo
chegou onde chegou.

**Ledger** — o conjunto de lançamentos de uma carteira. **Append-only:** correção
financeira é lançamento novo, nunca edição do antigo. O saldo armazenado deve
sempre bater com a soma de créditos menos débitos.

**Transação** (`WagerTransaction`) — a operação enviada por um provedor, ou a
abertura interna da carteira. Carrega o que foi pedido, quem pediu, em que
estado está e qual foi o resultado.

**Direção** — `DEBIT` tira da carteira, `CREDIT` põe.

## Origem e identidade

**Provedor** — o sistema de jogo que envia operações. Cada um enxerga apenas as
próprias transações.

**Identificador externo** — o identificador que o provedor dá à operação. O par
`(provedor, identificador externo)` identifica a operação de negócio, e é por
ele que uma reversão encontra o que reverter.

**Rodada** — a partida ou jogada a que a operação pertence. Uma reversão precisa
concordar com a rodada da operação referenciada.

**Chave de idempotência** — o que o cliente envia para dizer "esta é a mesma
requisição de antes". Distinta do identificador externo: a chave é de
transporte, o identificador é de negócio.

**Hash do payload** — impressão digital determinística dos campos de negócio,
calculada sobre JSON canônico. É o que distingue um reenvio legítimo de um
conflito — mesma chave, conteúdo diferente.

## Tipos de operação

| Termo | O que faz |
|---|---|
| **Abertura** (`OPENING`) | Crédito inicial da carteira. Origem interna; rejeitada se chegar de fora. |
| **Aposta** (`BET`) | Débito. Exige valor positivo e saldo suficiente. |
| **Ganho** (`WIN`) | Crédito. Exige valor positivo. |
| **Perda** (`LOSS`) | Não movimenta. Valor zero, sem lançamento, sem mudar a versão da carteira. Existe para registrar o desfecho. |
| **Devolução** (`REFUND`) | Crédito que desfaz integralmente uma aposta processada. |
| **Desfazimento** (`ROLLBACK`) | Movimento contrário ao original, desfazendo integralmente uma aposta, ganho ou devolução. |

**Reversão** — o termo guarda-chuva para devolução e desfazimento: operações que
existem em função de outra e precisam encontrá-la.

**Referência** — a operação que uma reversão desfaz.

## Estados

| Estado | Significado |
|---|---|
| `PENDING` | Aceita e registrada, processamento não concluído |
| `PENDING_REFERENCE` | Esperando uma referência que ainda não chegou |
| `PROCESSED` | Concluída com sucesso — terminal |
| `REJECTED` | Recusada por regra de negócio — terminal |
| `FAILED` | Falha permanente de infraestrutura, registrada para auditoria — terminal |

**Terminal** — estado do qual não se sai. Um reenvio consulta o resultado
guardado em vez de reprocessar.

**Código de falha** (`failureCode`) — identificador estável do motivo de uma
rejeição. Estável porque o provedor decide o que fazer com base nele: saldo
insuficiente numa aposta e saldo insuficiente numa reversão são situações
diferentes e recebem códigos diferentes.

## Mecanismos

**Idempotência** — a propriedade de a mesma operação, aplicada várias vezes,
produzir o mesmo efeito de uma. Aqui ela é **persistente**: vive no banco e
sobrevive ao reinício de todos os processos.

**Replay** — o reenvio de uma operação já concluída. Devolve o resultado
guardado, incluindo **o saldo observado no processamento original** — não o
saldo atual.

**Registro de entrada** (*inbox*) — tabela que guarda quais mensagens já foram
tratadas, para uma entrega repetida não virar movimentação repetida. Gravado na
mesma transação das mudanças de domínio.

**Registro de saída** (*outbox*) — tabela onde o evento é gravado junto com a
mudança que o originou. Um worker publica depois. Existe porque banco e
mensageria não compartilham transação: publicar antes do commit é publicar um
fato que pode não ter acontecido.

**Reconciliação** — reconstruir o saldo a partir do ledger e comparar com o
armazenado. Não corrige nada; só reporta.

**Lost update** — duas escritas concorrentes onde a segunda apaga o efeito da
primeira. É o modo de falha que a versão da carteira existe para impedir.
