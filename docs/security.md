# Autenticação e isolamento

> Como este serviço decide quem está chamando e o que essa pessoa — ou esse
> sistema — pode ver. Decisões e o porquê delas em
> [`adr/0011`](adr/0011-authentication-and-isolation.md).

## Em uma frase

**O handler autentica, o caso de uso autoriza.** O middleware HTTP transforma um
token numa identidade e para aí; quem decide o que essa identidade pode fazer é
a camada de aplicação — porque a fila chama o mesmo caso de uso sem passar por
handler nenhum.

## O que você precisa para chamar

Um `Bearer` token emitido pelo IdP configurado, para a audience deste serviço.

```
Authorization: Bearer eyJhbGciOiJSUzI1NiIs...
```

Tudo que é negócio exige credencial. As únicas rotas abertas são
`GET /health/live` e `GET /health/ready`: um load balancer não tem token, e a
resposta é uma palavra sobre o processo.

Isso não é uma lista de exceções mantida à mão. A tabela de rotas em
`internal/adapter/http/server.go` carrega um campo `public`, e **o valor zero é
protegida** — rota nova nasce autenticada. Um teste percorre a própria tabela,
então a rota que alguém registrar amanhã já está coberta hoje.

## O que o token precisa dizer

| Claim | Para quê |
|---|---|
| `iss` | Tem de bater com `OIDC_ISSUER_URL` |
| `aud` | Tem de conter `OIDC_AUDIENCE` |
| `exp` | **Obrigatório.** Token sem validade é credencial permanente |
| `sub` | Quem está chamando; é o que vai para o log |
| `provider_id` | O provedor por quem o token age. Ausente = serviço interno |
| `scope` ou `scp` | Os escopos concedidos |

Escopo desconhecido é **ignorado**, não recusado: um IdP serve mais de um
sistema, e um token com `profile` não é um token malformado.

## Os três escopos

| Escopo | Quem tem | O que abre |
|---|---|---|
| `wagering:submit` | Provedor | `POST /wagering/transactions` |
| `wagering:read` | Provedor e serviço interno | Ler operações |
| `wallets:manage` | Serviço interno | Abrir e ler carteira e ledger |

**Abrir carteira cria dinheiro** — é a única operação aqui que não move saldo, e
sim o origina. Por isso ela é do serviço interno e de mais ninguém
(REQ-SEC-004). O schema diz o mesmo do outro lado:
`wager_transactions_external_shape` recusa um `OPENING` que carregue provedor.

Carteira **não tem dono provedor** no schema: ela pertence a um jogador. Por isso
a restrição de carteira é por escopo, e não um filtro por provedor — filtrar por
coluna que não existe seria teatro.

## O isolamento entre provedores

**Na escrita:** o `providerId` do corpo tem de ser o do token. Discordância é
**403**, e a mensagem cita o provedor do próprio chamador — nunca o que foi
pedido. O corpo é recusado, não corrigido em silêncio; é a mesma regra da chave
de idempotência, e aqui o payload hash depende dos campos como chegaram.

**Na leitura:** operação de outro provedor responde **404**, com exatamente o
mesmo erro de uma operação que nunca existiu. Um 403 ali seria consulta de id
alheio: recusar é confirmar que há o que recusar.

**No replay:** não há verificação extra, e é de propósito. Uma operação é
identificada por `(provider, externalId)`; como o provedor vem do token, um
chamador não consegue nem endereçar o que é de outro. A chave de idempotência
também é única por `(provider_id, idempotency_key)` — fosse global, um 409 já
diria "alguém usou essa chave".

Consequência prática: **dois provedores podem usar o mesmo
`externalTransactionId`**, e são duas operações distintas.

## Quem é quem, e o que cada um recebe

| Chamador | `provider_id` | Escopos |
|---|---|---|
| Provedor | o dele | `wagering:submit`, `wagering:read` |
| Serviço interno | ausente | `wallets:manage`, `wagering:read` |
| Consumidor de fila | o do envelope | `wagering:submit`, implícito |

