# PROJECT — wagering-core

## O que é

Um serviço distribuído de **carteira e ledger financeiro** para operações de
jogo. Provedores enviam apostas, ganhos, devoluções e desfazimentos por API HTTP
ou por fila; o serviço movimenta a carteira do jogador e registra cada
movimentação num ledger imutável.

Projeto de estudo pessoal. O que se estuda aqui não é CRUD: é o que acontece com
dinheiro quando há concorrência, entrega repetida e processos morrendo no meio.

## Golden rule

**O domínio não conhece infraestrutura.**

Teste operacional, verificável e não opinativo:

```sh
go list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'
```

Saída vazia, ou a regra foi quebrada. `internal/domain/` importa apenas a
biblioteca padrão e, se houver, a biblioteca decimal.

Por que esta e não outra: toda garantia deste sistema é uma invariante de
domínio — saldo não negativo, ledger imutável, transição de estado válida.
Invariante que mora dentro de um handler HTTP ou de um repositório existe uma
vez por camada e some na segunda porta de entrada. E este sistema tem duas
portas de entrada, HTTP e fila, que precisam produzir **o mesmo resultado**.

## Princípios

1. **Invariante se garante duas vezes:** no domínio, porque é lá que ela é
   conhecida; e no banco, por constraint, porque é lá que ela sobrevive a um bug
   de aplicação e a uma instância mal comportada.
2. **Estado encapsulado.** Construtor valida, método explícito faz a transição.
   Não existe campo público que permita um objeto inválido.
3. **Criação e reidratação são caminhos distintos.** Reidratar não reaplica
   movimentação, não dispara transição e não emite evento.
4. **A borda normaliza, o núcleo confia.** Payload externo vira tipo de domínio
   numa camada só; o resto do código não reparseia string.
5. **Dependência externa atrás de uma porta (interface).** O caso de uso conhece
   a porta, nunca o driver.
6. **Erro é valor tipado.** Classificável por `errors.Is`/`errors.As`. `panic`
   não representa rejeição de negócio, e nenhum erro é engolido.
7. **`context.Context` em toda operação de I/O**, respeitando cancelamento e
   prazo.

## Stack

| Responsabilidade | Escolha |
|---|---|
| Linguagem | Go |
| Composição e ciclo de vida | Uber Fx (`fx.Module`, `fx.Provide`, `fx.Invoke`, `fx.Lifecycle`) |
| HTTP | `net/http` ou roteador leve — decidir em F3.6 |
| Persistência | PostgreSQL com `pgx` e SQL explícito |
| Migrations | Versionadas, com reversão documentada — ferramenta a decidir em F2.2 |
| Mensageria | Fila gerenciada, emulada localmente para desenvolvimento *(fase 7)* |
| Identidade | Provedor OIDC externo, provisionado no compose *(fase 9)* |
| Ambiente local | Docker Compose |
| Testes | `testing` e `go test`, incluindo `-race` |

## Arquitetura

Hexagonal. O domínio no centro, adaptadores na borda, casos de uso no meio
definindo as portas.

| Pasta | Responsabilidade |
|---|---|
| `cmd/` | Binários. Cada um monta seu grafo Fx e mais nada. |
| `internal/domain/` | Entidades, value objects, invariantes e erros de domínio. **Sem import de infraestrutura.** |
| `internal/app/` | Casos de uso e as portas que eles exigem (repositórios, unit of work, relógio). |
| `internal/adapter/` | Implementações das portas: HTTP, PostgreSQL e, depois, fila. |
| `internal/platform/` | Config, log, métricas e módulos Fx compartilhados. |
| `migrations/` | SQL versionado. |
| `test/` | Integração, concorrência e recuperação, com containers reais. |

A dependência aponta sempre para dentro: `adapter → app → domain`. Nunca o
contrário.

## Escopo

**Nesta rodada** (fases 1–5): núcleo de domínio, persistência com constraints,
abertura de carteira e HTTP, idempotência persistente, concorrência por carteira.

**Depois** (fases 6–10): operações e reversões completas, consumo por fila com
registro de entrada, publicação por registro de saída, autenticação e isolamento
entre provedores, observabilidade e reconciliação.

**Fora de escopo:** reversão parcial, ledger de partidas dobradas, tracing
distribuído, teste de carga, cadastro de usuário e emissão própria de token.

## Onde está o resto

| O quê | Onde |
|---|---|
| Requisitos com ID | `specs/project/REQUIREMENTS.md` |
| O que vamos construir nesta fatia | `specs/features/NNNN-slug/spec.md` |
| Como o sistema é hoje | `docs/` |
| Decisões estruturais, permanentes | `docs/adr/` |
| Plano, ordem das tarefas e andamento | pasta de controle, fora do repositório |
