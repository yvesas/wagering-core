# Arquitetura — como o sistema é hoje

> Este documento descreve o que **existe**, não o que está planejado. O desenho
> pretendido vive em `specs/project/PROJECT.md` e no `spec.md` de cada feature.
>
> **Estado atual:** domínio, persistência, os cinco tipos de operação, duas
> portas de entrada, registro de entrada, registro de saída com publicação,
> idempotência persistente, coordenação por carteira e três workers, compostos
> por Fx. Não há autenticação nem reconciliação.

## O que existe

```
internal/domain/            o núcleo, sem import de infraestrutura
├── errors.go               Code, Error e as sentinelas
├── identifier.go           os identificadores opacos
├── money.go                Currency e Money
├── ledger.go               Direction e LedgerEntry
├── wallet.go               Wallet, Movement e Opening
└── transaction.go          Kind, Origin, Status e WagerTransaction

internal/app/               as portas, declaradas por quem consome
├── ports.go                leitores, repositórios, Repositories, UnitOfWork
├── identity.go             quem está chamando, e o que pode fazer
└── errors.go               as falhas que um adaptador pode reportar

internal/adapter/postgres/  a implementação
├── uow.go                  UnitOfWork, Repositories, Queries
├── wallet.go · ledger.go · transaction.go   SQL explícito
├── errors.go               tradução de SQLSTATE na borda
├── pool.go · migrate.go    pool e migrations embarcadas

internal/adapter/http/      a borda de entrada
├── server.go               rotas, middleware, timeouts
├── auth.go                 o token vira identidade, e para aí
├── wallet.go               handlers e DTOs
├── health.go               liveness e readiness
└── errors.go               erro → status, tabela exaustiva

internal/adapter/oidc/      verificação de token contra o IdP externo
├── oidc.go                 discovery, claims e a lista de algoritmos aceitos
├── jwks.go                 o conjunto de chaves, com cache e rotação
└── oidctest/               um emissor em processo, para as três suítes

internal/adapter/system/    relógio e geração de identidade (UUIDv7)

internal/platform/          o único lugar que conhece Fx, junto de cmd/
├── modules.go              os módulos e os hooks de ciclo de vida
├── app.go                  configuração de processo e logger
└── config.go               configuração de banco

migrations/                 o SQL versionado, embarcado por go:embed
cmd/migrate/                aplica, reverte e reporta a versão
cmd/api/                    o servidor
```

Duas regras rodam dentro do `make check`: `domain-check` garante que
`internal/domain` não importa infraestrutura, e `app-check` garante o mesmo para
`internal/app` — que pode importar o domínio e mais nada.

## Os tipos e o que cada um garante

### `Money`

Value object imutável: `int64` de unidades mínimas mais a moeda. Escala fixa de
duas casas, ISO 4217 em três letras maiúsculas. Ponto flutuante não aparece em
etapa nenhuma. Ver `docs/adr/0002-money-representation.md`.

Campos não exportados, então o zero é detectável e é rejeitado por toda
operação. O tipo é comparável, e `a == b` é verdadeiro exatamente quando valor e
moeda coincidem.

Duas portas de entrada por string: `ParseMoney` aceita sinal negativo, porque
diferença interna é legitimamente negativa; `ParseExternalMoney` acrescenta a
regra de que entrada financeira externa não é. `NewMoneyFromMinor` é a
reidratação a partir do que o banco guardou.

Overflow é checado explicitamente em quatro pontos — parsing, soma, subtração e
negação — porque o estouro de inteiro em Go é silencioso.

### `LedgerEntry`

Registro imutável de uma movimentação: direção, valor, saldo antes e saldo
depois. O construtor valida `saldoDepois = saldoAntes ± valor` e é também o
caminho de reidratação, de propósito: reconferir a aritmética quando a linha
volta do banco é como um ledger corrompido se denuncia em vez de ser lido como
fato.

