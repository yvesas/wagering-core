# Regra — CI e minuto de Actions

> Enforçada por: o próprio workflow (`concurrency`, guarda de diff,
> `timeout-minutes`), o checklist do `/pr` e code review.
>
> Não há hook para isto, e é uma limitação real: minuto gasto não aparece no
> diff. Quem vê é a fatura, um mês depois, quando não dá mais para desfazer.

## Como o minuto é cobrado

Três fatos que explicam quase todo gasto alto:

- **A conta é por job, arredondada para cima até o minuto.** Um job de doze
  segundos custa um minuto. Dez jobs de doze segundos custam dez.
- **Cada job é uma VM nova.** Dividir o pipeline em jobs paralelos deixa ele
  mais rápido no relógio e **mais caro** na fatura: cada job repete checkout e
  instalação, e o que se paga é a soma, não o maior.
- **Repositório público não consome cota; privado consome.** O runner também
  tem multiplicador: `ubuntu` 1×, `windows` 2×, `macos` 10×.

## A que mais economiza: agrupar

**Um PR paga o pipeline inteiro — checkout, instalação, build — mesmo quando o
diff é de uma linha.** Cinco PRs de uma linha custam cinco pipelines; um PR com
cinco commits custa um. Nenhuma otimização dentro do workflow compensa essa
diferença, então ela vem primeiro.

- **"Um assunto por PR" continua valendo, mas "um assunto" não é "um arquivo".**
  Enquanto o assunto for o mesmo, empilhe commits atômicos na mesma branch e
  abra um PR só.
- **PR não é checkpoint.** Cada push na branch dispara um run novo. Rode
  `make check` localmente — de graça — e empurre quando o assunto estiver
  fechado.
- Correção pequena que apareceu no meio do caminho vai **junto**, se for do
  mesmo assunto. Se não for, ela espera a próxima branch em vez de virar um PR
  de duas linhas.
- Se o trabalho precisa mesmo ficar visível antes de terminar, use **draft** e
  barre o job:

  ```yaml
  on:
    pull_request:
      # `ready_for_review` NÃO é tipo padrão. Sem ele, marcar o PR como pronto
      # não dispara run nenhum, e o check obrigatório nunca chega — o mesmo
      # "PR esperando para sempre" que condena o `paths:` mais abaixo.
      types: [opened, synchronize, reopened, ready_for_review]

  jobs:
    check:
      if: github.event.pull_request.draft == false
  ```

  Draft **não** deixa de disparar workflow por si só — sem esse `if`, o rascunho
  custa igual.

## Cancelar o run que já não interessa

Duas linhas, e costuma ser o maior ganho isolado: iterar numa branch deixa três
ou quatro runs completos correndo em paralelo para falar de commits que ninguém
vai mergear. Só a resposta mais nova é lida.

```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

O `github.workflow` no grupo evita que dois workflows do mesmo repo cancelem um
ao outro por dividirem o nome.

**`cancel-in-progress: true` só em CI.** Num workflow de deploy ou release ele
mata a publicação pela metade — lá o grupo fica sem cancelamento, ou com
`cancel-in-progress: false`, que enfileira em vez de matar.

## Não testar duas vezes o mesmo commit

Com **squash merge**, o commit que entra na `main` é o conteúdo da branch cujo
CI já passou. Rodar de novo no `push` é checar o mesmo código duas vezes e pagar
duas vezes.

```yaml
on:
  pull_request:      # e não `push: branches: [main]`
```

O dia em que um merge der errado, o próximo PR encontra — ele rebaseia naquela
`main`. Se o projeto precisa de algo na `main` (deploy, tag, release), isso é
**outro workflow**, com os passos de deploy, não uma segunda rodada de teste.

## Não rodar o que a mudança não pode quebrar

Um parágrafo em `docs/` não quebra um tipo, e uma tela não quebra uma query.
Descobrir isso custa segundos; provar o contrário roda a suíte inteira.

**Guarde por `if:` de step, não por `paths:` no gatilho.** O filtro de `paths`
parece mais limpo e tem um footgun caro: se o workflow é *required check* na
branch protection, o run que o filtro pulou **nunca reporta status**, e o PR
fica esperando para sempre um check que não vem. O mesmo vale para `[skip ci]`
na mensagem de commit. Com a guarda por step, o job existe, fica verde, e só os
passos caros são pulados.

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0          # sem histórico não há diff contra a base

- id: touched
  run: |
    git fetch --no-tags origin "${{ github.base_ref }}"

    # `core.quotePath=false`: sem isso o git devolve caminho com acento citado
    # e escapado — `"docs/decis\303\243o.md"` —, que não casa com `^docs/`.
    # Erra para o lado seguro (roda tudo), mas anula a guarda num repo cuja
    # documentação é em português.
    changed=$(git -c core.quotePath=false diff --name-only \
      "origin/${{ github.base_ref }}"...HEAD || true)

    # Silêncio significa que o diff falhou, não que nada mudou. Pular numa
    # guarda quebrada é como um gate desaparece sem ninguém notar — então
    # aqui o lado seguro de errar é rodar tudo.
    if [ -z "$changed" ]; then
      echo "code=true" >> "$GITHUB_OUTPUT"; exit 0
    fi

    # Os arquivos, nunca o exit status do grep: `-q` com `-v` não quer dizer
    # o mesmo em todo grep, e as implementações discordam justamente no PR
    # misto que esta guarda existe para acertar.
    code=$(echo "$changed" | grep -vE '^(docs/|specs/|README\.md)' || true)

    if [ -n "$code" ]; then
      echo "code=true" >> "$GITHUB_OUTPUT"
    else
      echo "code=false" >> "$GITHUB_OUTPUT"
    fi

- if: steps.touched.outputs.code == 'true'
  run: make check
```

