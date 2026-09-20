# ADR 0011 — Autenticação na borda, autorização no caso de uso

- **Data:** 2026-09-19
- **Status:** aceita
- **Requisitos relacionados:** REQ-SEC-001..007
- **Decide:** F9.1

## Contexto

O serviço tem duas portas de entrada que produzem o mesmo resultado — HTTP e
fila — e passa a ter um requisito novo: **cada provedor só enxerga o que é
seu**, inclusive em replay. A identidade vem de um IdP externo; este serviço não
cadastra ninguém e não emite token.

Quatro perguntas, e nenhuma delas é sobre JWT:

1. Onde mora a regra de quem pode o quê?
2. Como a identidade viaja até lá?
3. De onde sai o `providerId`?
4. O que responder a quem pede o que não é dele?

## Decisão 1 — o handler autentica, o caso de uso autoriza

O middleware HTTP transforma um token numa identidade e para aí. **Quem decide o
que a identidade pode fazer é o caso de uso.**

O motivo é a golden rule deste projeto, aplicada a segurança: *invariante que
mora num handler some na segunda porta.* Uma regra escrita no handler seria uma
regra do HTTP — e a fila chama `SubmitTransaction.ExecuteIn` por baixo, sem
passar por handler nenhum. Ou a regra seria duplicada no consumidor, e duas
cópias de uma regra de autorização divergem na primeira vez que uma delas é
corrigida.

O preço é que `internal/app` passa a conhecer o conceito de identidade. É um
conceito de negócio, não de transporte: "um provedor não vê a operação de outro"
é uma frase sobre o domínio, e nenhuma linha de `internal/app` sabe o que é um
`Authorization: Bearer`.

## Decisão 2 — a identidade viaja no `context`, e o caso de uso falha fechado

`app.Identity` entra no `context` na borda. Todo caso de uso começa pedindo por
ela, e **um `context` sem identidade encerra a chamada** com
`ErrUnauthenticated`.

Isto parece contradizer o ADR 0003, que recusou pôr a transação no `context`.
Não contradiz — inverte o sinal do argumento. Lá o problema era **falhar
aberto**: um repositório que não achasse a transação caía no pool e escrevia
fora dela *sem falhar*, e o bug era invisível. Aqui não existe fallback: não há
"chamador anônimo" para o qual cair. Quem esquecer de autenticar recebe erro, e
o erro é alto.

A alternativa era um campo `Caller` em `SubmitCommand`. O compilador não obriga a
preenchê-lo, e o valor zero de um campo é indistinguível de uma chamada anônima
— que é exatamente o caso que precisa ser barrado. O valor zero de `Identity`,
por outro lado, é "ninguém", e ninguém não passa em nenhuma verificação.

## Decisão 3 — o `providerId` vem de um claim próprio

Claim `provider_id`, e não `azp`/`client_id` nem `sub`.

- **`azp`/`client_id`** sai de graça em qualquer IdP, e amarra o identificador
  do provedor ao nome do client no realm. Renomear um client passaria a ser
  migração de dados — e o `provider_id` está gravado em toda linha de
  `wager_transactions`.
- **`sub`** de service account é UUID opaco: ilegível num log, ilegível numa
  consulta, e sem relação com o vocabulário do domínio.
- **Claim próprio** custa um protocol mapper no realm, que é trabalho de quem
  opera o IdP. A troca é: configuração deles, independência nossa.

Um token **sem** `provider_id` é o serviço interno. Ele não age por provedor
nenhum, e é por isso que ele não pode enviar operação — o schema diz o mesmo do
outro lado, em `wager_transactions_internal_shape`.

## Decisão 4 — recusa de escrita é 403; recusa de leitura é 404

Na escrita, o corpo que nomeia outro provedor recebe **403**, e a mensagem cita
o provedor do próprio chamador, nunca o que foi pedido. Ninguém aprende nada:
o chamador já sabia quem ele é.