Valor de um lançamento é sempre positivo — a direção carrega o sinal. Operação
que não move dinheiro não produz lançamento de zero, produz lançamento nenhum.

`applyDirection` é a única definição do que uma direção faz com um saldo. O
lançamento e a carteira passam os dois por ela, então não há como discordarem
sobre o que é um débito.

### `Wallet`

Raiz do agregado. Todo método que muda saldo tem **receptor por valor e devolve
uma carteira nova**. Um débito recusado deixa a carteira do chamador intacta, e
com receptor por ponteiro isso seria uma promessa que o tipo cumpre só enquanto
todo retorno antecipado lembrar de cumpri-la.

`Debit` e `Credit` devolvem um `Movement` — a carteira nova e o lançamento
juntos, porque são confirmados juntos. Saldo que se moveu sem o lançamento é
exatamente a corrupção que o ledger existe para excluir.

`OpenWallet` e `RehydrateWallet` são caminhos separados. A abertura com saldo
positivo produz a carteira **na versão 1** mais o lançamento de crédito: o
crédito inicial faz parte de criar a carteira, não é movimentação aplicada a uma
existente, então não há versão anterior de onde avançar. Abertura em zero não
produz lançamento nenhum.

A reidratação não reaplica movimentação, não dispara transição e não emite
evento — mas valida, e um saldo negativo armazenado é recusado como corrupção.

### `WagerTransaction`

Um tipo cobre as duas origens, não dois. As transições são a parte arriscada, e
dois tipos significariam duas cópias delas. A segurança que tipos separados
comprariam fica de pé assim mesmo: os campos são não exportados e os dois
construtores recebem structs de parâmetro diferentes, então uma abertura não tem
onde pôr um id de provedor.

`OPENING` é recusado quando chega de fora — aceitá-lo deixaria um provedor
cunhar saldo.

A máquina de estados está escrita uma vez, no mapa `allowedTransitions`, e
nenhum método inventa aresta própria. Os três estados terminais não têm aresta
de saída, que é o que torna o replay seguro: o resultado guardado é a única
resposta possível.

`MarkProcessed` guarda **o saldo observado naquele momento**. Devolver o saldo
atual da carteira seria mais fácil de escrever, passaria em todo teste de
caminho feliz, e estaria errado na primeira vez que outra operação caísse no
meio.

## Erros

`Code` é o identificador estável de uma rejeição, e é o que um provedor lê para
decidir o que fazer. As sentinelas são `*Error` carregando só um código, e
`Error.Is` compara por código — um conceito cobre os dois papéis, comparar com
`errors.Is` e extrair o código com `errors.As`, em vez de duas listas paralelas
que divergem.

Nenhuma rejeição de negócio viaja como `panic`.

## A fronteira transacional

`internal/app` declara `UnitOfWork`; o adaptador a implementa. O caso de uso pede
atomicidade e recebe, dentro de um callback, os repositórios já ligados àquela
transação — ver `docs/adr/0003-transactional-boundary.md`.

Erro do callback faz rollback, `nil` faz commit, pânico faz rollback e
repropaga. Nada dentro do callback chama `Commit` ou `Rollback`, e é por isso que
nenhum dos dois pode ser esquecido.

O rollback roda num contexto que não pode ser cancelado. No contexto do
chamador, uma requisição já cancelada faria o rollback falhar e a transação
ficaria aberta até a conexão ser recolhida — que é como um cliente que desligou
vira um lock que ninguém explica.

Duas guardas parecem redundantes e não são. `UpdateBalance` condiciona a escrita
à versão lida, numa instrução só, sem janela entre checar e escrever. `Update` de
transação recusa mover linha já terminal: o domínio sabe o que o chamador tem em
memória, e o `WHERE` sabe o que está gravado — e um worker que acordou tarde
segura um `PENDING` velho.

## O que o banco garante

