# Requisitos — wagering-core

> Corpo de requisitos do produto, com IDs estáveis. O `spec.md` de cada feature
> cita os IDs que cobre; o código e os testes referenciam a spec. **IDs nunca são
> reaproveitados nem renumerados.**
>
> A ordem de execução está no plano de ação, fora do repositório. Este arquivo
> descreve *o quê*, não *quando*.

## Visão

Um serviço distribuído que movimenta carteiras de jogadores a partir de
operações enviadas por provedores de jogo, por API HTTP e por fila, com o mesmo
resultado financeiro em ambos os caminhos.

A propriedade que domina todo o resto: **o resultado financeiro permanece
correto com várias instâncias em execução e falhas entre as etapas do
processamento.**

---

## Money — `REQ-MON`

| ID | Requisito |
|---|---|
| REQ-MON-001 | `Money` é um value object imutável, com valor e moeda. |
| REQ-MON-002 | Dinheiro não passa por `float32` ou `float64` em nenhuma etapa — parsing, cálculo, serialização ou persistência. |
| REQ-MON-003 | Representação interna em `int64` de unidades mínimas ou biblioteca decimal de precisão exata, com limites documentados. |
| REQ-MON-004 | Operações: criação a partir de string decimal, zero por moeda, soma, subtração, negação, comparação e serialização. |
| REQ-MON-005 | O contrato externo recebe e devolve `{"amount":"25.00","currency":"BRL"}` — escala fixa de duas casas, moeda ISO 4217. |
| REQ-MON-006 | Rejeitar valor vazio, `NaN`, `Infinity`, notação científica, escala excedente e negativo em entrada financeira externa. |
| REQ-MON-007 | Nunca arredondar silenciosamente uma entrada inválida. Formas equivalentes aceitas têm a normalização documentada, e ela ocorre antes do hash de idempotência. |
| REQ-MON-008 | Aritmética e comparação exigem moedas compatíveis; incompatibilidade é erro. |
| REQ-MON-009 | Overflow tratado em parsing, soma, subtração e negação. |
| REQ-MON-010 | Valor negativo é permitido em diferença e cálculo interno, nunca no saldo da carteira. |
| REQ-MON-011 | A persistência preserva valor e moeda exatamente (unidades mínimas em `BIGINT` ou decimal em `NUMERIC`). |

## Carteira — `REQ-WAL`

| ID | Requisito |
|---|---|
| REQ-WAL-001 | A carteira é a raiz do agregado financeiro e carrega identidade, jogador, moeda, saldo, versão e instantes de criação e atualização. |
| REQ-WAL-002 | O par `(playerId, currency)` identifica uma única carteira. |
| REQ-WAL-003 | Débito preserva saldo maior ou igual a zero. |
| REQ-WAL-004 | A moeda de cada movimentação coincide com a da carteira. |
| REQ-WAL-005 | Toda mudança financeira produz o lançamento correspondente no ledger, confirmado junto com o saldo. |
| REQ-WAL-006 | A versão inicial é `1`; depois da criação, incrementa apenas quando o saldo muda. |
| REQ-WAL-007 | Criação e reidratação são caminhos separados. A reidratação não reaplica movimentação, transição nem emissão de evento. |
| REQ-WAL-008 | Disputa entre escritores não descarta uma atualização já confirmada. |

## Ledger — `REQ-LED`

| ID | Requisito |
|---|---|
| REQ-LED-001 | Cada lançamento registra `id`, `walletId`, `transactionId`, direção (`DEBIT`/`CREDIT`), valor, saldo anterior, saldo posterior e instante de criação. |
| REQ-LED-002 | O lançamento é imutável; sua construção valida `balanceAfter = balanceBefore ± money` conforme a direção. |
| REQ-LED-003 | O ledger é append-only: correção financeira exige lançamento novo, nunca edição. |
| REQ-LED-004 | O banco impõe unicidade de `(walletId, transactionId)`. |
| REQ-LED-005 | O banco impede edição e exclusão de lançamento. |
| REQ-LED-006 | Operação sem movimentação e operação rejeitada não produzem lançamento. |

