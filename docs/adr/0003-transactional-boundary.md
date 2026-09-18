# ADR 0003 — Fronteira transacional atrás de uma porta

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-PER-002, REQ-PER-004, REQ-PER-005, REQ-WAL-005, REQ-MSG-006
- **Decide:** F2.1

## Contexto

Várias coisas precisam ser confirmadas **no mesmo commit**: o saldo da carteira,
o lançamento do ledger, o estado da transação e — nas fases 7 e 8 — o registro
de entrada da fila e os eventos de saída. Se qualquer par delas puder divergir,
o sistema está errado de um jeito que nenhum teste de caminho feliz pega.

Isso levanta a pergunta que molda a camada inteira: **quem sabe que existe uma
transação SQL?**

O domínio não pode saber (ADR 0001). Mas o caso de uso precisa declarar que
duas escritas são atômicas, e os repositórios precisam executar na mesma
conexão. Alguém tem de carregar esse contexto.

## Opções

**1. `pgx.Tx` no caso de uso.** O caso de uso abre a transação e passa a `Tx`
para cada repositório. Direto e explícito. Custo: `internal/app` passa a
importar `pgx`, e o caso de uso — que é regra de aplicação — fica escrito na
linguagem de um driver. Trocar de driver vira reescrita de casos de uso.

**2. Transação carregada no `context.Context`.** Os repositórios procuram uma
`Tx` no contexto e usam o pool quando não acham. Não polui assinatura nenhuma e
parece elegante.

**3. Unit of Work atrás de uma porta.** O caso de uso pede "faça isto
atomicamente" e recebe, dentro de um callback, os repositórios já ligados
àquela transação. Quem sabe o que é um commit é o adaptador.

## Decisão

**Opção 3.** `internal/app` declara a porta; `internal/adapter/postgres` a
implementa com `pgx`.

```go
// internal/app/ports.go
type Repositories interface {
    Wallets() WalletRepository
    Ledger() LedgerRepository
    Transactions() TransactionRepository
}

type UnitOfWork interface {
    Do(ctx context.Context, fn func(context.Context, Repositories) error) error
}
```

O caso de uso escreve:

```go
err := uow.Do(ctx, func(ctx context.Context, repos app.Repositories) error {
    if err := repos.Wallets().UpdateBalance(ctx, moved.Wallet, previousVersion); err != nil {
        return err
    }
    return repos.Ledger().Append(ctx, moved.Entry)
})
```

Erro devolvido pelo callback faz rollback; `nil` faz commit. Pânico faz rollback
e é repropagado.

## Motivo

**A opção 2 é a que recusamos com mais convicção, e é a mais sedutora.** O
fallback silencioso é o problema: um repositório que não acha a transação no
contexto e usa o pool **escreve fora da transação e não falha**. O saldo é
gravado, o lançamento é gravado por outra conexão, o rollback desfaz um e não o
outro, e nada no código aponta para o bug. É a mesma classe de falha dos hooks
que retornavam zero antes de checar qualquer coisa — a guarda existe, parece
funcionar, e não roda.

Dá para fechar esse buraco fazendo o repositório **exigir** a transação no
contexto e falhar quando não acha. Só que aí o contexto virou um parâmetro
obrigatório disfarçado de opcional, com o compilador sem poder ajudar: esquecer
de propagar o `ctx` certo continua compilando. O callback resolve o mesmo
problema com o compilador do lado certo — os repositórios **só existem** dentro
dele.

**A opção 1 não é errada, é cara no lugar errado.** Ela acerta em ser explícita,
e o custo aparece em quantos arquivos precisam saber o nome do driver. O caso de
uso descreve política de aplicação; `pgx` é detalhe de como ela é gravada.

**O callback também elimina o vazamento de transação.** Não existe caminho em
que alguém esqueça o `Commit` ou o `Rollback`, porque nenhum dos dois é chamado
por quem escreve o caso de uso. O `defer` do adaptador cobre o retorno normal, o
erro e o pânico.

## Consequências

**Os repositórios não são construídos soltos.** Eles nascem ligados a uma
transação, dentro do `Do`. Não há um `WalletRepository` "global" que alguém possa
usar por engano fora de uma transação — e isso é proposital, porque leitura
consistente com escrita também precisa da mesma transação.

**Leitura fora de transação existe e é explícita.** Consulta de carteira,
extrato paginado e consulta de transação não precisam de atomicidade. Elas vão
numa porta própria (`app.Queries`), e o dia em que uma delas precisar enxergar o
que outra escreveu, ela migra para dentro do `Do` — que é uma mudança visível,
não um efeito colateral.

**Aninhar `Do` é erro, não reentrância.** Um `Do` dentro de outro devolve erro em
vez de abrir savepoint. Savepoint aninhado escondido é como uma "transação" passa
a não significar mais nada — o de fora comita e o de dentro já tinha desfeito
metade. Quando um caso de uso precisar mesmo de savepoint, ele pede por nome.

**O retry de conflito de concorrência mora aqui.** A fase 5 vai precisar repetir
a transação quando o Postgres devolver falha de serialização ou o `UPDATE`
condicionado por versão não casar nenhuma linha. O `Do` é o único lugar que vê a
transação inteira, então é o único que pode repeti-la — e o callback precisa ser
idempotente por construção, o que este desenho já obriga, já que ele não guarda
estado entre tentativas.

**Erro de driver é traduzido na borda.** Violação de unicidade, deadlock e falha
de serialização viram erros tipados de `app` antes de sair do adaptador. Nenhum
código acima da borda inspeciona código de erro do Postgres, e nenhum
`*pgconn.PgError` sobe.

**A regra ganha verificação.** `internal/app` não pode importar driver, HTTP nem
SDK de fila, do mesmo jeito que o domínio — a diferença é que ele pode importar
o domínio. `make app-check` falha se isso for violado, porque regra que não roda
sozinha vira recomendação.