24 constraints, 5 índices únicos e 2 triggers. O ledger é append-only no banco,
não só no tipo: `REVOKE` sozinho não serviria, porque não alcança o dono da
tabela e migration roda como dono. São **dois** triggers porque `TRUNCATE` não
dispara trigger de linha — sem o de statement, a guarda teria uma porta ao lado.

Consequência prática: teste de integração não limpa o que criou. Não dá `DELETE`
nem `TRUNCATE` no ledger. Os testes usam identificadores únicos por execução, o
que dá isolamento sem precisar desfazer nada.

## Composição e ciclo de vida

Fx vive em `internal/platform` e `cmd/`, e mais nada o conhece —
`make app-check` falha se `internal/app` importar. Tudo que ele monta é
construtor comum, o que faz o mesmo código ser montado por três linhas num
teste. Ver `docs/adr/0004-fx-only-at-the-edge.md`.

O start **valida**: o pool dá ping, as migrations rodam e configuração
malformada falha nomeando o que está errado. `APP_SHUTDOWN_TIMEOUT=30` sem
unidade é recusado em vez de cair no padrão.

O listener abre **dentro do hook de start**, não dentro do `Serve`. Deixado para
a goroutine, porta ocupada faria o processo reportar "started" e não servir nada.

Os `OnStop` rodam na ordem inversa dos `OnStart`, e é só isso que garante que o
**pool feche depois do servidor drenar**. Fechado antes, toda requisição que o
drain existe para terminar falharia com conexão morta no último milissegundo.

## A borda HTTP

`net/http` puro. Desde o Go 1.22 o `ServeMux` casa método e extrai curinga de
caminho, que eram as duas razões da dependência. Ver
`docs/adr/0005-stdlib-http.md`.

A tabela de erro → status é **exaustiva sobre os códigos do domínio**, e um
teste lê o código-fonte do domínio para provar que continua. Código sem
mapeamento viraria 500: erro do cliente reportado como culpa nossa.

Conflito responde com código desta API, não com nome de constraint. Devolver o
nome vazaria detalhe de schema e faria o tratamento de erro do cliente depender
dele — renomear constraint é migration, não mudança de contrato.

Contrato completo em `docs/api.md`.

## Identidade

**O handler autentica, o caso de uso autoriza.** O middleware transforma o
`Bearer` token numa `app.Identity` e a põe no `context`; quem decide o que essa
identidade pode fazer é a camada de aplicação — porque a fila chama o mesmo caso
de uso sem passar por handler nenhum. É a golden rule aplicada a segurança.

Todo caso de uso começa pedindo o chamador, e um `context` sem identidade
encerra a chamada. Não há fallback para "anônimo", que é o que torna esquecer de
autenticar um erro alto em vez de um buraco silencioso.

A tabela de rotas carrega se a rota é pública, e o **valor zero é protegida**.
Um teste percorre a própria tabela, então rota nova já nasce coberta.

O `providerId` vem do token, nunca do corpo: o corpo pode concordar, e discordar
é 403. Ler operação de outro provedor é 404 — o mesmo erro de uma que nunca
existiu, porque recusar é confirmar que há o que recusar.

Decisões em `docs/adr/0011-authentication-and-isolation.md`; escopos, rotação de
chave e o IdP local em `docs/security.md`.

## Idempotência

**Não existe tabela de chaves.** A linha de `wager_transactions` já carrega a
chave, o hash e o saldo observado, com unicidade em `(provider, external_id)` e
em `(provider, idempotency_key)`. Duas linhas sobre o mesmo fato é como elas
passam a discordar.

**O caminho não é consultar antes de escrever.** Entre a consulta e o insert
cabem outras cinco cópias da mesma requisição. O caminho é inserir e deixar a
constraint recusar: quem perdeu a corrida relê a linha vencedora e devolve o
resultado dela. A consulta que vem antes é otimização; a constraint é a garantia.

