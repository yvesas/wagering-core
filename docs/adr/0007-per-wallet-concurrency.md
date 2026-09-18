# ADR 0007 — Coordenação por carteira: lock pessimista, versão e retry

- **Data:** 2026-09-18
- **Status:** aceita
- **Requisitos relacionados:** REQ-CON-001..008, REQ-WAL-008, REQ-PER-005
- **Decide:** F5.1

## Contexto

Duas apostas chegam ao mesmo tempo, na mesma carteira, e o saldo só cobre uma.
Uma precisa ser processada e a outra rejeitada por saldo insuficiente — nunca as
duas processadas, nunca saldo negativo, nunca um débito perdido.

Ao mesmo tempo, **carteiras diferentes não podem esperar uma pela outra**. Lock
global resolveria a primeira exigência e destruiria a segunda.

O que já existe: `UpdateBalance` condiciona a escrita à versão lida, numa
instrução só. Isso já impede lost update — a escrita que perdeu a corrida não
casa linha nenhuma. O que **não** existe é o que fazer quando ela não casa.

## Opções

**1. Otimista puro: versão mais retry.** Ninguém segura lock; quem perde relê e
tenta de novo.

**2. Pessimista: `SELECT ... FOR UPDATE` na linha da carteira.** Quem chega
primeiro segura a linha até o commit; os outros esperam.

**3. Isolamento `SERIALIZABLE`.** O banco detecta o conflito e aborta uma das
transações.

## Decisão

**As três camadas, nesta ordem:**

1. **`SELECT ... FOR UPDATE` na linha da carteira**, dentro da transação, é a
   coordenação de verdade.
2. **`UPDATE` condicionado à versão** continua, como segunda garantia.
3. **Retry com backoff e jitter, limitado**, no unit of work, para falha de
   serialização e deadlock que o banco levante mesmo assim.

## Motivo

**O otimista puro falha pior justamente quando o sistema está ocupado.** Com N
escritores disputando uma carteira, N−1 fazem trabalho jogado fora e voltam para
a fila. O orçamento de tentativas passa a decidir quem consegue apostar, e com
carga alta a resposta vira "tente de novo" para quem teve azar. É a forma de
degradação mais difícil de explicar para alguém: o sistema não está quebrado,
está ocupado, e mesmo assim recusa.

Isso não é especulação. Removendo o `FOR UPDATE` e deixando só versão e três
tentativas, o cenário de quarenta apostas distintas numa carteira — três
processos, soltas juntas — recusa boa parte delas com `409 VERSION_MISMATCH`. O
dinheiro continua certo, porque a condição de versão pega tudo; o que quebra é a
disponibilidade, e quebra sob carga.

**O lock pessimista tem exatamente a granularidade que o requisito pede.**
Trava-se uma **linha**, não uma tabela: a carteira A não faz a carteira B
esperar, o que é a definição de "carteiras independentes avançam em paralelo".
Quem espera, espera pouco — a transação é curta e não faz I/O externo enquanto
segura o lock.

**A versão fica como segunda garantia, e não é redundância.** O `FOR UPDATE`
protege quem passou por ele. Um caso de uso futuro que leia a carteira sem
travar, e depois escreva, não é protegido por nada — e a condição de versão
recusa essa escrita em vez de deixá-la sobrescrever. É a mesma lógica das
constraints de banco: a primeira linha de defesa é código, e código tem bug.

**`SERIALIZABLE` faz toda consulta pagar** por um conflito que só existe numa
linha, e ainda assim exige o retry. Ele resolve por detecção o que o `FOR UPDATE`
resolve por prevenção, com mais aborto e menos controle sobre onde o custo cai.

## A regra de deadlock, que é o preço deste desenho

**Uma transação trava exatamente uma linha de carteira, e sempre a carteira.**
Com um só recurso travado por transação não existe ciclo, então não existe
deadlock por ordenação.

Isso **constrange o código futuro**: o dia em que uma operação precisar mexer em
duas carteiras — transferência entre jogadores, por exemplo — ela terá de travar
as duas **numa ordem determinística**, por identificador crescente. Travar na
ordem em que aparecem no payload é como duas transferências opostas se
bloqueiam mutuamente às três da manhã.

O retry existe também por isso: se essa regra for quebrada, o banco detecta o
deadlock, aborta uma das transações, e a tentativa seguinte tende a passar. Ele
não conserta o bug — só evita que o primeiro sintoma seja uma requisição
perdida.

## O retry, e por que ele tem jitter

Repete apenas o que é **transitório**: falha de serialização, deadlock detectado
e conflito de versão. Rejeição de negócio nunca é repetida — saldo insuficiente
não melhora tentando de novo, e repetir transformaria uma recusa numa espera.

O backoff é exponencial **com jitter**. Sem jitter, N escritores que colidiram
juntos dormem o mesmo tempo e colidem de novo, no mesmo instante, em bloco: o
backoff sincroniza exatamente o que deveria espalhar.

O número de tentativas é limitado e configurável. Ilimitado, uma carteira
disputada vira um punhado de requisições que nunca respondem e nunca desistem.

**O callback precisa poder rodar de novo.** O unit of work já obriga isso — ele
não guarda estado entre tentativas —, e a leitura da carteira acontece *dentro*
dele, então cada tentativa enxerga o estado fresco. Uma leitura feita fora e
reaproveitada faria a segunda tentativa decidir com dados velhos, que é o bug
que o retry deveria evitar.

## Consequências

**O retry mora no unit of work**, que é o único lugar que vê a transação inteira
(ADR 0003). Espalhado pelos casos de uso, cada um teria a própria política e a
própria versão do bug.

**`FindByIDForUpdate` existe só na porta de escrita**, não na de leitura. Travar
uma linha fora de transação não faz sentido, e o compilador é quem impede.

**A demonstração exige processos separados.** Goroutines compartilham pool e
memória: elas provam que o código é seguro para threads, não que a garantia está
no banco. Os cenários obrigatórios rodam com três binários independentes, cada
um com suas conexões.
