# Regra — Testes (Go)

> Enforçada por: `make check` (`go test ./...` e `go test -race ./...`), o gate
> de cada task e o CI.

## O contrato

- **Teste anda junto com o código, na mesma task.** Não existe "task de escrever
  os testes" no fim — se a task cria uma camada que exige teste, o teste faz
  parte do critério de pronto dela.
- **Teste que passaria também sem a mudança não conta.** O critério é: ele
  falharia na versão anterior? Se não, ele não está provando nada.
- Este projeto tem invariantes financeiras listadas no fim de
  `specs/project/REQUIREMENTS.md`. Suíte verde **não** substitui nenhuma delas.

## Padrões

- **`table-driven` é a forma padrão.** Casos num slice de struct, `t.Run` por
  caso, nome do caso descrevendo a situação e não o número.
- **Unit** cobre regra de domínio e caso de borda; roda sem rede, sem banco e
  sem container. É o grosso da suíte, e é onde as invariantes são exercitadas a
  fundo — barato é o que faz ficar completo.
- **Integração** usa **PostgreSQL de verdade**, não mock: o ponto é justamente
  provar constraint, transação e lock, que mock nenhum reproduz. Sobe com
  `docker compose --profile test up -d`.
- **Teste de integração fica atrás de build tag** (`//go:build integration`), e
  o comando que os inclui está no `Makefile`. Assim `go test ./...` continua
  rápido e sem dependência externa.
- **`t.Parallel()` onde o teste é independente** — e nunca onde ele compartilha
  estado, que é como um teste flaky nasce parecendo bug de código.
- **`t.Cleanup` em vez de `defer`** para desfazer setup: roda também quando o
  teste falha no meio.
- **`t.Helper()` em toda função auxiliar de teste**, senão a falha aponta para a
  linha errada.
- **Fixture sintética**, nunca payload real com dado de gente.
- Teste que depende de relógio recebe um relógio injetado. `time.Sleep` não é
  sincronização.

## Concorrência

- **`go test -race` é obrigatório** e faz parte de `make check`.
- **`-race` não prova ausência de corrida** — detecta o que aconteceu naquela
  execução. Cenário de concorrência precisa de disputa real: N goroutines,
  barreira de largada (`sync.WaitGroup` ou canal fechado), e repetição
  (`-count`) para a janela aparecer.
- Cenário de concorrência entre **processos** não se faz com goroutines. Exige
  binários separados, cada um com suas conexões e sua memória — é o único jeito
  de provar que a garantia está no banco e não num lock local.
- Ao fim de todo cenário financeiro, conferir o saldo armazenado contra a soma
  de créditos menos débitos do ledger. Essa conferência é parte do teste, não
  uma checagem manual.

## Footguns

- **Variável de laço capturada em goroutine.** Desde Go 1.22 cada iteração tem
  sua própria variável, mas código antigo e exemplos da internet ainda trazem o
  bug — e ele some do olho e aparece só sob `-race`.
- **Teste de integração que compartilha o banco de dev** apaga dado de trabalho
  e produz falha intermitente que parece bug de código. Daí o segundo banco no
  compose, em `tmpfs`, e o `--profile test`.
- **Ordem de teste não é garantia.** Teste que só passa depois de outro está
  acoplado por estado, e vai quebrar no dia em que alguém passar `-shuffle`.
- Erro de negócio (saldo insuficiente) **não** é falha técnica: não abre circuit
  breaker, não vai para DLQ e não conta como indisponibilidade.
