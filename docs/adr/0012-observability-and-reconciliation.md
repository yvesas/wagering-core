# ADR 0012 — Métrica atrás de porta, e a reconciliação numa vista só

- **Data:** 2026-09-20
- **Status:** aceita
- **Requisitos relacionados:** REQ-OBS-001..004, REQ-API-010..011
- **Decide:** F10.1

## Contexto

A última fase pede três coisas que parecem independentes e não são: log com os
identificadores certos, métricas de resultado e de atraso, e um endpoint que
reconstrói o saldo a partir do ledger e compara.

O que as une é a pergunta *"como alguém descobre que algo está errado, e o que
faz em seguida"*. Métrica dispara; log diz onde olhar; reconciliação diz se o
dinheiro bate. Cada uma sozinha deixa metade do incidente sem resposta.

Quatro decisões.

## Decisão 1 — métrica é porta, e o coletor é `prometheus/client_golang`

`internal/app` declara quatro interfaces estreitas — operação, fila, registro de
saída e reconciliação — e o adaptador em `internal/adapter/metrics` satisfaz as
quatro. Nada acima dele sabe o que é um contador, um histograma ou um label.

A escolha do coletor vem depois dessa estrutura, e é justamente por isso que ela
é **barata**: trocar é reescrever um pacote, e `app-check` garante que continue
assim. Dito isso:

- **OpenTelemetry** resolve correlação **entre** serviços, e aqui há um só.
  Métrica OTel em Go quase sempre termina exportada para Prometheus, o que
  pagaria SDK mais exportador para chegar no mesmo endpoint. Tracing com OTel
  continua adiado, como estudo separado.
- **`expvar`** é stdlib e não tem histograma, nem label, nem quantil. Os três
  viravam código nosso, e é onde implementação caseira de métrica costuma errar.

### Label é conjunto fechado, e identificador não é label

**Nenhum método da porta aceita id de carteira, provedor ou valor.** Um label
sem limite é como um backend de métrica cai — derrubado pela instrumentação que
existia para observá-lo.

O caso concreto é a rota HTTP: o label vem da **tabela de rotas**, nunca do
caminho que chegou. `/wallets/{walletId}` é uma série; `/wallets/01a0…` seria
uma série por carteira. A tabela é lista fechada, então o limite é estrutural e
não depende de alguém lembrar de sanitizar.

Identificador vai para o **log**, onde é uma string e não uma série nova.

### Recorder nulo é intratável, não fatal

Todo construtor troca `nil` por `NoMetrics{}`. Métrica que entra em pânico
derrubaria uma operação financeira para registrar que a operação aconteceu —
o que é exatamente a ordem errada de prioridades.

## Decisão 2 — `/metrics` numa porta separada, sem credencial

Segundo `http.Server`, em `APP_METRICS_ADDR` (padrão `:9090`), que não é
publicada para fora da rede local.

A alternativa era a porta de negócio. Nela, ou o endpoint é público — e
contador por provedor e por status conta volume de negócio a quem alcançar a
porta — ou é protegido, e aí o Prometheus precisa de client no realm, arquivo de
credencial e alguém para renovar. **Um scraper não é um provedor.**

Há um ganho de isolamento junto: scrape não disputa o limite de conexões do
servidor de negócio, e o dreno do servidor de negócio não espera por scrape. O
`OnStop` das métricas tem prazo próprio de dois segundos — o pior que um scrape
cortado custa é uma amostra, e o Prometheus pergunta de novo em quinze.

## Decisão 3 — reconciliação lê numa **vista só**, e por isso tem porta própria

Esta é a decisão que quase passou despercebida, e é a mais importante da fase.

Reconciliação lê duas coisas: o saldo da carteira e a soma do ledger. O reflexo é
pôr as duas dentro de uma transação e considerar o assunto resolvido. **Não
resolve.** O padrão do PostgreSQL é `READ COMMITTED`, onde **cada statement**
tira seu próprio snapshot — então as duas leituras podem cercar o commit de
outra pessoa. Uma aposta caindo no meio é contada numa e não na outra, e o
resultado é uma divergência que o banco reporta e que **nunca existiu**.

