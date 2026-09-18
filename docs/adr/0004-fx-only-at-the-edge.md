# ADR 0004 — Fx só na borda, e a ordem do encerramento

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-TST-004, REQ-MSG-011, REQ-API-006
- **Decide:** F3.1, F3.2

## Contexto

Uber Fx compõe a aplicação: configuração, pool, repositórios, casos de uso,
handlers e, nas fases 7 e 8, os workers. Ele também governa o ciclo de vida —
subir validando dependências, encerrar drenando o que está em andamento.

A pergunta é **até onde ele entra**. Um container de injeção de dependência é
sedutor: assim que existe, é tentador resolver tudo com ele, inclusive coisas
que não são composição.

## Decisão

**Fx vive em `internal/platform` e em `cmd/`. Mais nada o conhece.**

- `internal/domain` não o importa — já é a golden rule (ADR 0001).
- `internal/app` não o importa, e `make app-check` falha se importar.
- `internal/adapter` expõe construtores comuns (`NewUnitOfWork`, `NewServer`);
  quem os registra num grafo é a camada de composição.

O teste é o mesmo de sempre, e é mecânico:

```sh
go list -deps ./internal/app/... | grep go.uber.org/fx
```

Saída vazia, ou a regra foi quebrada.

## Motivo

**Um caso de uso que recebe `*fx.App` não é mais testável sem um container.**
A tentação concreta é usar `fx.In` em structs de dependência, porque poupa
escrever um construtor. O preço aparece no teste: instanciar o caso de uso passa
a exigir um grafo, e um teste que precisa de container para exercitar uma regra
de negócio deixa de ser barato — e teste caro é teste que não se escreve.

**Construtor comum é a interface com o Fx.** `func NewOpenWallet(uow app.UnitOfWork,
ids app.IDGenerator, clock app.Clock) *OpenWallet` funciona igualmente bem com
`fx.Provide` e com três linhas num teste. O container não precisa ser mencionado
para ser usado.

## A ordem do encerramento

`fx.Lifecycle` roda os `OnStop` **na ordem inversa** dos `OnStart`. Isso não é
detalhe de implementação: é a única coisa que garante que o pool feche **depois**
do servidor parar.

Se o pool fechasse antes, cada requisição ainda em voo — exatamente as que o
drain existe para terminar — falharia com "conexão fechada" no último
milissegundo. O drain teria acontecido, e teria sido inútil.

Daí a regra de registro: **quem é usado é construído primeiro.** O pool antes do
servidor, o servidor antes dos workers. A ordem de `fx.Provide` não importa — o
Fx resolve por dependência —, mas a ordem em que os `OnStart` são registrados
importa, e ela vem da ordem em que os componentes são construídos.

## Consequências

**O start valida, não só constrói.** O pool dá ping antes de a aplicação subir, e
a configuração falha com o que está faltando. Uma aplicação que sobe "com
sucesso" e falha na primeira requisição move um erro de configuração de onde ele
é óbvio para onde parece bug de requisição.

**O shutdown tem prazo, e o prazo é configurável.** `APP_SHUTDOWN_TIMEOUT`
governa quanto tempo o servidor espera as requisições em voo. Esgotado o prazo,
ele desiste — drain sem limite é um processo que nunca morre, e um processo que
não morre é um deploy que não termina.

**Há um teste da composição.** Ele sobe o grafo inteiro, encerra e confere que os
recursos foram liberados. Um grafo que só é exercitado em produção é um grafo
que quebra em produção: dependência faltando é erro de runtime no Fx, não de
compilação.
