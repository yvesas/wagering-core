# ADR 0005 — `net/http` da biblioteca padrão, sem roteador

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-API-001..011
- **Decide:** F3.6

## Contexto

A API tem poucas rotas e duas delas têm parâmetro de caminho:

```
POST /wallets
GET  /wallets/{walletId}
GET  /wallets/{walletId}/ledger
POST /wallets/{walletId}/reconciliation
POST /wagering/transactions
GET  /wagering/transactions/{transactionId}
GET  /providers/{providerId}/wagering/transactions/{externalTransactionId}
GET  /health/live
GET  /health/ready
```

Até o Go 1.22 isso justificava um roteador de terceiro: o `ServeMux` da stdlib
não casava por método nem extraía parâmetro de caminho, e implementar isso à mão
dava exatamente o tipo de código que não se quer manter.

## Decisão

**`net/http` puro.** Desde o Go 1.22 o `ServeMux` aceita método e curinga no
padrão:

```go
mux.HandleFunc("GET /wallets/{walletId}", h.get)
```

e `r.PathValue("walletId")` devolve o valor. As duas razões que sustentavam a
dependência deixaram de existir.

Middleware é `func(http.Handler) http.Handler`, encadeado explicitamente.

## Motivo

**A dependência não compra mais nada aqui.** `chi` e afins continuam ótimos —
grupos de rota, middleware por subárvore, montagem de sub-roteadores. Nenhum
desses recursos aparece nestas nove rotas, e uma dependência que não é usada
ainda custa: superfície de atualização, uma segunda forma de escrever handler, e
mais um vocabulário para quem chega ler.

**A stdlib é o que todo mundo já sabe ler.** Handler é `http.Handler`,
middleware é composição de `http.Handler`. Não há registro mágico, não há
contexto proprietário, e um `http.Handler` daqui funciona em qualquer teste com
`httptest`.

**O que a stdlib não dá, não estamos pedindo.** Sem binding automático de JSON e
sem validação por tag — e isso é ganho, não perda: o payload externo é
normalizado numa camada só, à mão, onde dá para rejeitar em vez de arredondar.
Um binder que "quase" converte `"25.000"` é exatamente o que este sistema não
pode ter.

## Consequências

**A ordem dos padrões não importa, a especificidade sim.** O `ServeMux` do Go
1.22+ escolhe o padrão mais específico, então `GET /wallets/{id}/ledger` e
`GET /wallets/{id}` convivem sem depender de quem foi registrado primeiro.
Conflito genuíno é `panic` no registro — no start-up, não em produção.

**405 e 404 saem de graça.** Um método não registrado num caminho conhecido
devolve 405 com `Allow`, sem código nosso.

**Middleware é explícito e ordenado à mão.** Não há `Use()` que acumule estado
invisível: a cadeia é uma expressão, e lê-se de fora para dentro.

**Revisitar isto é barato.** Trocar por um roteador depois é reescrever o arquivo
de rotas, não os handlers, porque eles são `http.HandlerFunc` e nada mais. É por
isso que a decisão pode ser tomada agora sem travar nada.
