# 0002 — Persistência e schema

- **Status:** implementada · 26 casos de integração contra PostgreSQL real · `-race` limpo
- **Requisitos cobertos:** `REQ-PER-001..005` · `REQ-WAL-002` · `REQ-WAL-005` ·
  `REQ-WAL-008` (a metade que é do banco) · `REQ-LED-004..005` · `REQ-TRX-009` ·
  `REQ-TST-002..003`
- **Depende de:** feature 0001 (concluída)

## O que vamos construir

O schema, as migrations e a camada que grava e lê o domínio no PostgreSQL —
com as invariantes impostas **pelo banco**, não só pelo código.

## O princípio que ordena esta fatia

**Invariante se garante duas vezes.** No domínio, porque é lá que ela é
conhecida e testável em memória. E no banco, por constraint, porque é lá que ela
sobrevive a um bug de aplicação, a uma instância mal comportada e a um `UPDATE`
feito à mão numa madrugada.

Constraint de banco não é redundância: é a última linha. O domínio erra.

## O que vai para o banco

| Invariante | Como o banco impõe |
|---|---|
| Uma carteira por jogador e moeda | `UNIQUE (player_id, currency)` |
| Saldo nunca negativo | `CHECK (balance_minor >= 0)` |
| Versão começa em 1 | `CHECK (version >= 1)` |
| Um lançamento por transação e carteira | `UNIQUE (wallet_id, transaction_id)` |
| Valor de lançamento é positivo | `CHECK (amount_minor > 0)` |
| `saldoDepois = saldoAntes ± valor` | `CHECK` por direção |
| Ledger é append-only | trigger que recusa `UPDATE` e `DELETE`, mais `REVOKE` |
| Operação de negócio é única | `UNIQUE (provider_id, external_id)` |
| Chave de idempotência é única por provedor | `UNIQUE (provider_id, idempotency_key)` |
| Crédito inicial não duplica | índice parcial único de `OPENING` por carteira |
| `LOSS` vale zero, o resto vale mais que zero | `CHECK` por tipo |
| Reversão tem referência, o resto não | `CHECK` por tipo |
| Origem interna não carrega metadado externo | `CHECK` por origem |

## Requisitos detalhados

### Schema e migrations

| ID | Critério |
|---|---|
| A1 | Migrations versionadas e ordenadas, com `up` e `down`, aplicadas por comando documentado. |
| A2 | Toda constraint da tabela acima existe e é exercitada por um teste que a viola de propósito. |
| A3 | Dinheiro é `BIGINT` de unidades mínimas mais a moeda em coluna própria. Nunca `FLOAT`, nunca `NUMERIC` implícito. |
| A4 | Timestamps são `TIMESTAMPTZ`. |
| A5 | O ledger tem ordenação estável para paginação por cursor opaco. |

### Fronteira transacional

| ID | Critério |
|---|---|
| B1 | `app` declara `UnitOfWork`, `Repositories` e as portas de repositório; o adaptador as implementa. Ver ADR 0003. |
| B2 | Erro do callback faz rollback, `nil` faz commit, pânico faz rollback e repropaga. |
| B3 | `Do` aninhado devolve erro, não abre savepoint. |
| B4 | Saldo e lançamento são confirmados no mesmo commit, provado por um teste que falha no meio e confere que nada sobrou. |
| B5 | Erro de driver vira erro tipado de `app` na borda; nenhum `*pgconn.PgError` sobe. |
| B6 | `internal/app` não importa driver, HTTP nem SDK de fila — verificado por `make app-check`. |

### Repositórios

| ID | Critério |
|---|---|
| C1 | SQL explícito, escrito à mão. Sem ORM, sem construtor de query. |
| C2 | `Money` vai e volta exato: `Minor()` para `BIGINT`, `NewMoneyFromMinor` na volta. |
| C3 | Reidratação usa os construtores de reidratação do domínio, que validam — linha corrompida para na borda. |
| C4 | `UpdateBalance` é condicionado à versão esperada e reporta quando não casa nenhuma linha. |
| C5 | Violação de unicidade é distinguível de erro genérico. |

## Áreas cinzentas — resolvidas

**Migration → goose como biblioteca, SQL embarcado por `go:embed`.** O binário
carrega o próprio schema, e o advisory lock do goose torna seguro toda réplica
chamar no start-up.

**Coluna de id → `TEXT`.** `UUID` rejeitaria `transaction-123`, que é entrada
legítima de provedor.

**Imutabilidade → trigger e `REVOKE`, e são dois triggers.** `REVOKE` não alcança
o dono da tabela e migration roda como dono. `TRUNCATE` não dispara trigger de
linha, então o de statement fecha a porta que sobrava.

**Descoberto no caminho:** com o ledger imutável, teste de integração não limpa
o que criou — `DELETE` e `TRUNCATE` são recusados. Os testes usam identificadores
únicos por execução em vez de desfazer.

## Como se prova que está pronto

```sh
make up-test              # PostgreSQL isolado, em tmpfs
make test-integration     # migrations, constraints, atomicidade
make check                # gate completo
```

Cada constraint tem um teste que **a viola de propósito** e espera a recusa.
Constraint sem esse teste não conta como existente: já vimos neste repositório o
que acontece com uma guarda que ninguém exercita.
