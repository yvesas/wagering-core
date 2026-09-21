# Simular clientes

> Sete cenários em bash que exercitam a API como um provedor exercitaria, contra
> o sistema rodando de verdade. Scripts em
> [`scripts/simulate/`](../../scripts/simulate/).

## Rodar

```sh
make up                              # o sistema precisa estar no ar
make simulate                        # todos os cenários, em ordem
make simulate SCENARIO=happy-path    # um só
N=50 make simulate SCENARIO=concurrency
```

Cada cenário imprime uma linha por verificação e um total no fim; o código de
saída é o que o `make` propaga.

```
== A bet of 25.00 ==
   ok  the bet is accepted -> 200
   ok  the status -> PROCESSED
   ok  the balance it observed -> 75.00

--- 17 passed, 0 failed
```

## Por que isto existe, se já há testes em Go

Porque prova outra coisa. A suíte Go prova que o **código** está certo; ela roda
contra um banco de teste, com um emissor de identidade em processo e sem
container de aplicação nenhum.

Estes scripts falam com o sistema **como ele é entregue**: o binário na imagem,
o Keycloak de verdade emitindo token de verdade, o emulador de fila, tudo pela
rede. É a diferença entre "os testes passam" e "alguém consegue usar".

Não é teoria: a primeira execução deles encontrou **três defeitos** que três
fases de teste não tinham encontrado. Estão na seção final.

E servem de exemplo. Um provedor que vá integrar lê `happy-path.sh` e
`idempotency.sh` e vê o HTTP que precisa mandar, sem biblioteca no meio.

## Onde eles rodam, e por quê

**Dentro da rede do compose**, como um serviço atrás do profile `sim`.

A razão é o `iss`. O simulador pede token ao Keycloak no mesmo endereço em que a
aplicação valida — `keycloak:8081` — então o problema descrito em
[`running.md`](running.md) não existe para ele: ele resolve ficando no lugar
certo, em vez de configurar em volta.

Os scripts são **montados por volume**, não copiados para a imagem: editar um
cenário e rodar de novo não pede rebuild.

A imagem tem `curl`, `jq` e `psql`. O `psql` está lá por um cenário só — plantar
uma divergência exige escrever onde o domínio não alcança, que é justamente o
ponto.

## Os cenários

| Cenário | O que prova |
|---|---|
| `happy-path` | Abrir carteira, apostar, ganhar, perder; ler por id e por id de negócio; o ledger e a reconciliação batendo |
| `idempotency` | Reenvio devolve o resultado guardado **e o saldo original**; mesma chave com conteúdo diferente é 409; mesma operação sob outra chave é 409; rejeição replica como rejeição |
| `security` | Sem token é 401 com o desafio e sem motivo; `rival` não envia como `acme`, não lê o que `acme` enviou, e recebe a mesma resposta de algo que nunca existiu; provedor não toca carteira |
| `concurrency` | N apostas idênticas em paralelo movem dinheiro uma vez; duas de 80,00 sobre 100,00 e só uma passa; carteiras distintas não esperam uma pela outra |
| `queue` | A mesma operação pela fila termina no mesmo estado; reentrega absorvida pelo registro de entrada; envelope impossível descartado com motivo |
| `reversals` | `REFUND` devolve; nada é revertido duas vezes; valor divergente é recusado; reversão que chega **antes** da referência é estacionada e resolve depois |
| `reconciliation` | Carteira saudável bate; divergência plantada aparece no corpo, na métrica e no log; **nada é corrigido** |

## Como ler a saída

- `ok` / `NOT` — uma verificação, com o valor obtido e o esperado.
- Linhas cinzas são contexto: id da carteira, contadores, onde olhar depois.
- O total no fim é o que decide o código de saída. Um cenário que morre no meio
  também falha: o resumo roda na saída, qualquer que seja o caminho.

Cada execução usa um `RUN_ID` próprio, então rodar de novo **não limpa nada** —
o ledger é append-only e não existe limpeza. É por isso que os identificadores
carregam o sufixo.

## O que a primeira execução encontrou

Vale registrar, porque é a justificativa destes scripts existirem.

**1. Um bug da aplicação: o registro de entrada não absorvia reentrega.**

`Claim` inseria e contava com a violação de chave primária para detectar
duplicata. No PostgreSQL um erro **aborta a transação**, e o passo seguinte do
consumidor é justamente ler o registro guardado — que falhava com `25P02`. Na
prática: a mensagem reentregue era devolvida para a fila, recebida de novo,
devolvida de novo, em laço, até a DLQ.

Passou por três fases porque a única cobertura era o fake em memória, que não
modela transação abortada. A correção é `ON CONFLICT DO NOTHING` — detectar a
duplicata sem levantar erro — e o teste de integração novo reproduz a sequência
inteira numa transação só.

**2. Um bug no compose: o healthcheck do LocalStack estava errado desde a fase 7.**

Ele exigia `"sqs": "available"`, e o LocalStack passa a dizer `"running"` assim
que a fila é usada. Ou seja: o container virava *unhealthy* no momento em que
começava a funcionar. Ninguém notou porque nada dependia daquele health — o
`localstack-test` estava assim havia 26 horas e os testes passavam.

**3. Duas afirmações erradas nos próprios cenários**, e o código certo: eu
esperava um código de conflito nomeado onde a resposta correta é o genérico, e
verifiquei `401` em rotas que não existem — onde o router responde `405` antes
de qualquer handler.

## Escrever um cenário novo

`lib.sh` traz o que todos usam: `token`, `http`, `submit`, `open_wallet`,
`balance_of`, `send_envelope`, `metric`, `wait_for` e as asserções.

```bash
#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"

ACME="$(token provider-acme)"
PLAYER="player-novo-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"

step "O que este passo demonstra"
submit "$ACME" "$(operation acme "op-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
expect_status 200 'aceita'
expect_json '.balance.amount' '75.00' 'o saldo observado'
```

Duas regras que valem a pena:

- **Nada de `sleep` como sincronização.** A porta de fila é assíncrona; use
  `wait_for`, que faz polling com prazo.
- **Sempre `$RUN_ID` nos identificadores.** Sem isso, a segunda execução colide
  com a primeira e a falha parece bug do sistema.

Para incluir no `all.sh`, acrescente o nome ao array `SCENARIOS`.
