# Regra — Segredos e .env

> Enforçada por: `.gitignore` (`.env*`), hook `guard-env` (PreToolUse em
> **Edit/Write e Bash**), `deny: Read(./**/.env*)` no `settings.json` e code review.
>
> O hook cobre as duas portas: escrever (`echo ... > .env`, `tee`, `cp`, `sed -i`)
> e **ler** (`cat`, `head`, `grep`, `source`) — porque `deny: Read(./.env)` só
> alcança a ferramenta Read, não o shell, e um `cat .env` põe o segredo no
> contexto do agente. Só inspeção de metadado passa (`ls`, `stat`, `test`,
> `git check-ignore`). Quando precisar mesmo mexer no `.env`, rode você mesmo
> com `! <comando>`.

## Nunca

- Commitar `.env`, `.env.local`, `.env.prod` ou qualquer arquivo com valor real.
- Colar apikey, token, SAS, connection string, JWT ou senha em código, teste,
  fixture, log, commit, PR, issue ou mensagem.
- Logar dado pessoal (CPF, e-mail, nome, dado de saúde). Log estruturado, sem PII.

## Sempre

- Segredo via variável de ambiente ou Secret Manager. Em dev, `.env` local
  gitignorado.
- `.env.example` versionado com **só o nome** das variáveis e um comentário do
  que cada uma é — nunca o valor.
- Teste de integração usa **fixture sintética**. Teste com credencial real fica
  *gated* por env var (`*_LIVE_TEST`) e nunca roda no CI.
- Sample capturado de sistema real é **sanitizado antes de salvar**.
- Credencial de terceiro guardada pelo sistema é criptografada; o agente nunca
  vê a chave em claro.

## Se um segredo vazar para o histórico

Rotacionar o segredo **primeiro** — reescrever o histórico não o invalida.
Depois limpar o histórico e avisar quem mais tem clone do repo.