Reconciliação que grita lobo é reconciliação que as pessoas aprendem a ignorar,
e isso é pior do que não ter.

Daí a porta `Snapshot`, separada de `UnitOfWork`:

- **`REPEATABLE READ`** prende a transação inteira a um snapshot.
- **`READ ONLY`** diz ao banco o que o caso de uso diz em prosa. Custa nada, e o
  que sobra é a garantia que sobrevive a alguém editar o caso de uso.
- **Sem retry**, porque não há o que serializar: tira snapshot e lê.

Separada, e não uma opção booleana no `UnitOfWork`: as duas têm garantias
diferentes, e um argumento `readOnly bool` deixaria pedir a errada sem perceber.

O teste de integração prova isso contra PostgreSQL de verdade — e falha com
`ReadCommitted`, com `100.00` e depois `75.00` na mesma transação. O teste em
memória mostra o outro lado de propósito: lá a aposta intercalada **produz** a
divergência. Os dois são um par.

## Decisão 4 — reconciliação não corrige, e divergência responde 200

**Não escreve nada.** Uma reconciliação que corrigisse o que encontrou destruiria
a evidência de como os dois se separaram. Correção é lançamento novo, levantado
por gente que olhou.

E responde **200 com o veredito no corpo**, não 409. Quem chamou perguntou se os
dois batem; responder "não batem" é o endpoint funcionando. Um não-2xx faria
todo monitor tratar uma verificação que funcionou como requisição quebrada — de
trás para frente, justamente no endpoint cujo modo de falha é ser ignorado.

Ela é `POST` mesmo sem mudar nada: lê todo lançamento que a carteira já teve.
`GET` convida cache, prefetch e retry-por-timeout, e nenhum deles devia decidir
com que frequência isso roda.

A divergência sai em **três lugares**, e cada um responde uma pergunta: o corpo
responde a quem perguntou, a métrica é o que dispara alerta, e o log em nível
`ERROR` diz qual carteira e por quanto. Nenhum dos três substitui os outros.

## Consequências

- Um `submit` que falha por `ErrVersionMismatch` conta em
  `wallet_contention_total`; as tentativas que o retry absorveu contam em
  `transaction_retries_total`. Juntos dizem quanto da disputa ficou escondida —
  que é o número que o ADR 0007 mediu à mão.
- O atraso da publicação é medido **do instante do fato**, não de quando o
  publisher pegou a linha. A segunda leitura responderia "com que rapidez
  publicamos o que escolhemos publicar", que não é pergunta de ninguém.
- Entrega duplicada é contada e **não** é cronometrada: a segunda é quase de
  graça, e incluí-la faria uma tempestade de reentrega parecer melhora.
- `internal/app` ganhou um logger em `SubmitTransaction`. A linha "operation
  settled" é a única com todos os identificadores, e é a mesma nas duas portas.
- A porta de métricas é mais um `http.Server` no grafo Fx, com `OnStart` e
  `OnStop` próprios. Nos testes multi-processo cada instância precisa da sua, o
  que apareceu na primeira execução com três processos brigando pela `:9090`.

## Alternativas consideradas

- **Contar dentro do caso de uso, com a biblioteca.** Menos indireção hoje;
  `app-check` vermelho e a camada de aplicação escrita no vocabulário de um
  coletor.
- **Uma interface `Metrics` larga.** O adaptador implementa uma só de qualquer
  forma; o que muda é o caso de uso depender dos dois métodos que chama em vez
  de todos.
- **Reconciliação em lote, varrendo tudo.** É o próximo passo natural e precisa
  de decisões que este endpoint não precisa — janela, prioridade, o que fazer
  com dez mil carteiras. Por carteira e sob demanda primeiro.
- **Corrigir a divergência automaticamente.** Apaga a evidência do bug que a
  produziu, que é a única coisa que ninguém consegue recuperar depois.
