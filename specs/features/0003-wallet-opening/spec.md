# 0003 — Abertura de carteira e HTTP

- **Status:** implementada · 341 casos no total · `-race` limpo
- **Requisitos cobertos:** `REQ-API-001..006` · `REQ-API-009` · `REQ-WAL-005` ·
  `REQ-TRX-008..009` · `REQ-TST-004`
- **Depende de:** features 0001 e 0002 (concluídas)

## O que vamos construir

O primeiro caminho de ponta a ponta: uma requisição HTTP entra, um caso de uso
orquestra o domínio, uma transação SQL confirma tudo junto, e uma resposta sai.
Mais a composição por injeção de dependência que sustenta o processo.

## O que fica de pé nesta fatia

```
POST /wallets                      abre carteira
GET  /wallets/{walletId}           lê carteira
GET  /wallets/{walletId}/ledger    extrato paginado por cursor opaco
GET  /health/live                  o processo está vivo
GET  /health/ready                 as dependências respondem
```

## Requisitos detalhados

### Caso de uso da abertura

| ID | Critério |
|---|---|
| A1 | Carteira, transação `OPENING` e lançamento de crédito num commit só. |
| A2 | Saldo inicial zero não cria `OPENING` nem lançamento; a carteira nasce na versão 1 mesmo assim. |
| A3 | Saldo inicial positivo também nasce na versão 1 — o crédito é parte da criação. |
| A4 | Segunda carteira para o mesmo jogador e moeda é conflito. |
| A5 | O caso de uso não gera identidade nem lê o relógio direto: ambos são portas. |
| A6 | Entrada externa passa por `ParseExternalMoney` — negativo é recusado na borda. |

### Composição e ciclo de vida

| ID | Critério |
|---|---|
| B1 | Fx compõe config, pool, repositórios, casos de uso, handlers e servidor. |
| B2 | Nem `domain` nem `app` importam Fx — verificado por `make app-check`. Ver ADR 0004. |
| B3 | O start valida: o pool dá ping e a configuração falha nomeando o que falta. |
| B4 | O shutdown drena requisições em voo dentro de `APP_SHUTDOWN_TIMEOUT` e desiste quando esgota. |
| B5 | O pool fecha **depois** do servidor parar. |
| B6 | Existe teste que sobe o grafo inteiro, encerra e confere a liberação. |

### HTTP

| ID | Critério |
|---|---|
| C1 | `net/http` puro, padrões com método e curinga. Ver ADR 0005. |
| C2 | Dinheiro entra e sai como `{"amount":"25.00","currency":"BRL"}` — string, nunca número JSON. |
| C3 | Corpo malformado, moeda inválida, escala excedente e valor negativo → 400 com código estável. |
| C4 | Carteira duplicada → 409. Carteira inexistente → 404. |
| C5 | O mapa de erro → status é uma tabela única, testada caso a caso. |
| C6 | O cursor do extrato é opaco: o cliente devolve o que recebeu e nada mais. |
| C7 | `/health/live` responde sem tocar em dependência; `/health/ready` reporta o banco. |
| C8 | Log em JSON com `correlationId`, sem credencial e sem payload financeiro completo. |

## Áreas cinzentas — resolvidas

**Identidade → UUIDv7 via `google/uuid`.** Ordena por tempo; escrever à mão é o
tipo de trinta linhas cujo erro aparece tarde, como chave duplicada sob carga.

**Abertura → sem `PENDING` intermediário.** Não depende de nada e é confirmada
junto da carteira: não há janela entre aceitar e aplicar.

**Descoberto no caminho:** `platform` montar `postgres` criou ciclo de import,
porque `postgres.NewPool` usava o tipo de config do `platform`. O adaptador
passou a ter o próprio — adaptador que importa quem o monta não pode ser montado
por mais ninguém.

## Como se prova que está pronto

```sh
make check
make up-test && make test-integration
```

O teste da composição não pode ser um `fx.New` que só valida o grafo: ele sobe,
atende uma requisição e encerra. Grafo validado sem start é a mesma família de
guarda que já nos custou três vezes neste repositório.