Na leitura, operação de outro provedor responde **404, com o mesmo erro que uma
operação inexistente** — o sentinela `ErrNotFound` puro, não uma variante
embrulhada, porque a diferença apareceria no corpo da resposta. Um 403 ali seria
um serviço de consulta de ids alheios: **recusar é confirmar que há o que
recusar.**

O corpo que discorda é recusado, e não sobrescrito em silêncio. É a mesma
decisão que a chave de idempotência (F4.3): servidor que corrige requisição
sozinho responde uma pergunta que o cliente não fez — e, aqui, o payload hash é
calculado sobre os campos como chegaram.

### O isolamento em replay não é uma segunda verificação

Uma operação é identificada por `(provider, externalId)`. Como o `providerId` da
requisição **tem** de ser o do token, um chamador não consegue nem endereçar a
operação de outro provedor. O replay isolado sai de graça: não há o que checar.

A chave de idempotência já era única por `(provider_id, idempotency_key)` desde
a fase 2. Fosse global, um 409 diria "alguém já usou essa chave" — um oráculo de
existência de graça.

## Decisão 5 — na fila, a autoridade é o broker, e isso está dito

O consumidor monta a identidade **a partir do envelope**, com escopo de envio e
mais nada. Não há token: quem controla quem escreve na fila é a política de
acesso do broker (REQ-SEC-005).

Vale dizer sem eufemismo: **quem pode escrever na fila pode agir como qualquer
provedor.** A garantia ali é a credencial do broker, não uma assinatura que
verificamos. O que não muda é tudo depois desse ponto — mesmo caso de uso,
mesmas regras, mesmo isolamento.

## Decisão 6 — sem chave que desliga a autenticação

`OIDC_ISSUER_URL` e `OIDC_AUDIENCE` são obrigatórios e não há flag de desligar.
Essa flag é justamente aquela cujo valor errado é **invisível**: tudo funciona e
nada é verificado. O que existe no lugar é um IdP local no `docker-compose.yml`.

Pelo mesmo motivo, o discovery acontece no start-up: IdP inalcançável **falha o
boot**. Um processo que subisse assim mesmo se diria saudável e responderia 401
a todo mundo, e "tudo recusado" é muito mais difícil de ler num incidente do que
"o container não subiu".

## Consequências

- Autorização testável sem HTTP: os casos de isolamento rodam contra os casos de
  uso, com um `context` e nenhuma rede.
- A tabela de rotas passa a carregar se a rota é pública, e o valor zero é
  **protegida**. Rota nova nasce autenticada; uma lista de caminhos públicos
  erraria para o lado aberto.
- Teste que chamava caso de uso com `context.Background()` passa a falhar. É a
  garantia funcionando, e foi assim que a suíte inteira teve de declarar quem
  está chamando.
- O serviço fica dependente do IdP para subir. Não para servir: as chaves em
  cache continuam verificando assinatura enquanto o emissor estiver fora.
- Carteira não tem dono provedor no schema. O isolamento por provedor vale para
  **operação**; carteira é restrita ao serviço interno (REQ-SEC-004). Filtrar
  por uma coluna que não existe seria teatro.

## Alternativas consideradas

- **Autorizar no handler.** Menos código hoje; a fila fica sem regra amanhã.
- **Gateway decide e manda `X-Provider-Id`.** Passa a confiar num header, e
  qualquer coisa com acesso à rede interna vira provedor.
- **Emitir nossos próprios tokens.** Fora de escopo por decisão de produto, e
  seria assumir guarda de credencial que não precisamos guardar.
- **Biblioteca OIDC completa.** Traria discovery, cache e rotação prontos, e
  duas dependências. A escolha foi `golang-jwt` — que faz a parte perigosa,
  parsing e validação — com o cache de chaves escrito aqui, que é a parte que
  precisa ser testável contra um emissor que rotaciona e cai.
