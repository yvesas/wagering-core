# 0007 — Consumo por fila, com registro de entrada

- **Status:** implementada · 487 casos · `-race` limpo
- **Requisitos cobertos:** `REQ-MSG-001..012` · `REQ-TST-002` · `REQ-TST-006`
- **Depende de:** features 0001 a 0006 (concluídas)

## O que foi construído

A segunda porta de entrada: um consumidor SQS com registro de entrada, e as
filas provisionadas pela aplicação no start-up.

Decisões em
[`docs/adr/0009-inbox-and-queue.md`](../../../docs/adr/0009-inbox-and-queue.md).

## O que o requisito forçou no desenho

"O registro da inbox e a conclusão durável do tratamento devem compartilhar a
transação SQL" não é detalhe de implementação: obrigou o caso de uso a ganhar
duas formas.

```go
Execute(ctx, cmd)              // abre a transação — HTTP
ExecuteIn(ctx, repos, cmd)     // roda na transação de quem chama — fila
```

As duas passam pelo mesmo código. Era o requisito, e é também a única forma de
não divergirem no primeiro conserto aplicado de um lado só.

## Uma coisa que o ambiente ensinou

`localstack/localstack:latest` virou a imagem licenciada. Uma build que o usasse
passaria a falhar com `License activation failed` num dia em que nada no
repositório mudou. A tag está fixada em `3.8`, e o `docker-compose.yml` diz por
quê — fixar tag não é pedantismo.

## Como se prova

```sh
make check
make up-test && make test-integration
make test-scenarios
```

Os cenários rodam com três processos e seis loops de consumidor na mesma fila:
um bug que deduplicasse em memória de processo passaria num teste de processo
único e falharia ali.
