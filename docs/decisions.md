# Decisões menores

> Uma linha por decisão. Decisão **estrutural** — que molda o sistema e seria
> cara de reverter — vira um arquivo em `docs/adr/`.
>
> Este arquivo é histórico e só cresce. Decisão revista ganha linha nova
> apontando para a antiga, em vez de a antiga ser editada.

| Data | Decisão | Motivo |
|---|---|---|
| 2026-09-18 | Go com Uber Fx | A stack é parte do objetivo: concorrência, `context`, `errors.Is`/`As` e composição por construtor são o que este problema exercita. `fx.Lifecycle` dá encerramento observável aos workers. |
| 2026-09-18 | Requisitos com ID versionados em `specs/project/REQUIREMENTS.md` | Precisam viajar no clone para o `spec.md` de cada feature poder citá-los. Ficariam órfãos fora do repositório. |
| 2026-09-18 | Plano, ordem das tarefas e andamento fora do repositório | Plano e verdade no mesmo arquivo envelhecem juntos e ninguém confia em nenhum dos dois. O repositório guarda o produto; a pasta de controle, a execução. |
| 2026-09-18 | Convenções de Go no `CLAUDE.md` do projeto | A rule `code-style.md` do baseline é de TypeScript e não se edita dentro de um projeto — a próxima instalação sobrescreve. O `CLAUDE.md` é o arquivo que o instalador nunca toca. |
| 2026-09-19 | O evento nasce no domínio (F8.1) | ADR 0010. Tipo e versão fixados pelo construtor: quem pudesse escolhê-los publicaria um v1 rotulado v2. |
| 2026-09-19 | Payload é retrato imutável, não referência | Montado na publicação leria o estado atual, e o mesmo evento diria coisas diferentes conforme o atraso. |
| 2026-09-19 | O lock da linha é o lease do publisher | Um lease com expiração exige timeout calibrado e relógio confiável entre máquinas; o lock morre com a conexão de graça. |
| 2026-09-19 | `eventId` cunhado na gravação, estável na republicação | É o que permite ao consumidor deduplicar o que a entrega at-least-once entrega duas vezes. |
| 2026-09-18 | Registro de entrada e domínio no mesmo commit (F7.1) | ADR 0009. Obrigou `ExecuteIn`, a forma do caso de uso que roda na transação de quem chama. As duas portas compartilham o mesmo código. |
| 2026-09-18 | Identidade é o `messageId` do envelope | O id do SQS muda em redrive, o que faria reentrega parecer mensagem nova. |
| 2026-09-18 | Commit primeiro, apagar depois | Apagar antes perde a operação; apagar depois pode duplicar, e é isso que o registro de entrada absorve. |
| 2026-09-18 | `maxReceiveCount` é do broker, não nosso | Contador nosso estaria em dois lugares e discordaria no primeiro reinício. |
| 2026-09-18 | `MessageGroupId` é a carteira | Mesma granularidade da coordenação. Por provedor serializaria todas as carteiras dele. |
| 2026-09-18 | Tag do LocalStack fixada em 3.8 | `latest` virou a imagem licenciada: a build passou a falhar com "License activation failed" num dia em que nada no repositório mudou. |
| 2026-09-18 | Uma reversão por operação, de qualquer tipo (F6.1) | ADR 0008. "Não duas do mesmo tipo" deixa `REFUND` + `ROLLBACK` devolverem o mesmo dinheiro duas vezes. Imposto por índice único parcial. |
| 2026-09-18 | Direção da reversão derivada do tipo referenciado | Tabela escrita à mão seria segunda opinião sobre o que uma aposta faz. |
| 2026-09-18 | `REVERSAL_EXCEEDS_BALANCE` separado de `INSUFFICIENT_FUNDS` | Aposta sem saldo é rotina; reversão que não cabe é dinheiro já entregue que não volta, e precisa de gente olhando. Código igual perderia o segundo no volume do primeiro. |
| 2026-09-18 | Espera com TTL **e** limite de tentativas | Só tentativas é frágil com backoff exponencial; só TTL gera consultas inúteis. |
| 2026-09-18 | Worker processa uma pendência por transação | Consequência da regra de deadlock do ADR 0007: uma transação trava uma linha de carteira. |
| 2026-09-18 | Lock pessimista por linha de carteira, não otimista puro (F5.1) | ADR 0007. Medido: sem o `FOR UPDATE`, quarenta apostas distintas numa carteira recusam boa parte com 409. O dinheiro fica certo — a versão pega tudo — e a disponibilidade quebra sob carga. |
| 2026-09-18 | Versão mantida como segunda garantia | O `FOR UPDATE` protege quem passou por ele; um caso de uso futuro que leia sem travar não é protegido por nada. |
| 2026-09-18 | Retry no unit of work, com jitter | É o único lugar que vê a transação inteira. Sem jitter, quem colidiu junto dorme junto e colide de novo no mesmo instante. |
| 2026-09-18 | Uma transação trava uma linha de carteira, sempre | Um só recurso travado não forma ciclo, logo não há deadlock. Constrange o futuro: duas carteiras exigem ordem por identificador. |
| 2026-09-18 | Cenários de concorrência em três processos, não goroutines | Goroutine compartilha pool e memória; um bug que dependesse de lock local passaria em todas. |
| 2026-09-18 | Sem tabela de chaves de idempotência (F4.1) | A linha da transação já é o registro. Duas linhas sobre o mesmo fato precisam ser mantidas em sincronia, e é assim que passam a discordar. |
| 2026-09-18 | Inserir e tratar o conflito, não consultar antes | Entre a consulta e o insert cabem outras cópias da mesma requisição. A constraint é a serialização; a consulta prévia é só otimização. |
| 2026-09-18 | Codificação canônica escrita à mão, com prefixo de comprimento | `chave=valor` concatenado é ambíguo quando o separador aparece dentro do valor — e `externalId` vem do provedor. O hash precisa ser estável para sempre, então não depende do comportamento de ordenação de mapa do `encoding/json`. |
| 2026-09-18 | Desfecho no status, não no erro | Rejeição como erro faria o primeiro envio falhar e o replay ter sucesso — replay lê linha gravada e não tem o que falhar. |
| 2026-09-18 | `REFUND`/`ROLLBACK` respondem 501 nesta fase | Precisam resolver referência, que é a fase 6. Dizer isso é melhor que aceitar e fazer outra coisa em silêncio. |
| 2026-09-18 | UUIDv7 via `google/uuid`, não escrito à mão (F3.1) | Ordena por tempo, o que mantém o insert na borda direita do índice. Escrever é trinta linhas que parecem inofensivas — e um contador sutilmente errado dentro do mesmo milissegundo apareceria tarde, como chave duplicada sob carga. |
| 2026-09-18 | Abertura sem `PENDING` intermediário | A operação não depende de nada e é confirmada junto da carteira: não há janela entre aceitar e aplicar, e nada para outra instância retomar. |
| 2026-09-18 | Handlers dependem de interfaces pequenas, não dos casos de uso concretos | Declaradas por quem consome. O ganho é prático: teste de handler precisa de dois stubs em vez de banco, pool e grafo. |
| 2026-09-18 | Conflito responde com código da API, não com nome de constraint | Devolver o nome vaza schema e faz o cliente depender dele. Renomear constraint passaria a ser mudança de contrato. |
| 2026-09-18 | `postgres.NewPool` deixou de usar o tipo de config do `platform` | Não foi preferência: virou ciclo de import quando `platform` passou a montar o adaptador. Adaptador que importa quem o monta não pode ser montado por mais ninguém — nem pelo próprio teste de integração. |
| 2026-09-18 | goose como biblioteca, migrations embarcadas por `go:embed` (F2.2) | CLI externa exigiria instalar binário. Embarcado, o binário carrega o próprio schema e não há passo de "copiou a pasta sql?". O goose pega advisory lock antes de rodar, que é o que torna seguro toda réplica chamar no start-up. |
| 2026-09-18 | Coluna de id é `TEXT`, não `UUID` (F2.3) | Id externo é o que o provedor mandou. `UUID` rejeitaria `transaction-123`, que é entrada legítima. |
| 2026-09-18 | Imutabilidade do ledger por trigger **e** `REVOKE` (F2.5) | `REVOKE` não alcança o dono da tabela, e migration roda como dono. São dois triggers: `TRUNCATE` não dispara trigger de linha, então sem o de statement a guarda teria uma porta ao lado. |
| 2026-09-18 | Ordenação do ledger por `seq`, não por `created_at` | Duas entradas do mesmo commit compartilham o timestamp, e empate faz cursor pular ou repetir linha. `seq` é único e monotônico, então a ordem é total. |
| 2026-09-18 | Diretiva do módulo subiu para Go 1.26 | Veio do `go get` do goose, não de escolha deliberada; aceito depois do fato. A toolchain é baixada automaticamente e `go-version-file: go.mod` mantém o CI alinhado. Dockerfile terá de usar imagem 1.26. |
| 2026-09-18 | Movimentação devolve carteira nova, não muta no lugar (F1.7) | Um débito recusado tem de deixar a carteira do chamador intacta. Com receptor por ponteiro isso é promessa que todo retorno antecipado precisa lembrar de cumprir; com receptor por valor é o compilador que garante. O custo é alocação, e ela é irrelevante no tamanho destes agregados. |
| 2026-09-18 | Um tipo de transação para as duas origens (F1.8) | Dois tipos duplicariam a máquina de estados, que é a parte arriscada, e a cópia divergiria no primeiro conserto aplicado só de um lado. A segurança que tipos separados comprariam fica de pé pelos campos não exportados e por dois structs de parâmetro distintos: uma abertura não tem onde pôr um id de provedor. |
| 2026-09-18 | Identificadores são strings opacas, não UUID | O domínio não decide como uma identidade é gerada: as internas são UUIDv7 cunhados por um adaptador e as externas são o que o provedor mandou (`transaction-123`). Validar formato aqui rejeitaria entrada legítima. |
| 2026-09-18 | Escala fixa de duas casas no tipo, não na moeda | Exclui JPY (zero casas) e KWD (três); aceito porque os fluxos são em BRL. O tipo carrega a moeda, então o que muda no dia em que outra escala entrar é o fator, não o desenho. |
| 2026-09-18 | `.claude/` bifurcado do `yas-claude-base` v0.6.4 | O baseline assume TypeScript, e a suposição matava guardas em silêncio. Detalhe em `.claude/README.md`. |
| 2026-09-18 | Branch resolvida por `symbolic-ref`, não `rev-parse` | Em repositório sem commits o `rev-parse --abbrev-ref HEAD` devolve `"HEAD"`, e o guard de commit na `main` liberava em silêncio. |
| 2026-09-18 | Guards de hook falham fechados sem parser JSON | Guarda que não lê o pedido não sabe o que autorizar. A versão anterior liberava tudo, inclusive `cat .env`. |
| 2026-09-20 | Reconciliação é `POST` embora não altere nada | Lê todo lançamento que a carteira já teve. `GET` convida cache, prefetch e retry-por-timeout, e nenhum deles devia decidir a frequência disso. |
| 2026-09-20 | Divergência de reconciliação responde 200 | Quem chamou perguntou se os dois batem; responder "não batem" é o endpoint funcionando. Um não-2xx faria um monitor tratar verificação bem-sucedida como requisição quebrada. |
| 2026-09-20 | Registry Prometheus próprio, não o `DefaultRegisterer` | Registry global é estado que qualquer dependência escreve, e registro duplicado entra em pânico no start-up. O que é exportado passa a ser o que o arquivo diz. |
| 2026-09-19 | Escopos no claim `scp` do realm local, não como client scopes do Keycloak | Declarar `clientScopes` num realm de import substitui os padrões e quebra o console administrativo. O serviço lê `scope` e `scp`; o realm local é conveniência de desenvolvimento, não modelo de produção. |
| 2026-09-19 | `golang-jwt` para o token, cache de chaves escrito aqui | A biblioteca faz a parte perigosa — parsing, `alg`, `exp`, `aud`. O cache é a parte que precisa ser testável contra um emissor que rotaciona e cai, e são cem linhas. |
| 2026-09-19 | Janela mínima de cinco segundos entre buscas de JWKS | Trinta segundos faziam uma rotação recusar token legítimo por meio minuto; zero deixaria `kid` inventado virar um jeito de martelar o IdP. |
| 2026-09-18 | `make setup` como porta de entrada do clone | Não há `package.json` para o `prepare` que reinstala o `core.hooksPath`. Sem um alvo explícito, o hook fica no repositório sem nunca executar. |

## `docs/architecture.md`

Nasceu com a feature 0001, quando passou a existir sistema para descrever. Ele
documenta o que **existe** — hoje, só o pacote de domínio — e não o que está
planejado. O desenho pretendido continua em `specs/project/PROJECT.md` e no
`spec.md` de cada feature.