Como calibrar:

- **Sempre roda**, mudança de documentação inclusive: `gofmt -l`. Custa segundos
  e pega arquivo desformatado, que já chegou na `main` mais de uma vez.
- **Só quando mudou algo fora de `docs/`, `specs/` e `*.md`**: `go build`,
  `go vet`, `domain-check`, unit e `-race`.
- **Só quando mudou a camada que ela exercita**: integração, que sobe
  PostgreSQL e é de longe o job mais caro.
- Na dúvida entre pular e rodar, **rode**. A guarda existe para cortar o
  desperdício óbvio, não para adivinhar.

## Check não-bloqueante não merece job próprio

Um job é uma VM nova: ele repete checkout, instalação e qualquer preparo caro
(gerar client de ORM, por exemplo) só para rodar um comando. Quando esse comando
é `continue-on-error: true` — dívida conhecida que roda para ficar visível, não
para barrar — o custo é um pipeline inteiro por PR em troca de um aviso.

Vira um **step** do job que já pagou a instalação:

```yaml
      - name: golangci-lint (não bloqueante — dívida conhecida)
        continue-on-error: true
        run: golangci-lint run ./...
```

Job separado se justifica quando ele precisa de **infra que o outro não tem**
(banco, browser, outro runtime) ou quando o paralelismo compra tempo de relógio
que importa de verdade. "Fica mais organizado" não paga a segunda VM.

## Timeout em todo job

O padrão do GitHub é **360 minutos**. Um job travado — servidor que nunca sobe,
comando esperando stdin — consome seis horas de cota sozinho, e a maior fatura
inesperada costuma ser essa, não o pipeline do dia a dia.

```yaml
jobs:
  check:
    runs-on: ubuntu-latest
    timeout-minutes: 15     # obrigatório, e perto do tempo real do job
```

## Dependências e cache

O `go test` de um módulo sem cache baixa tudo de novo e recompila a stdlib — o
que transforma um job de trinta segundos em três minutos. O `setup-go` resolve
os dois caches (módulos e build) numa linha:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version-file: go.mod   # uma fonte só para a versão; nunca duplicar aqui
    cache: true               # ~/go/pkg/mod e ~/.cache/go-build, chave no go.sum
```

- **`go-version-file: go.mod`**, nunca `go-version:` fixado no workflow. Duas
  declarações da versão divergem, e o CI passa a testar um Go que ninguém roda.
- **`go mod download` antes de compilar** deixa o erro de dependência separado
  do erro de código, e o log fica legível.
- **`go test ./...` já compila**; um step de `go build` antes dele é trabalho
  repetido, a não ser que o artefato seja o que se quer.
- **`-race` custa caro** — instrumenta tudo e chega a triplicar o tempo. Vale a
  pena mesmo assim neste projeto, mas num job só: `-race` e a rodada sem race na
  mesma invocação é desperdício.
- **Serviço de banco no job certo.** `services: postgres:` sobe container em
  **todo** job que o declara, inclusive nos que não tocam banco. Ele fica no job
  de integração e em nenhum outro.
- Artefato só `if: failure()` e com `retention-days` curto (7). Armazenamento
  também é cobrado, e relatório de run verde ninguém abre.
- **Matrix multiplica minuto por linha.** Uma versão de Go, a do `go.mod`. Mais
  de uma só entra se o projeto realmente sustenta mais de uma em produção.

## Workflow agendado

`schedule:` roda para sempre, inclusive no repo que ninguém toca há seis meses,
e o resultado não é lido por ninguém. É o vazamento clássico.

Todo cron declara, em comentário, **quem é o dono e o que ele quebra se não
rodar**. Sem essas duas linhas, não entra — e o que já está lá sem elas sai.

## Como auditar quando a fatura assustar

```bash
gh run list -R <owner>/<repo> -L 50 --json event,headBranch,conclusion,createdAt,updatedAt
```

O número verdadeiro está em **Settings → Billing → Actions**, por repositório.
Procure nesta ordem: mesmo commit rodado duas vezes (`push` + `pull_request`),
runs concorrentes na mesma branch (falta `concurrency`), PRs de uma linha em
sequência (falta agrupar), job acima de meia hora (falta `timeout-minutes`).

## Checklist ao mexer num workflow

- [ ] `concurrency` com `cancel-in-progress`
- [ ] Sem `push: main` duplicando o que o PR já testou
- [ ] `timeout-minutes` em todo job
- [ ] Nenhum job existindo só para rodar um check não-bloqueante
- [ ] `ubuntu-latest`, sem matrix sem motivo escrito
- [ ] `setup-go` com `cache: true` e `go-version-file: go.mod`
- [ ] `services: postgres` só no job de integração
- [ ] Passo caro atrás de guarda de diff, com o lado seguro rodando tudo
- [ ] Guarda por `if:` de step — nunca `paths:` num *required check*