## Transação — `REQ-TRX`

| ID | Requisito |
|---|---|
| REQ-TRX-001 | Tipos: `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. |
| REQ-TRX-002 | Operação externa registra identificadores interno e externo, provedor, chave de idempotência, hash do payload, carteira, jogador, rodada, jogo, tipo, valor, referência externa opcional, estado e timestamps. |
| REQ-TRX-003 | Quando aplicável, persiste também a referência interna resolvida, o código de falha e o resultado financeiro devolvido ao provedor. |
| REQ-TRX-004 | Estados: `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED`. Os três últimos são terminais. |
| REQ-TRX-005 | Transação terminal não sofre nova transição. As transições são validadas pelo domínio. |
| REQ-TRX-006 | Replay consulta o resultado persistido sem reaplicar a operação. |
| REQ-TRX-007 | Todo `PENDING` confirmado tem retomada durável por outra instância após interrupção. Operação sem dependência pode concluir de forma síncrona, sem commit intermediário de aceite. |
| REQ-TRX-008 | `OPENING` é reservado à abertura interna de carteira e é rejeitado quando chega por HTTP ou por fila. |
| REQ-TRX-009 | `OPENING` exige identidade interna estável, carteira, jogador, moeda, valor, estado e timestamps; metadados externos não se aplicam. O schema distingue origem interna de externa e impede crédito inicial duplicado. |
| REQ-TRX-010 | A distinção entre falha transitória e falha permanente é explícita e documentada. |
| REQ-TRX-011 | Erros de domínio são classificáveis por tipo ou `errors.Is`/`errors.As`. `panic` não representa rejeição de negócio. |

## Operações e referências — `REQ-OPS`

| ID | Requisito |
|---|---|
| REQ-OPS-001 | `BET` debita; exige valor positivo e saldo suficiente. |
| REQ-OPS-002 | `WIN` credita; exige valor positivo e pode citar uma aposta da mesma rodada como referência. |
| REQ-OPS-003 | `LOSS` não movimenta; exige valor `"0.00"`, não cria lançamento e não altera a versão da carteira. Exige a moeda da carteira. |
| REQ-OPS-004 | `REFUND` credita, devolvendo integralmente o valor de uma aposta processada. |
| REQ-OPS-005 | `ROLLBACK` aplica o movimento contrário, desfazendo integralmente uma aposta, ganho ou devolução processada. |
| REQ-OPS-006 | Em `REFUND` e `ROLLBACK` a referência externa é obrigatória e resolvida por `(providerId, referenceExternalTransactionId)`. |
| REQ-OPS-007 | Operação e referência concordam em provedor, jogador, carteira, moeda e rodada; o valor da reversão é igual ao referenciado. Reversão parcial está fora de escopo. |
| REQ-OPS-008 | Uma referência não recebe duas reversões bem-sucedidas do mesmo tipo. A combinação de devolução e desfazimento sobre a mesma aposta é documentada e impede devolução duplicada do mesmo débito. |
| REQ-OPS-009 | Reversão que precisaria debitar mais que o saldo disponível é rejeitada, auditável, e com código de falha distinto do usado para aposta sem saldo. |
| REQ-OPS-010 | Referência ainda indisponível persiste a operação em `PENDING_REFERENCE`; um worker tenta de novo com backoff exponencial, inclusive após reinicialização. |
| REQ-OPS-011 | A pendência tem número máximo de tentativas ou TTL; esgotado, finaliza como rejeição com código de referência não encontrada e produz o evento correspondente. |
| REQ-OPS-012 | O comportamento é definido também quando a referência existe mas está pendente, ou terminou sem sucesso. |
| REQ-OPS-013 | Toda rejeição fornece um `failureCode` estável e documentado, distinguindo entrada corrigível de resultado definitivo. |

## Idempotência — `REQ-IDE`

| ID | Requisito |
|---|---|
| REQ-IDE-001 | A idempotência é persistente e sobrevive ao reinício de todos os processos. |
| REQ-IDE-002 | A chave de idempotência é obrigatória na entrada por HTTP; o servidor não substitui silenciosamente a chave recebida por outra calculada. |
| REQ-IDE-003 | Um hash determinístico dos campos de negócio é persistido, sobre JSON canônico com ordenação de chaves, excluindo a chave e os metadados de transporte. |
| REQ-IDE-004 | Algoritmo, campos e normalizações são documentados, e o hash é equivalente entre os caminhos de entrada. |
| REQ-IDE-005 | Chave e conteúdo equivalentes devolvem o resultado persistido, sinalizando que é replay. |
| REQ-IDE-006 | Chave reutilizada com conteúdo diferente devolve conflito. |
| REQ-IDE-007 | Uma operação identificada por `(providerId, externalTransactionId)` não pode ser reaplicada sob outra chave. |
| REQ-IDE-008 | Para operação concluída, o replay devolve o saldo observado no processamento original, mesmo que a carteira já tenha recebido outras movimentações. |

## Concorrência — `REQ-CON`

| ID | Requisito |
|---|---|
| REQ-CON-001 | A coordenação ocorre por carteira. Locks globais são proibidos; carteiras independentes avançam em paralelo. |
| REQ-CON-002 | A estratégia — lock pessimista, controle otimista com retry limitado, atualização atômica condicionada ou combinação — é escolhida e justificada. |
| REQ-CON-003 | As invariantes financeiras são garantidas no banco, independentemente de locks locais e de deduplicação da camada de transporte. |
| REQ-CON-004 | Atualização de saldo impede lost update. |
| REQ-CON-005 | As garantias se demonstram com pelo menos três processos independentes, cada um com suas próprias conexões e memória. |
| REQ-CON-006 | Cenário obrigatório: carteira com 100,00 recebe simultaneamente duas apostas distintas de 80,00 → uma processada, uma rejeitada por saldo insuficiente, saldo final 20,00, um único lançamento. Reenvio não altera esse resultado. |
| REQ-CON-007 | Cenário obrigatório: a mesma operação enviada 50 vezes em paralelo produz um único débito. |
| REQ-CON-008 | O sistema não depende de uma única instância para funcionar corretamente. |

## Persistência — `REQ-PER`

| ID | Requisito |
|---|---|
| REQ-PER-001 | PostgreSQL, com migrations versionadas e aplicação e reversão documentadas. |
| REQ-PER-002 | Acesso com `pgx` e SQL explícito; transações, locks e constraints permanecem explícitos e verificáveis. |
| REQ-PER-003 | Unicidade, não negatividade e imutabilidade do ledger são impostas pelo schema e pelas constraints, não só pelo código. |
| REQ-PER-004 | A fronteira da transação SQL entre repositórios é explícita e documentada. |
| REQ-PER-005 | Estado da operação, saldo, lançamento, registro de entrada e registros de evento são confirmados atomicamente, conforme aplicável. |

## API HTTP — `REQ-API`

| ID | Requisito |
|---|---|
| REQ-API-001 | `POST /wallets` abre carteira a partir de jogador e saldo inicial. |
| REQ-API-002 | Abertura com saldo positivo cria `OPENING` em `PROCESSED`, seu lançamento de crédito e os registros de evento no mesmo commit da carteira; a versão da carteira é `1`. |
| REQ-API-003 | Saldo inicial zero não cria `OPENING`, lançamento nem eventos financeiros. |
| REQ-API-004 | Abrir segunda carteira para o mesmo jogador e moeda resulta em conflito. |
| REQ-API-005 | `GET /wallets/:walletId` e `GET /wallets/:walletId/ledger` com paginação por cursor opaco e ordenação estável. |
| REQ-API-006 | `GET /health/live` e `GET /health/ready` — liveness do processo, readiness das dependências. |
| REQ-API-007 | `POST /wagering/transactions` recebe a operação, com chave de idempotência obrigatória no header. |
| REQ-API-008 | `GET /wagering/transactions/:transactionId` e `GET /providers/:providerId/wagering/transactions/:externalTransactionId` permitem acompanhar pendência e consultar código de rejeição ou falha. |
| REQ-API-009 | Os códigos HTTP e os corpos de resposta para entrada inválida, conflito, rejeição de negócio, processamento pendente e indisponibilidade transitória são documentados e distinguíveis pelo contrato. |
| REQ-API-010 | `POST /wallets/:walletId/reconciliation` reconstrói o saldo a partir do ledger, incluindo a abertura, e compara com o armazenado numa visão consistente. Não altera o saldo. |
| REQ-API-011 | A reconciliação reporta divergência na resposta, nos logs e numa métrica. |

## Mensageria — `REQ-MSG`

| ID | Requisito |
|---|---|
| REQ-MSG-001 | Fila principal e fila de descarte provisionadas, com configuração de redrive. |
| REQ-MSG-002 | A entrega é at-least-once; o mesmo envelope pode chegar repetido, inclusive cruzando com o caminho HTTP. |
| REQ-MSG-003 | Os dois caminhos de entrada compartilham o caso de uso e as garantias de idempotência financeira. |
| REQ-MSG-004 | O identificador da mensagem no envelope é a identidade durável para o consumidor, e seu hash é verificado em reentrega. |
| REQ-MSG-005 | O registro de entrada impõe unicidade de `(consumerName, messageId)`. |
| REQ-MSG-006 | O registro de entrada e a conclusão durável do tratamento compartilham a transação SQL das alterações de domínio, do ledger e dos eventos. |
| REQ-MSG-007 | A mensagem só é removida da fila após o commit do seu tratamento durável. |
| REQ-MSG-008 | Rejeição de negócio confirmada é terminal e permite remover a mensagem. |
| REQ-MSG-009 | Falha transitória exige retry com backoff; erro permanente ou tentativas esgotadas chegam à fila de descarte. |
| REQ-MSG-010 | Limites de tentativa, tempo de invisibilidade, agrupamento, deduplicação de transporte e tratamento de mensagem inválida são documentados. |
| REQ-MSG-011 | Em sinal de término, o consumidor para de buscar trabalho e conclui o que está em andamento dentro do prazo, ou libera a mensagem para reentrega segura. |
| REQ-MSG-012 | Pendência de referência pode ter sua mensagem de entrada concluída depois de a pendência estar persistida; o worker de referências assume a continuidade. |

## Eventos — `REQ-EVT`

| ID | Requisito |
|---|---|
| REQ-EVT-001 | Evento externo só é publicado depois da confirmação da transação que o originou. |
| REQ-EVT-002 | O registro de saída guarda identidade estável do evento, agregado, tipo, payload, ocorrência, tentativas, próximo envio e publicação. |
| REQ-EVT-003 | Um worker separado publica os registros pendentes, suportando múltiplos publishers, disputa por registro, backoff e recuperação de trabalho abandonado. |
| REQ-EVT-004 | Republicação preserva o identificador do evento. A recuperação é demonstrada para interrupção entre commit e publicação, e entre publicação e confirmação. |
| REQ-EVT-005 | Eventos exigidos: conclusão bem-sucedida de operação (inclusive a sem movimentação), rejeição definitiva por regra de negócio, alteração efetiva de saldo e registro de espera por referência. |
| REQ-EVT-006 | O envelope contém identificador do evento, tipo, agregado, correlação, causação opcional, ocorrência, versão e dado tipado. Tipo e versão são definidos pelo construtor do evento. |
| REQ-EVT-007 | O evento de alteração de saldo carrega carteira, transação, direção, valor, saldo anterior, saldo posterior e versão da carteira. |
| REQ-EVT-008 | Timestamps em UTC no formato RFC 3339, valores monetários em string decimal, e o payload do registro de saída é um snapshot imutável. |
| REQ-EVT-009 | O destino dos eventos é provisionado, com contratos de roteamento e consumo documentados. |

## Segurança — `REQ-SEC`

| ID | Requisito |
|---|---|
| REQ-SEC-001 | Autenticação e autorização são obrigatórias nos endpoints de negócio, integradas a um provedor de identidade externo OAuth 2.0/OIDC. |
| REQ-SEC-002 | A identidade autenticada determina o provedor autorizado. Cadastro de senha e emissão própria de token estão fora de escopo. |
| REQ-SEC-003 | Cada provedor acessa apenas suas próprias transações, inclusive em replay. |
| REQ-SEC-004 | Operações de carteira são restritas ao serviço interno. |
| REQ-SEC-005 | O acesso à mensageria é controlado por credenciais e políticas do broker, sem que isso dispense as validações de domínio no consumidor. |
| REQ-SEC-006 | Credencial ausente, inválida ou expirada é rejeitada, sem efeito financeiro e sem exposição de dado. |
| REQ-SEC-007 | Nenhum segredo real é versionado; o arquivo de exemplo traz apenas nomes de variável e valores locais. |

## Observabilidade — `REQ-OBS`

| ID | Requisito |
|---|---|
| REQ-OBS-001 | Logs em JSON com os identificadores disponíveis para rastrear a operação: correlação, mensagem, transação, carteira e provedor. |
| REQ-OBS-002 | Não registrar credencial, dado sensível nem payload financeiro completo. |
| REQ-OBS-003 | Métricas de resultado por status, duplicata, retry, descarte, conflito de concorrência, atraso da publicação, latência de processamento e divergência de reconciliação. |
| REQ-OBS-004 | Health checks expostos pela API. |

## Verificação — `REQ-TST`

| ID | Requisito |
|---|---|
| REQ-TST-001 | Testes de unidade cobrindo parsing e operações monetárias, escala, limites numéricos, entrada inválida, incompatibilidade de moeda, invariantes da carteira, transições de estado, regras de cada tipo externo, política de valor zero por tipo, abertura interna e conflito de payload para a mesma chave. |
| REQ-TST-002 | Testes de integração com PostgreSQL, provedor de identidade e emulador de fila em containers reais — não substituir toda a infraestrutura por mocks. |
| REQ-TST-003 | Verificar migrations, constraints, imutabilidade do ledger, atomicidade financeira, registro de entrada, reentrega, publicação concorrente, retry, descarte e recuperação após reinicialização. |
| REQ-TST-004 | Verificar a composição por injeção de dependência: início, encerramento e liberação de recursos dos workers. |
| REQ-TST-005 | Cenários de concorrência e recuperação, com pelo menos três instâncias independentes nos casos relevantes. |
| REQ-TST-006 | Interromper o consumidor depois do commit e antes da remoção da mensagem, e validar a reentrega. |
| REQ-TST-007 | Dois publishers disputando o mesmo registro de saída, validando a recuperação da publicação. |
| REQ-TST-008 | Entregar reversão antes da referência e comprovar a resolução posterior ou a rejeição por expiração. |
| REQ-TST-009 | Reiniciar a aplicação e verificar que idempotência, pendências e consistência financeira foram preservadas. |
| REQ-TST-010 | Ao final de cada cenário, conferir o saldo armazenado contra a soma de créditos menos débitos do ledger. |
| REQ-TST-011 | `go test ./...`, `go test -race ./...` e `go vet ./...` verdes; código formatado com `gofmt` e dependências reproduzíveis. |

---

## Invariantes que não se negociam

Qualquer uma destas violada significa que o sistema está errado, mesmo que os
testes passem:

1. Dinheiro nunca passa por ponto flutuante.
2. Saldo nunca fica negativo, nem sob concorrência.
3. Nenhuma movimentação é aplicada duas vezes.
4. Idempotência nunca depende de memória de processo.
5. Nenhum evento é publicado antes do commit que o originou.
6. O ledger nunca é editado nem apagado.
7. Nenhuma carteira depende de lock global para avançar.
8. O sistema nunca depende de uma única instância para estar correto.