**O hash não inclui a chave nem metadado de transporte.** Incluir a chave faria
todo reenvio bater por construção; incluir `messageId` impediria HTTP e fila de
chegarem ao mesmo valor. A única normalização é o valor monetário pela forma
canônica de `Money.String()`, e ela está documentada porque hash com entrada
reescrita em silêncio é hash que ninguém reproduz. Ver
`docs/adr/0006-idempotency-hash.md`.

**Rejeição é resultado gravado, não erro.** O desfecho mora em
`Transaction.Status()`, não no erro devolvido: se a rejeição viajasse como erro,
o primeiro envio falharia e o replay teria sucesso, porque um replay lê linha
gravada e não tem o que falhar.

## Concorrência

Três camadas, e a primeira é a que coordena de verdade — ver
`docs/adr/0007-per-wallet-concurrency.md`.

**`SELECT ... FOR UPDATE` na linha da carteira**, dentro da transação. Trava-se
uma linha, não uma tabela: carteira A não faz carteira B esperar, que é a
definição de "carteiras independentes avançam em paralelo".

**`UPDATE` condicionado à versão** continua, como segunda garantia. Não é
redundância: o `FOR UPDATE` protege quem passou por ele, e um caso de uso futuro
que leia sem travar e depois escreva não é protegido por nada.

**Retry com backoff e jitter, limitado**, no unit of work. Só repete o que é
transitório — falha de serialização, deadlock, conflito de versão. Rejeição de
negócio nunca: saldo insuficiente não melhora tentando de novo, e repetir
transformaria uma recusa numa espera.

O jitter é o ponto. Sem ele, os escritores que colidiram juntos dormem o mesmo
tempo e colidem de novo no mesmo instante — o backoff sincronizaria exatamente o
que deveria espalhar.

**Uma transação trava exatamente uma linha de carteira, e sempre a carteira.**
Com um só recurso travado não há ciclo, então não há deadlock por ordenação.
Isso constrange o código futuro: uma operação que precise de duas carteiras terá
de travá-las por identificador crescente.

## Reversões

`REFUND` desfaz uma aposta; `ROLLBACK` desfaz aposta, ganho ou devolução. **A
direção do movimento é derivada do tipo referenciado**, não configurada: uma
tabela escrita à mão seria uma segunda opinião sobre o que uma aposta faz.

**Uma reversão bem-sucedida por operação, de qualquer tipo.** O requisito mínimo
é "não duas do mesmo tipo", e isso deixa a porta aberta: `REFUND` e depois
`ROLLBACK` da mesma aposta devolveriam o mesmo dinheiro duas vezes. A regra forte
é imposta por índice único parcial em `resolved_reference_id`, restrito a
reversões `PROCESSED` — no banco, do mesmo jeito que a idempotência.

Reverter um `REFUND` continua valendo, e é diferente de reverter a aposta de
novo. A distinção é sutil e é toda a regra.

**Referência pendente espera; terminal-sem-sucesso rejeita.** Uma operação ainda
`PENDING` pode virar `PROCESSED`, e rejeitar agora seria decidir cedo demais. Uma
`REJECTED` nunca moveu dinheiro, e esperar seria esperar para sempre.

A espera tem **TTL e número máximo de tentativas**, os dois: só tentativas é
frágil com backoff exponencial, e só TTL gera consultas inúteis. Ver
`docs/adr/0008-reversals.md`.

## O worker de pendências

Varre reversões cujo próximo instante de tentativa já passou, reservando com
`FOR UPDATE SKIP LOCKED` — duas instâncias nunca pegam a mesma pendência e
nenhuma espera a outra.

**Uma pendência por transação.** Isso não é otimização, é a regra de deadlock do
ADR 0007: uma transação trava exatamente uma linha de carteira. Reservar vinte
pendências de vinte carteiras num commit só tomaria vinte locks na ordem que a
varredura devolvesse, e dois workers acabariam tomando dois deles em ordens
opostas.

O estado da espera vive em coluna, então um reinício encontra o trabalho onde
parou.

## A fila

