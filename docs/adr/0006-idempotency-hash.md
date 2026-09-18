# ADR 0006 — Hash canônico do payload e onde a idempotência mora

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-IDE-001..008, REQ-MSG-003
- **Decide:** F4.1, F4.2

## Contexto

A entrega é at-least-once. A mesma operação chega repetida, por HTTP e por fila,
e o resultado financeiro tem de ser o de uma. Três perguntas precisam de
resposta antes de qualquer código:

1. **Onde o registro de idempotência vive?**
2. **Como se distingue um reenvio legítimo de uma chave reusada com outro
   conteúdo?**
3. **O que entra no hash, e o que é normalizado antes dele?**

## Decisão 1 — o registro é a própria transação

Não existe tabela `idempotency_keys`. A linha de `wager_transactions` já carrega
`idempotency_key`, `payload_hash` e `balance_after_minor`, com unicidade em
`(provider_id, external_id)` e em `(provider_id, idempotency_key)`.

Uma tabela separada precisaria ser escrita no mesmo commit da transação, e
manter duas linhas sincronizadas sobre o mesmo fato é como elas passam a
discordar. Aqui a operação e o seu registro de deduplicação **são a mesma
linha**, então não há como uma existir sem a outra.

## Decisão 2 — escrever e tratar o conflito, não checar antes de escrever

O caminho **não** é "consultar se já existe, e inserir se não". Entre a consulta
e o insert cabem outras cinco cópias da mesma requisição; todas encontram nada e
todas seguem.

O caminho é inserir e deixar o banco recusar:

```
inserir a transação
  └── violação de unicidade?
        ├── (provider, external_id)      → é a mesma operação: reler e responder
        └── (provider, idempotency_key)  → a chave é de outra operação: conflito
```

A constraint é a serialização. Quem perdeu a corrida relê a linha vencedora e
devolve **o resultado dela**, que é exatamente o que um replay deve fazer.

Isso torna o conflito um caminho normal, não excepcional — e é por isso que o
adaptador nomeia qual constraint disparou (ADR 0003): sem o nome, "conflito"
não diz se foi um reenvio ou um abuso de chave.

## Decisão 3 — o que entra no hash

**Entram** os campos de negócio: provedor, identificador externo, jogador,
carteira, rodada, jogo, tipo, valor, moeda e, quando existe, a referência.

**Não entram:**

- A **chave de idempotência**. Ela é o *rótulo* do conteúdo; incluí-la faria todo
  reenvio com a mesma chave bater por construção, e o hash deixaria de detectar
  a única coisa que precisa detectar — mesma chave, conteúdo diferente.
- **Metadados de transporte**: header, `messageId`, `occurredAt`, tentativa. Uma
  reentrega do SQS tem `messageId` diferente e é a mesma operação; se entrassem,
  HTTP e fila nunca chegariam ao mesmo valor para o mesmo pedido, que é
  justamente o requisito.

### A normalização, e por que ela precisa estar documentada

O valor monetário é normalizado **antes** do hash, pela forma canônica de
`Money.String()`. `"25"`, `"25.0"` e `"025.00"` viram `"25.00"` e produzem o
mesmo hash, porque são o mesmo dinheiro.

Essa é a única normalização. Nada mais é aparado, convertido ou preenchido:
moeda minúscula é rejeitada em vez de virar maiúscula, e espaço em volta de um
identificador é diferença de conteúdo, não ruído. A regra é a mesma do parsing —
não arredondar entrada inválida — aplicada à deduplicação.

### A codificação canônica é escrita à mão

Os pares são ordenados por chave e escritos com **prefixo de comprimento**:

```
len(chave) ":" chave "=" len(valor) ":" valor ";"
```

Concatenar `chave=valor` com separador seria ambíguo: `a=bc` e `ab=c` produzem a
mesma sequência se o separador aparecer dentro de um valor — e `externalId` vem
do provedor, que pode mandar qualquer caractere. O comprimento remove a
ambiguidade sem precisar escapar nada.

Não é `encoding/json` com mapa. A ordenação de chaves de mapa pelo `json` é
comportamento documentado, mas este hash precisa ser estável **para sempre**:
registros gravados hoje têm de continuar batendo daqui a anos. Vinte linhas
próprias, com teste de valor fixo, dependem só de nós.

**SHA-256**, em hexadecimal, prefixado com `sha256:`. O prefixo é o que permite
trocar de algoritmo depois sem confundir dois formatos.

## Consequências

**Um teste de valor fixo trava o algoritmo.** Mudar qualquer coisa — a ordem, o
separador, os campos — quebra esse teste alto. Sem ele, uma mudança silenciosa
invalidaria todos os registros já gravados, e o sintoma seria operações
duplicadas em produção, não um teste vermelho.

**O replay devolve o saldo observado no processamento original**, lido de
`balance_after_minor`. Devolver o saldo atual da carteira é mais fácil de
escrever e passa em todo teste de caminho feliz; está errado na primeira vez que
outra operação cai no meio.

**Idempotência não depende de memória de processo.** Não há cache, não há mapa
em RAM, não há nada que um reinício apague. O teste que importa é matar tudo e
reenviar.

**A mesma operação não pode ser reaplicada sob outra chave.** A unicidade de
`(provider, external_id)` garante isso no banco, e é por isso que ela é uma
constraint e não uma consulta.
