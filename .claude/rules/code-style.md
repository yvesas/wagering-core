# Regra — Estilo de código (Go)

> Enforçada por: `gofmt` e `go build`/`go vet` no hook `build-vet` (Stop),
> `make check` antes de cada commit e code review.

## Princípios

- **SOLID + Clean Code.** Função pequena, um motivo para mudar, nome que diz o
  que faz.
- **Dependência externa fica atrás de uma interface** declarada por quem a
  consome. O domínio não conhece o formato do fornecedor.
- **Payload despadronizado é normalizado na borda**, numa camada só. O resto do
  código vê o tipo do domínio.
- Reutilizar o que já existe antes de criar abstração nova. Padrão novo precisa
  de justificativa no PR — dependência nova, mais ainda.

## Go

- **`gofmt` não é negociável.** Sem discussão de estilo: a ferramenta decide.
- **Interface é declarada por quem consome, não por quem implementa.** Ela mora
  ao lado do caso de uso, em `internal/app/`, não junto do repositório que a
  satisfaz. Interface pequena, um papel — `Accept interfaces, return structs`.
- **Nada de interface "por precaução".** Uma implementação só e nenhum teste
  precisando trocá-la significa que a interface ainda não ganhou o direito de
  existir.
- **Erro é valor.** Embrulhe com `%w` para preservar a cadeia, e classifique com
  `errors.Is`/`errors.As`. Nunca `_ = err`, nunca erro engolido.
- **`panic` não representa rejeição de negócio.** Ele existe para invariante de
  programa violada — estado que só um bug produz. Regra de negócio devolve erro.
- **Erro de domínio é tipado**, com código estável, para a borda mapeá-lo em
  status HTTP sem inspecionar string.
- **`context.Context` é o primeiro parâmetro** de toda função que faz I/O, e é
  respeitado: cancelamento e prazo não são decorativos.
- **Nunca guardar `context.Context` numa struct.** Ele é por chamada.
- `internal/` para tudo que não é API pública deste módulo.
- **Concorrência:** quem inicia uma goroutine é responsável por encerrá-la. Sem
  goroutine órfã, sem `time.Sleep` como sincronização, sem canal sem dono claro.
- **Zero value útil** quando fizer sentido; quando não fizer, construtor com
  validação e campo não exportado. Não existe caminho que produza valor inválido.
- **Nada de `float32`/`float64` em código que toca dinheiro** — nem em parsing,
  cálculo, serialização, persistência ou teste.

## Comentário

- Comentário explica **por quê**, não o quê. Código que precisa de comentário
  para dizer o quê deve ser reescrito.
- Comentário de identificador exportado começa com o nome dele e é frase
  completa — é o que o `go doc` mostra.
- Cada pacote tem um `doc.go` dizendo a responsabilidade dele e o que ele **não**
  pode importar.

## Idioma

Código em inglês: identificador, comentário, `doc.go`, nome de arquivo e de
pasta. Prosa em português: `specs/`, `docs/`, `README.md` e os arquivos de
instrução. O corte é entre o que a linguagem lê e o que uma pessoa lê.

## Antes de considerar pronto

- `make check` verde: `gofmt` · `go vet` · `domain-check` · `go test` ·
  `go test -race`.
- O hook `build-vet` roda no `Stop` com a árvore suja. Se ele reclamou, o
  trabalho **não** está pronto — não é aviso, é gate.
- Teste novo que passaria também sem a mudança não conta como teste.
