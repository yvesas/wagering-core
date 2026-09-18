# Arquitetura — como o sistema é hoje

> Este documento descreve o que **existe**, não o que está planejado. O desenho
> pretendido vive em `specs/project/PROJECT.md` e no `spec.md` de cada feature.
>
> **Estado atual:** domínio, persistência, o primeiro caso de uso e a borda
> HTTP, compostos por Fx. Não há fila, autenticação nem operações de aposta.

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
└── errors.go               as falhas que um adaptador pode reportar

internal/adapter/postgres/  a implementação
├── uow.go                  UnitOfWork, Repositories, Queries
├── wallet.go · ledger.go · transaction.go   SQL explícito
├── errors.go               tradução de SQLSTATE na borda
├── pool.go · migrate.go    pool e migrations embarcadas

internal/adapter/http/      a borda de entrada
├── server.go               rotas, middleware, timeouts
├── wallet.go               handlers e DTOs
├── health.go               liveness e readiness
└── errors.go               erro → status, tabela exaustiva

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

## O que os testes provam

341 casos no total: unidade no domínio, no caso de uso e nos handlers, mais
integração contra PostgreSQL de verdade. `-race` limpo.

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

Operações de aposta, idempotência, concorrência, fila, outbox, autenticação,
métricas e reconciliação. A ordem em que entram está no plano de ação, fora do
repositório.