## A porta de fila

Mensagem não carrega token. A identidade sai do **envelope**, e quem garante que
o envelope é legítimo é a política de acesso do broker (REQ-SEC-005).

Dito sem eufemismo: **quem pode escrever na fila pode agir como qualquer
provedor.** É a credencial do broker que vale ali, não uma assinatura verificada
por nós. O que não muda é tudo depois: mesmo caso de uso, mesmas regras, mesmo
isolamento, e as validações de domínio inteiras.

## O que a resposta não conta

Um 401 nunca diz por quê. Expirado, assinado por chave desconhecida e emitido
para outra audience são a mesma resposta aqui — separá-las diria a quem está
sondando qual parte da falsificação consertar. O motivo vai para o log com o
`correlationId`; o cliente recebe:

```json
{ "code": "UNAUTHENTICATED", "message": "a valid bearer token is required" }
```

com `WWW-Authenticate: Bearer realm="wagering-core", error="invalid_token"`.

Nenhum log carrega token, e não é por disciplina: `app.Identity` não guarda a
credencial, então o método que a imprimiria não teria o que imprimir.

## Chaves e rotação

O conjunto de chaves é buscado por discovery e mantido em cache por
`OIDC_JWKS_CACHE_TTL` (padrão 5 min).

- **`kid` desconhecido força uma busca**, no máximo uma a cada cinco segundos.
  É o que uma rotação custa, e o teto é o que impede que token com `kid`
  inventado vire um jeito de martelar o IdP.
- **IdP fora do ar não invalida token nenhum.** As chaves em cache continuam
  verificando assinatura. O preço, dito de frente: chave aposentada segue
  aceita enquanto não der para perguntar — e é por isso que `exp` é obrigatório.
- **Algoritmos são uma lista fechada** (`RS*`, `PS*`). Sem ela o header do token
  escolheria como ele é verificado, e tanto `alg: none` quanto um HMAC assinado
  com a chave pública viram falsificação por edição de header.

## Rodando com o IdP local

`make up` sobe um Keycloak com o realm de
[`deploy/keycloak/realm.json`](../deploy/keycloak/realm.json) importado, em
`http://localhost:8081`. Três clients, todos com segredo `local-dev-only`:

| Client | Age por | Escopos |
|---|---|---|
| `provider-acme` | `acme` | `wagering:submit`, `wagering:read` |
| `provider-rival` | `rival` | `wagering:submit`, `wagering:read` |
| `platform` | ninguém | `wallets:manage`, `wagering:read` |

`provider-rival` existe para tentar o isolamento à mão: nada do que `acme`
enviar pode aparecer para ele.

```sh
make token CLIENT=provider-acme     # imprime um access token
curl -H "Authorization: Bearer $(make -s token CLIENT=platform)" \
     http://localhost:8080/wallets/<id>
```

O realm entrega os escopos num claim `scp`, e não como client scopes do
Keycloak. É uma escolha de conveniência do arquivo de import — client scopes
declarados substituem os padrões do realm e quebram o console administrativo — e
não um modelo a copiar para produção. O serviço lê `scope` e `scp`.

**Nada disso é segredo.** Emissor e audience são públicos por construção, e os
segredos de client pertencem a um realm que existe para ser jogado fora. Em
produção a credencial vem do secret manager e o realm não é nosso para importar.

## Onde isso está provado

| Arquivo | O que prova |
|---|---|
| `internal/adapter/oidc/oidc_test.go` | Treze falsificações recusadas, cache, rotação, IdP fora do ar |
| `internal/app/identity_test.go` | A identidade zero não pode nada; falta de escopo; recusa que não vaza |
| `internal/app/isolation_test.go` | Isolamento entre provedores, em escrita, leitura e replay |
| `internal/adapter/http/auth_test.go` | Toda rota de negócio exige credencial; 401 sem detalhe |
| `test/security_test.go` | O mesmo, ponta a ponta, com três processos e banco real |
