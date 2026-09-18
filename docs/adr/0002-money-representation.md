# ADR 0002 — Dinheiro em `int64` de unidades mínimas

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-MON-002, REQ-MON-003, REQ-MON-009, REQ-MON-011
- **Decide:** F1.1

## Contexto

`Money` é o tipo mais usado do sistema e o mais fácil de errar. A exigência é
absoluta: **ponto flutuante não pode aparecer em etapa nenhuma** — nem em
parsing, cálculo, serialização, persistência ou teste. `0.1 + 0.2 != 0.3` em
binário, e num ledger append-only esse centavo não se conserta: ele vira um
lançamento de correção, auditável para sempre.

O contrato externo fixa a forma: `{"amount":"25.00","currency":"BRL"}` — string
decimal, escala de duas casas, moeda ISO 4217.

Além disso, `internal/domain/` não pode importar infraestrutura
(ADR 0001), e uma biblioteca de terceiros dentro do domínio é exatamente o tipo
de dependência que a golden rule existe para barrar.

## Opções

**1. `int64` em unidades mínimas.** O valor é o número de centavos. Sem
dependência, sem alocação, semântica de valor (cópia é cópia), comparável com
`==`. Custo: overflow vira responsabilidade explícita, e a escala fica fixa no
tipo.

**2. Biblioteca decimal** (`shopspring/decimal` e similares). Resolve escala e
overflow sozinha, com API rica. Custo: uma dependência de terceiro no pacote que
mais queremos manter limpo, valores em heap, e igualdade que não é `==`.

**3. `math/big.Int` ou `big.Rat`.** Stdlib, então não fere a golden rule, e
precisão ilimitada. Custo: ponteiro interno — dois `Money` podem compartilhar o
mesmo `big.Int` e a imutabilidade passa a depender de disciplina, não do tipo;
alocação em toda operação; e `==` deixa de funcionar, o que abre espaço para o
bug mais silencioso possível numa comparação de saldo.

## Decisão

**`int64` em unidades mínimas, escala fixa de duas casas, moeda ISO 4217 em
código de três letras maiúsculas.**

```go
type Money struct {
    minor    int64    // centavos; 2500 é "25.00"
    currency Currency
}
```

Campos não exportados: não existe caminho, fora deste pacote, que produza um
`Money` inválido. `Currency` também encapsula sua string, então `Money{}` é
detectável como não inicializado e é rejeitado.

## Motivo

**A opção 3 perde pela imutabilidade.** `Money` é um value object, e a coisa que
mais importa nele é que passar adiante não deixa ninguém alterar o seu. Com
`big.Int` isso vira convenção — basta um `Set` num ponteiro compartilhado. Com
`int64` é o compilador que garante: cópia de struct é cópia de verdade.

**A opção 2 perde pela golden rule.** Trazer terceiro para dentro do domínio
para resolver um problema que `int64` resolve com quarenta linhas de aritmética
checada é pagar caro na moeda errada. E essas quarenta linhas são exatamente o
exercício que este projeto existe para fazer.

**O alcance do `int64` não é limitação prática.** ±9.223.372.036.854.775.807
centavos são ±92 quatrilhões de reais. O saldo de uma carteira de jogador não
chega perto, e o `int64` ainda pega o caso patológico antes do estouro, em vez
de saturar em silêncio.

**Comparação vira `==`.** Dois `Money` da mesma moeda e mesmo valor são iguais
por identidade estrutural do Go. Isso elimina uma classe inteira de teste falso
positivo — aquele que compara ponteiros e passa por acidente.

## Consequências

**Overflow é responsabilidade explícita, em quatro pontos.** Parsing, soma,
subtração e negação. Cada um checa antes de operar e devolve erro tipado; nenhum
deles pode confiar no wraparound do Go, que é silencioso. `Neg` tem o caso
especial de `math.MinInt64`, cujo simétrico não existe em `int64` — e é o único
lugar onde a aritmética é assimétrica.

**A escala é do tipo, não da moeda.** Duas casas valem para todas. Isso exclui
JPY (zero casas) e KWD (três), e é aceito porque os fluxos operam em BRL. O tipo
carrega a moeda e há teste de incompatibilidade, então o dia em que uma moeda de
escala diferente entrar, o que muda é o fator de escala — não o desenho.

**Parsing tem duas portas.** `ParseMoney` aceita sinal negativo, porque
diferença e cálculo interno são legitimamente negativos — a reconciliação
devolve `difference` negativo quando o saldo armazenado é menor que o
reconstruído. `ParseExternalMoney` é `ParseMoney` mais a regra de que entrada
financeira externa nunca é negativa. A borda usa a segunda; a reidratação a
partir do banco usa `NewMoneyFromMinor`, que aceita o que o banco guardou.

**Normalização é por `String()`, e é documentada.** `"25"`, `"25.0"` e `"025.00"`
são aceitos e produzem o mesmo `minor`. A forma canônica — a que entra no hash
de idempotência — é sempre a saída de `String()`: sinal quando negativo, ao menos
um dígito inteiro, ponto, exatamente duas casas. Entrada com escala excedente
(`"25.000"`) é **rejeitada**, nunca arredondada.

**A persistência guarda `BIGINT` de unidades mínimas**, mais a moeda em coluna
própria. É conversão exata nos dois sentidos, sem `NUMERIC` e sem risco de o
driver devolver `float64` no meio do caminho.

**Uma varredura de teste barra `float32`/`float64`** em qualquer arquivo do
pacote de domínio. É a invariante mais fácil de violar por descuido e a mais
cara de descobrir tarde, então ela não depende de revisão humana.
