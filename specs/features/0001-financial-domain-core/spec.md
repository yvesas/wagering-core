# 0001 — Núcleo de domínio financeiro

- **Status:** especificada, não iniciada
- **Requisitos cobertos:** `REQ-MON-001..011` · `REQ-WAL-001..008` ·
  `REQ-LED-001..003` · `REQ-LED-006` · `REQ-TRX-001..011` (modelagem) ·
  `REQ-TST-001`
- **Depende de:** F0.7 (módulo e layout de pacotes) — concluído

## O que vamos construir

O núcleo financeiro em Go puro: valor monetário, carteira, lançamento de ledger
e transação, com suas invariantes e sua máquina de estados. Sem banco, sem HTTP,
sem fila, sem Fx.

## Por que primeiro

As decisões difíceis deste sistema são todas daqui: como dinheiro é
representado, o que a carteira garante, o que torna um lançamento válido, quais
transições existem. Cada uma delas, descoberta tarde, obriga a reescrever o
schema, os repositórios e os casos de uso que vieram depois.

E há um ganho de método: enquanto não existe I/O, todo teste roda em
milissegundos. As invariantes ficam baratas de exercitar a fundo, e por isso
ficam exercitadas a fundo.

## Fora do escopo desta feature

Persistência, mapeamento para tabela, casos de uso, portas, eventos e resolução
de referência entre operações. A transação será **modelada** aqui — tipos,
campos por origem, estados e transições — mas quem a executa contra um banco é a
feature 0002.

## Requisitos detalhados

### Valor monetário

| ID | Critério |
|---|---|
| A1 | Criação a partir de string decimal com escala fixa de duas casas e moeda ISO 4217. |
| A2 | Zero por moeda, soma, subtração, negação, comparação e serialização de volta para string decimal. |
| A3 | Rejeitar: vazio, `NaN`, `Infinity`, notação científica, escala excedente, moeda inválida e negativo em entrada externa. |
| A4 | Nenhum arredondamento silencioso. Entrada inválida é erro, nunca valor corrigido. |
| A5 | Toda aritmética e comparação entre moedas diferentes é erro. |
| A6 | Overflow detectado em parsing, soma, subtração e negação. |
| A7 | Nenhum `float32`/`float64` em qualquer caminho — inclusive nos testes. |
| A8 | Valor negativo é representável para diferença e cálculo interno. |

### Lançamento de ledger

| ID | Critério |
|---|---|
| B1 | Campos: identidade, carteira, transação, direção, valor, saldo antes, saldo depois, instante de criação. |
| B2 | A construção valida `saldoDepois = saldoAntes ± valor` conforme a direção; violação é erro. |
| B3 | Nenhum método permite alterar um lançamento construído. |
| B4 | A moeda do valor, do saldo antes e do saldo depois é a mesma. |

### Carteira

| ID | Critério |
|---|---|
| C1 | Campos: identidade, jogador, moeda, saldo, versão, criação e atualização. |
| C2 | Construtor valida tudo; não existe caminho que produza carteira inválida. |
| C3 | Débito que deixaria o saldo negativo é erro de domínio, e o estado não muda. |
| C4 | Movimentação em moeda diferente da carteira é erro. |
| C5 | Débito e crédito produzem o lançamento correspondente e o novo saldo juntos, num resultado só. |
| C6 | Versão inicial `1`; incrementa apenas quando o saldo muda. |
| C7 | Reidratação é função separada da criação e não reaplica movimentação nem transição. |

### Transação

| ID | Critério |
|---|---|
| D1 | Os seis tipos, com a política de valor de cada um: abertura, aposta, ganho e as duas reversões exigem valor maior que zero; a perda exige exatamente zero. |
| D2 | Campos distintos por origem: a abertura é interna e não carrega provedor, identificador externo, chave, hash, rodada, jogo nem referência. |
| D3 | A abertura é rejeitada quando declarada como vinda de fora. |
| D4 | Os cinco estados, com os três terminais identificados pelo tipo. |
| D5 | Transições válidas explícitas; qualquer outra é erro. Transação terminal não transiciona. |
| D6 | Reversão exige referência externa; os demais tipos a rejeitam quando não se aplica. |

### Erros

| ID | Critério |
|---|---|
| E1 | Erros de domínio classificáveis por `errors.Is`/`errors.As`. |
| E2 | Nenhum `panic` representa rejeição de negócio. |
| E3 | Cada rejeição carrega um código estável, que será o `failureCode` exposto depois. |

## Áreas cinzentas — decidir antes de codificar

**Representação monetária: `int64` em unidades mínimas ou biblioteca decimal?**
`int64` não traz dependência e é rápido, mas exige tratar overflow à mão e
amarra a escala. Uma biblioteca decimal resolve escala e overflow, e custa uma
dependência dentro do domínio — que é justamente onde não queremos dependências.
Decidir em **F1.1**, com ADR, incluindo o limite numérico documentado.

**O resultado de débito e crédito é uma nova carteira ou mutação no lugar?**
Retornar `(novaCarteira, lançamento, erro)` mantém o valor imutável e torna o
teste trivial, ao custo de mais alocação. Mutar com ponteiro é idiomático em Go
e mais barato, mas abre espaço para estado meio-alterado quando o erro aparece
no meio. Decidir em **F1.7**.

**A transação é um tipo só com campos opcionais, ou tipos separados por origem?**
Um tipo com campos nulos é simples e espelha a tabela; tipos separados tornam
impossível construir uma abertura com identificador de provedor. Decidir em
**F1.8**.

## Como se prova que está pronto

```sh
make check    # fmt + vet + test + race
go list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'
```

O gate passa e o segundo comando não devolve nada.

Além disso, cada critério acima tem pelo menos um teste que **falharia** sem a
implementação correspondente. Teste que passa nas duas versões não prova nada e
não conta.

Um teste específico não pode faltar: uma varredura garantindo que nenhum arquivo
do pacote de domínio contém `float32` ou `float64`. É a invariante mais fácil de
violar por descuido e a mais cara de descobrir tarde.

## Ao fechar

- ADRs de **F1.1** e das demais áreas cinzentas escritos em `docs/adr/`.
- `docs/glossary.md` revisado se algum nome mudou na implementação.
- `docs/architecture.md` criado — passa a existir sistema para descrever.
- `TASKS.md` e `STATE.md`, na pasta de controle, atualizados.
