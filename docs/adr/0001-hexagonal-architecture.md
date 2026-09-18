# ADR 0001 — Arquitetura hexagonal com domínio independente de framework

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-MSG-003, REQ-TRX-011, REQ-CON-003

## Contexto

O serviço tem **duas portas de entrada** — API HTTP e consumo de fila — que
precisam produzir exatamente o mesmo resultado financeiro para a mesma operação.
Além disso, as regras que definem se o sistema está certo são todas invariantes:
saldo nunca negativo, ledger imutável, transição de estado válida, valor
monetário sem ponto flutuante.

Havia três caminhos plausíveis:

1. **Lógica nos handlers.** Cada porta de entrada valida e movimenta.
2. **Lógica nos repositórios.** O acesso a dados guarda as regras.
3. **Domínio no centro**, com casos de uso definindo portas e adaptadores nas
   bordas.

## Decisão

Arquitetura hexagonal. A dependência aponta sempre para dentro:
`adapter → app → domain`.

- `internal/domain/` importa a biblioteca padrão e, se houver, a biblioteca
  decimal. Nada mais.
- `internal/app/` contém os casos de uso e **declara as interfaces que consome**
  — repositórios, unit of work, relógio.
- `internal/adapter/` implementa essas interfaces: HTTP, PostgreSQL, fila.
- `internal/platform/` reúne config, log, métricas e os módulos Fx.

A regra é verificável, não opinativa:

```sh
go list -deps ./internal/domain/... | grep -E 'go.uber.org/fx|jackc/pgx|net/http|aws-sdk-go'
```

Saída vazia, ou a regra foi quebrada.

## Motivo

**A opção 1 duplica a regra por porta de entrada.** Com duas portas, toda
invariante existiria duas vezes e divergiria na primeira correção feita só de um
lado. O requisito de que HTTP e fila compartilhem o caso de uso torna isso um
defeito imediato, não um risco futuro.

**A opção 2 amarra a regra ao driver.** Testar "débito não deixa o saldo
negativo" passaria a exigir um banco de pé, e a regra ficaria escrita em SQL,
onde é mais difícil de ler e impossível de compor.

**A opção 3 paga um custo real:** mais interfaces, mais mapeamento entre tipo de
domínio e linha de tabela, mais indireção para ler um fluxo de ponta a ponta.
Aceitamos esse custo porque ele compra três coisas concretas aqui:

- as invariantes ficam testáveis em memória, e por isso ficam testadas a fundo;
- a segunda porta de entrada custa um adaptador, não uma reimplementação;
- trocar `pgx` por outra coisa, ou o emulador de fila pelo serviço real, não
  toca o núcleo.

**Fx fica fora do domínio** pela mesma razão: DI é detalhe de composição. O
domínio deve compilar e ser testado sem que exista um container.

## Consequências

- Todo caso de uso novo começa declarando a porta de que precisa, não escolhendo
  a biblioteca.
- Mapeamento entre domínio e persistência é trabalho explícito e recorrente. É o
  preço, e ele é cobrado toda vez.
- O comando de verificação da golden rule entra no gate de CI. Regra que não é
  verificada mecanicamente vira recomendação, e recomendação não sobrevive a
  prazo.
- **Invariante continua sendo garantida duas vezes:** no domínio, porque é lá
  que ela é conhecida; e no banco, por constraint, porque é lá que ela sobrevive
  a um bug de aplicação ou a uma instância mal comportada. Hexagonal não dispensa
  a constraint — os dois lugares têm papéis diferentes.
