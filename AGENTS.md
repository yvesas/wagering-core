# Instruções para agentes

Serviço distribuído de carteira e ledger financeiro em Go.

**Leia [`CLAUDE.md`](./CLAUDE.md) primeiro.**

Quatro coisas que não se negociam:

1. **O domínio não conhece infraestrutura.** `internal/domain/` importa a
   biblioteca padrão e nada mais. Verifique com o comando do `CLAUDE.md`.
2. **Dinheiro nunca passa por ponto flutuante** — nem em parsing, nem em
   cálculo, nem em serialização, nem em persistência.
3. **Inglês em tudo que é código** — identificadores, comentários, `doc.go`,
   nomes de pasta e arquivo, `Makefile`, compose, `.env.example` e mensagem de
   commit. Português fica em `specs/`, `docs/` e `README.md`.
4. **Sem atribuição de IA** em commit, PR ou comentário: nada de
   `Co-Authored-By`, "Generated with" ou assinatura de ferramenta. O hook
   `commit-msg` rejeita; não contorne com `--no-verify`.

As invariantes que definem se o sistema está certo estão em
`specs/project/REQUIREMENTS.md`, no fim do arquivo. Teste verde não substitui
nenhuma delas.
