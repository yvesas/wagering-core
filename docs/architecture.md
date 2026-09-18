# Arquitetura — como o sistema é hoje

> Este documento descreve o que **existe**, não o que está planejado. O desenho
> pretendido vive em `specs/project/PROJECT.md` e no `spec.md` de cada feature.
>
> **Estado atual:** só o núcleo de domínio existe. Não há banco, HTTP, fila,
> autenticação nem composição por injeção de dependência.

## O que existe

Um pacote: `internal/domain`. Ele importa apenas a biblioteca padrão —
`encoding/json`, `math`, `strconv`, `time` e `fmt` — e nada mais. A regra é
verificada por `make domain-check`, que faz parte de `make check` e do hook do
`Stop`.

```
internal/domain/
├── doc.go           responsabilidade do pacote e o que ele não pode importar
├── errors.go        Code, Error e as sentinelas
├── identifier.go    os identificadores opacos
├── money.go         Currency e Money
├── ledger.go        Direction e LedgerEntry
├── wallet.go        Wallet, Movement e Opening
└── transaction.go   Kind, Origin, Status e WagerTransaction
```

`cmd/`, `internal/app`, `internal/adapter`, `internal/platform`, `migrations/` e
`test/` existem como estrutura, com `doc.go` declarando a responsabilidade de
cada um, mas sem implementação.

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

## O que os testes provam

231 casos, `-race` limpo, 96% de cobertura do pacote.

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

Persistência, migrations, constraints, casos de uso, portas, HTTP, idempotência,
concorrência, fila, outbox, autenticação e observabilidade. A ordem em que
entram está no plano de ação, fora do repositório.
