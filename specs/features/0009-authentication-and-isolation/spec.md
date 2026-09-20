# 0009 — Autenticação e isolamento entre provedores

- **Status:** implementada
- **Requisitos cobertos:** `REQ-SEC-001..007`
- **Depende de:** features 0001 a 0008 (concluídas)

## O que foi construído

Autenticação por OIDC externo na borda HTTP, e isolamento entre provedores
dentro dos casos de uso. Um IdP local no compose para uso à mão; a suíte
automatizada não depende dele.

Decisões em [`docs/adr/0011`](../../../docs/adr/0011-authentication-and-isolation.md);
o contrato para quem chama em [`docs/security.md`](../../../docs/security.md).

## A decisão que mais vale explicar

**O handler autentica; o caso de uso autoriza.**

A tentação é óbvia: o token chega no HTTP, o header é lido no HTTP, então a
regra "este provedor pode ver isto" também mora no HTTP. É uma linha de
middleware e acabou.

O preço aparece na segunda porta. A fila chama `SubmitTransaction.ExecuteIn`
direto — sem handler, sem header, sem middleware. Uma regra escrita na borda
simplesmente não existiria ali; ou seria copiada para o consumidor, e duas
cópias de uma regra de autorização divergem na primeira vez que **uma** delas é
corrigida.

É a golden rule do projeto aplicada a segurança: *invariante que mora num
handler some na segunda porta.* O custo é `internal/app` conhecer o conceito de
identidade — e ele é um conceito de negócio, não de transporte. Nenhuma linha de
`internal/app` sabe o que é `Authorization: Bearer`.

## O argumento do ADR 0003, com o sinal invertido

A identidade viaja no `context`. Isso parece contradizer o ADR 0003, que recusou
pôr a transação lá.

Não contradiz — inverte o sinal. Lá o problema era **falhar aberto**: um
repositório que não achasse a transação no `context` caía no pool e escrevia
fora dela *sem falhar*. O bug era invisível.

Aqui não existe fallback. Não há chamador anônimo para o qual cair: todo caso de
uso começa pedindo o chamador, e um `context` sem identidade encerra a chamada.
Esquecer de autenticar vira erro alto, não buraco silencioso.

A alternativa — um campo `Caller` no comando — parece mais explícita e é pior: o
compilador não obriga a preenchê-lo, e o valor zero de um campo é
indistinguível de uma chamada anônima, que é justamente o caso a barrar.

## O isolamento em replay não custou código nenhum

`REQ-SEC-003` pede que um provedor não alcance a operação de outro, **inclusive
em replay**. Era o requisito que parecia exigir a verificação mais delicada.

Não exigiu nenhuma. Uma operação é identificada por `(provider, externalId)`, e
o `providerId` da requisição tem de ser o do token — então um chamador não
consegue *endereçar* a operação de outro provedor. Não há o que checar.

A chave de idempotência já era única por `(provider_id, idempotency_key)` desde
a fase 2. Fosse global, um 409 de chave reusada diria "alguém já usou essa
string": um oráculo de existência de graça, e a fase 9 teria começado com uma
migration.

## Recusar é confirmar

Escrita e leitura respondem diferente, e a diferença é o ponto:

- **Escrever como outro provedor** é 403. O chamador já sabe quem ele é, e a
  mensagem cita o provedor **dele**, nunca o que foi pedido.
- **Ler a operação de outro** é 404 — e não um 404 parecido: o mesmo
  `ErrNotFound` puro que uma operação inexistente produz, porque uma variante
  embrulhada apareceria no corpo da resposta.

Um 403 na leitura seria um serviço de consulta de id alheio.

## O que a suíte recusa

`internal/adapter/oidc/oidc_test.go` é uma tabela de treze falsificações, cada
uma errada de um jeito diferente. As duas que valem nomear:

- **`alg: none`** — editar o header para dizer que o token não é assinado.
- **HMAC com a chave pública** — a chave pública é pública, então um verificador
  que honrasse `HS256` estaria conferindo um MAC contra um segredo que o
  atacante também tem.

As duas morrem na mesma linha: a lista fechada de algoritmos, que é o que
transforma o header de instrução em declaração.

Os testes assinam com chave RSA de verdade contra um emissor em processo. O
Keycloak fica fora: o que ele acrescenta — realm, console, tela de login — não é
o que precisa ser provado, e um container por execução de `make check` é o
caminho mais curto para ninguém rodar `make check`.

## O que a garantia quebrou, e por quê isso é bom

Todo teste que chamava um caso de uso com `context.Background()` passou a
falhar: duas dezenas deles, de uma vez. Não era regressão — era a falha fechada
funcionando, e a suíte inteira teve de declarar quem está chamando.

O teste do adaptador OIDC também pegou uma escolha ruim minha: a janela mínima
entre buscas de chave estava em trinta segundos, o que fazia uma rotação recusar
tokens legítimos por meio minuto. O cenário de rotação falhou, e o número virou
cinco segundos — com relógio injetado, porque suíte que dorme é suíte que as
pessoas param de rodar.

## Como se prova

```sh
make check
make up-test && make test-integration
make test-scenarios
```

`test/security_test.go` repete o isolamento ponta a ponta: três processos, banco
real, token assinado. O provedor `rival` tenta reenviar, ler por id e ler por id
de negócio o que `acme` enviou — e recebe 403, 404 e 404, de instâncias
diferentes das que gravaram.

Para tentar à mão, `make up` sobe o Keycloak e `make token CLIENT=provider-rival`
imprime a credencial de quem não deveria ver nada.
