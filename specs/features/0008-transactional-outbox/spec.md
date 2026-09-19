# 0008 — Publicação por registro de saída

- **Status:** implementada · 536 casos · `-race` limpo
- **Requisitos cobertos:** `REQ-EVT-001..009` · `REQ-TST-007`
- **Depende de:** features 0001 a 0007 (concluídas)

## O que foi construído

Os quatro eventos exigidos, gravados na mesma transação do fato que os originou,
e um publisher que os envia com backoff e suporta várias instâncias.

Decisões em [`docs/adr/0010-outbox.md`](../../../docs/adr/0010-outbox.md);
contrato em [`docs/events.md`](../../../docs/events.md).

## A decisão que mais vale explicar

O publisher reserva com `FOR UPDATE SKIP LOCKED`, publica **dentro** da transação
e marca no mesmo commit. Isso segura um lock de linha durante uma chamada de rede,
o que normalmente é cheiro ruim — e aqui é o ponto.

A alternativa seria um **lease com expiração**: um campo dizendo "estou
trabalhando nisto até tal hora", com um timeout maior que a pior publicação e
menor que a paciência de quem espera, e confiança no relógio de máquinas
diferentes.

O lock dá a mesma garantia de graça: **ele morre com a conexão**. "Recuperação de
trabalho abandonado" deixa de ser código nosso. O custo é contido por lote
pequeno e timeout de publicação explícito — sem ele, o lock duraria o que o
broker quisesse.

## Uma coisa que a guarda pegou

A varredura de ponto flutuante barrou um **teste meu**: comparar
`walletVersion` contra `float64(1)` porque `encoding/json` decodifica número como
float. Corrigi decodificando numa struct tipada. Versão lida através de float é
versão que pode arredondar.

## Como se prova

```sh
make check
make up-test && make test-integration
make test-scenarios
```

Os cenários cobrem trinta apostas com três publishers disputando o mesmo
registro de saída — cada evento sai uma vez — e um broker que não estava lá: o
dinheiro se move, os eventos ficam guardados, e saem quando alguém volta.
