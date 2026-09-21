# 0011 — Execução em container e simulação de clientes

- **Status:** implementada
- **Requisitos cobertos:** `REQ-TST-002` (parcialmente — ver abaixo)
- **Depende de:** features 0001 a 0010 (concluídas)

## O que foi construído

`Dockerfile` multi-stage, a aplicação como serviço do compose, dois runbooks, e
sete cenários em bash que exercitam a API como um provedor exercitaria — rodando
dentro da rede, contra o sistema como ele é entregue.

Esta fase não cobre requisito novo. Ela fecha a distância entre "passa nos
testes" e "alguém consegue rodar", e o começo dela foi descobrir que **a
aplicação não rodava em Docker**: não havia `Dockerfile`, não havia serviço dela
no compose, e o `--build` do `make up` não construía nada — um flag sem efeito,
que é pior do que não estar lá.

## A decisão que mais vale explicar

**O Keycloak escuta na 8081 dos dois lados da rede.**

Um token é aceito só se o `iss` dele bater com `OIDC_ISSUER_URL`, e o discovery
recusa documento que se diga de outro emissor — as duas coisas de propósito,
desde a fase 9. Dentro de um container, `localhost:8081` é o próprio container.

Publicar `8081:8080`, que é o reflexo, cria um emissor com dois endereços: o
token minted pelo host carrega um `iss` que a aplicação em container não
alcança, e **todo token é recusado**, sem nada explicando por quê.

A saída não contorna: `--http-port=8081` com `ports: 8081:8081` e `--hostname`
fixando `http://keycloak:8081`. A mesma URL vale de dentro e de fora, e o
problema deixa de existir. O preço é uma linha em `/etc/hosts` para quem quiser
rodar a aplicação **fora** do Docker contra este Keycloak — documentado, e o
único caso que sobra.

É também o motivo de o simulador rodar dentro da rede: ele pede token no mesmo
endereço em que a aplicação valida. Resolve ficando no lugar certo.

## O que a primeira execução encontrou

Esta é a parte que justifica a fase.

### Um bug da aplicação, vivo desde a fase 7

O registro de entrada **não absorvia reentrega**. `Claim` inseria e contava com
a violação de chave primária para detectar a duplicata — mas no PostgreSQL um
erro **aborta a transação**, e o passo seguinte do consumidor é ler o registro
guardado para decidir o que a repetição significa. Essa leitura falhava com
`25P02`.

O efeito real: a mensagem reentregue era devolvida para a fila, recebida de
novo, devolvida de novo — em laço apertado, até a DLQ. Nos logs:

```
WARN releasing a message for redelivery
  error: current transaction is aborted, commands ignored until end of
         transaction block (SQLSTATE 25P02)
```

Passou por três fases de teste porque a única cobertura do caminho era o fake em
memória, e **um fake não modela transação abortada**. É a limitação de fake mais
cara que este projeto encontrou até agora.

A correção é `ON CONFLICT DO NOTHING`: detectar a duplicata sem levantar erro. O
teste de integração novo roda a sequência inteira numa transação — claim,
conflito, leitura — e foi verificado que ele **falha** contra a versão anterior,
com exatamente o `25P02`.

### Um healthcheck errado desde a fase 7

O do LocalStack exigia `"sqs": "available"`, e o LocalStack passa a dizer
`"running"` assim que a fila é usada. O container virava *unhealthy* no momento
em que começava a funcionar.

Ninguém notou porque nada dependia daquele health: o `localstack-test` estava
assim havia 26 horas e a suíte de integração passava. Só apareceu quando o
serviço `api` ganhou `depends_on: condition: service_healthy` — e aí bloqueou
tudo.

Vale a moral: **um healthcheck em que ninguém repara não é um healthcheck.**

### Duas afirmações erradas minhas

Nos próprios cenários, e o código certo nas duas: esperei um código de conflito
nomeado onde a resposta correta é o genérico `CONFLICT` — o caso nem chega ao
banco —, e verifiquei 401 em rotas que não existem, onde o router responde 405
antes de qualquer handler.

## Sobre `REQ-TST-002`

O requisito pede integração "com PostgreSQL, provedor de identidade e emulador
de fila em containers reais". Banco e fila sempre foram containers; o **provedor
de identidade** era, e continua sendo, um emissor em processo na suíte Go.

A fase 11 fecha isso por outro caminho: os cenários rodam contra o Keycloak de
verdade, com token de verdade, pela rede. Não é a suíte Go que passou a usar
container — é que passou a existir um caminho automatizado que usa.

Deixar a suíte Go dependendo de Keycloak custaria um container e ~30s em toda
execução de `make test-integration`, para provar o que treze casos de
falsificação já provam contra uma chave RSA real. O julgamento é esse, e está
registrado aqui para quem discordar saber o que mudar.

## Como se prova

```sh
make check
make up-test && make test-integration
make up && make simulate
```

O último sobe o sistema do zero e roda sete cenários, 110 verificações:

```
########  summary  ########
all 7 scenarios passed
```

Verificado com `docker compose down -v` antes, para que "do zero" fosse do zero.