O consumidor vê quatro métodos e nenhum vocabulário de transporte; o SDK da AWS
fica contido em `internal/adapter/sqs`. A verificação: `go list -deps` sobre
`internal/app` não traz nenhum pacote da AWS.

**Duas camadas de deduplicação, e não são a mesma coisa.** O registro de entrada
dedupa a **mensagem**; a idempotência dedupa a **operação financeira**. Duas
mensagens diferentes carregando a mesma operação passam pelo registro de entrada
e são barradas pela idempotência; a mesma mensagem entregue duas vezes é barrada
antes de tocar o domínio.

**A identidade é o `messageId` do envelope, não o da fila.** O id que o SQS dá à
mensagem muda em alguns cenários de redrive, e usá-lo faria uma reentrega parecer
mensagem nova. A unicidade é `(consumerName, messageId)`.

**Um commit só.** O registro de entrada e o efeito no domínio são a mesma
transação. Isso obrigou o caso de uso a ganhar duas formas: `Execute` abre a
transação (é o que o HTTP usa) e `ExecuteIn` roda dentro da transação de quem
chama (é o que o consumidor usa). As duas passam pelo mesmo código, então não há
versão da regra para fila e versão para HTTP.

**Commit primeiro, apagar depois.** Apagar antes perde a operação se o commit
falhar. Apagar depois pode entregar duas vezes, e é exatamente isso que o
registro de entrada absorve. A assimetria é a razão de ele existir.

| Desfecho | Mensagem |
|---|---|
| Aplicada, ou já tratada | apaga |
| Rejeição de negócio | apaga — é terminal, e reentregar não muda |
| Envelope ilegível | apaga, com log alto |
| Falha transitória | **devolve a visibilidade** para reentrega imediata |

A fila de descarte é do broker: `maxReceiveCount` e redrive ficam na fila, não
num contador nosso — que estaria em dois lugares e discordaria no primeiro
reinício. Ver `docs/adr/0009-inbox-and-queue.md`.

O `MessageGroupId` é a **carteira**, que é a mesma granularidade da coordenação
(ADR 0007). Agrupar por provedor serializaria todas as carteiras dele atrás de
uma só.

## O registro de saída

Banco e broker não compartilham transação. Publicar antes do commit anuncia um
fato que pode não ter acontecido; publicar depois, fora da transação, perde o
evento se o processo morrer no meio. Gravar o evento **na mesma transação** e
publicar depois é a única forma sem nenhuma das duas falhas.

**O evento nasce no domínio.** Tipo e versão são fixados pelo construtor — um
chamador que pudesse escolhê-los publicaria um payload v1 rotulado v2, e o
consumidor o leria errado sem nada aqui perceber.

**O payload é um retrato imutável.** Montá-lo na hora de publicar leria o estado
**atual**: um `WalletBalanceChanged` publicado três segundos depois anunciaria o
saldo de agora, e como o atraso varia, o mesmo evento diria coisas diferentes
dependendo de quando o worker acordou.

**O lock da linha é o lease.** O publisher reserva com `FOR UPDATE SKIP LOCKED`,
publica dentro da transação e marca no mesmo commit. Isso segura um lock durante
uma chamada de rede — e é o ponto: um publisher que trava ou morre libera as
linhas no instante em que o Postgres percebe, sem campo de expiração para
calibrar e sem relógio para sincronizar entre máquinas. O custo é contido por
lote pequeno e **timeout de publicação explícito**.

**`LOSS` produz `WagerTransactionProcessed` e não produz `WalletBalanceChanged`.**
É o caso que separa "a operação terminou" de "o dinheiro mudou".

Contrato completo em `docs/events.md`; decisões em `docs/adr/0010-outbox.md`.

## O que os testes provam

536 casos no total. `-race` limpo, inclusive nos cenários multi-processo.

Os cenários de concorrência rodam em **três processos independentes**, não em
goroutines. Goroutines compartilham pool e memória: provam que o código é seguro
para threads, não que a garantia está no banco — um bug que dependesse de lock
local passaria em todas elas. `make test-concurrency` compila o servidor, sobe
três instâncias contra o mesmo banco e dispara nas três.

Três guardas valem menção porque protegem invariante:

- **O hash tem teste de valor fixo.** Mudar campo, ordem ou separador quebra
  alto. Sem isso, a mudança invalidaria todo registro já gravado e o sintoma
  seria movimentação duplicada em produção, não teste vermelho.
- **O teste de reinício derruba o grafo inteiro** — pool, memória, tudo — sobe
  outro contra o mesmo banco e reenvia. É a única forma honesta de mostrar que a
  idempotência mora no banco.
- **Vinte duplicatas concorrentes**, soltas juntas, movem dinheiro uma vez.
- **Duas apostas de 80,00 sobre 100,00**, de processos diferentes: uma
  processada, uma rejeitada por saldo insuficiente, saldo 20,00, um único
  débito.
- **Cinquenta cópias idênticas** em três processos: um débito, versão 2.
- **Quarenta apostas distintas numa carteira**: todas passam. Removendo o
  `FOR UPDATE`, esse é o teste que falha — com 409, não com saldo errado.
- **Uma reversão que ultrapassa o que desfaz** é parqueada, a aposta chega, e o
  worker — possivelmente em outro processo — conclui.
- **Um `REFUND` e um `ROLLBACK` da mesma aposta**, soltos juntos em instâncias
  diferentes: um aplica, o outro recebe `ALREADY_REVERSED`, e os 25,00 voltam
  uma vez.
- **A mesma operação por HTTP e por fila** move dinheiro uma vez: o registro de
  entrada deixa passar (mensagem nova) e a idempotência barra (operação
  conhecida).
- **Dez entregas da mesma mensagem**, com ids de deduplicação distintos para a
  janela do broker não filtrar: um débito.
- **Um envelope ilegível não trava o consumidor** — numa fila FIFO, retentar
  bloquearia tudo atrás dele.
- **Trinta apostas com três publishers** disputando o mesmo registro de saída:
  cada evento sai exatamente uma vez.
- **Um broker que não estava lá** não impede a operação: o dinheiro se move, os
  eventos ficam guardados, e saem quando alguém volta a publicar.
- Toda cena termina conferindo saldo armazenado contra créditos menos débitos,
  em unidades mínimas: usar float na verificação faria ela depender do que está
  verificando.

O teste da composição sobe o grafo inteiro, atende uma requisição real de ponta
a ponta e encerra — grafo validado sem start não prova a ordem do encerramento,
que é justamente o que importa. Ele também confere as três formas de o start
falhar: banco inalcançável, configuração malformada e porta ocupada.

Cada constraint tem um teste que **a viola de propósito**, vários mandando SQL
direto, passando por fora do domínio. Regra que o domínio também impõe ainda
precisa valer no banco: a razão de existir a segunda cópia é justamente que a
primeira é código.

Dois merecem menção porque guardam invariantes em vez de comportamento:

- **`TestNoFloatingPointInTheDomain`** lê o código-fonte do próprio pacote com
  `go/parser` e falha se qualquer `float32`, `float64` ou literal de ponto
  flutuante aparecer. Usa a árvore sintática, não `grep`, porque os comentários
  legitimamente dizem "sem float" e um teste que precisa ser explicado toda vez
  que dispara deixa de ser lido.
- **`TestTheFloatScanActuallyLooks`** guarda a guarda: exige que a varredura
  encontre um número mínimo de declarações. Varredura que não lê nada reporta
  sucesso para sempre — que foi exatamente como os dois hooks mortos deste
  repositório falharam.

E um que enuncia a propriedade central: **`TestBalanceAlwaysMatchesTheLedger`**
percorre uma sequência de movimentações e confere o saldo final contra a soma de
créditos menos débitos.

## O que ainda não existe

Métricas e reconciliação. A ordem em que entram está no plano de ação, fora do
repositório.
