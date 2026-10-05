# Banco de dados

O schema do `wager-service` e um arquivo versionado: migrations em
`internal/pg/migrations`, aplicadas com `goose` e embutidas no binario. Nenhum passo
deste guia depende de clique no console do PostgreSQL.

O que o banco garante, e o que o dominio garante em Go, esta em
[`ARCHITECTURE.md`](../ARCHITECTURE.md).

## Pre-requisitos

O ambiente local ja sobe o PostgreSQL:

```sh
docker compose up -d postgres
```

Para as tabelas e as migrations deste guia:

```sh
export WAGER_POSTGRES_DSN="postgres://wager:wager@localhost:5432/wager?sslmode=disable"
```

## Aplicar

```sh
make migrations-up
```

Equivale a `go run ./cmd/migrate up`. Idempotente: rodar de novo nao faz nada.

## Reverter

```sh
make migrations-down
```

Reverte **a ultima** migration aplicada, nao todas. O alvo existe porque o caminho
mais rapido para perder dado e reverter o schema inteiro em um ambiente que ja tem
informacao; quem precisa do schema vazio cria um banco proprio.

## Estado

```sh
make migrations-status
```

## Como o schema e dividido

| Migration | O que acrescenta |
|---|---|
| `00001_schema_inicial` | `wallets`, `wager_transactions`, `wallet_ledger_entries`, `inbox_messages`, `outbox_events` e os indices de leitura |
| `00002_unicidade_e_saldo` | unicidades, saldo nao negativo, coerencia entre estado e dados, politica de valor por tipo, origem interna contra externa |
| `00003_ledger_append_only` | trigger de coerencia por linha, trigger append-only e constraint trigger deferido de coerencia entre ledger e carteira |
| `00004_runtime_role` | papel `wager_app` de menor privilegio e limites de sessao |

A divisao e por responsabilidade, e nao por data. Uma migration que cria tabela e
ja cria trigger fica com o trigger preso nela, e a reversao passa a depender das
duas. Aqui cada arquivo faz uma coisa e reverte sozinho.

## Papeis

| Papel | Quem usa | O que pode |
|---|---|---|
| `wager` (dono) | `make migrations-up`, testes de integracao | tudo, inclusive DDL |
| `wager_app` (runtime) | a aplicacao | `SELECT`, `INSERT`, `UPDATE` nas cinco tabelas |

O papel de runtime **nao tem `DELETE`, nao tem `CREATE` e nao tem DDL**. Nao e
zelo: e o que faz do append-only do ledger uma garantia do banco e nao apenas do
codigo. Se um `DELETE` acidental em `wallet_ledger_entries` passa, e porque a
aplicacao esta rodando com o papel errado -- e isso aparece no primeiro teste de
privilegio, e nao no incidente.

As senhas dos dois papeis sao de ambiente local e ficam no `.env.example` e na
migration. Nao sao segredo real.

### Por que dois papeis e nao um

Com um papel so, a aplicacao precisa do dono do schema em tempo de execucao para
que o `migrate up` funcione, e ai o `DELETE` acidental passa. Com dois papeis, o
`goose` roda como dono e a aplicacao roda com o que ela precisa.

## Limites de sessao

Configurados no papel de runtime:

| Limite | Valor | Por que |
|---|---|---|
| `statement_timeout` | `3s` | a transacao financeira faz poucas operacoes; segurar mais e segurar lock |
| `lock_timeout` | `1s` | com varias instancias disputando a mesma carteira, contecao tem de virar erro e nao espera |
| `idle_in_transaction_session_timeout` | `15s` | fecha a sessao que abriu transacao e esqueceu, que e o tipo de conexao que segura lock sem ninguem olhar |

O `lock_timeout` e o que garante que a disputa das duas apostas de 80.00 sobre
100.00 termine em decisao -- uma processada, uma rejeitada -- e nao em duas
esperas. Sem ele, o segundo escritor espera, e a espera e indistinguivel de travamento
para quem olha de fora.

O erro que o `lock_timeout` produz e transitorio por natureza: o caso de uso devolve
indisponibilidade, o provedor reenvia, e a idempotencia garante que o reenvio nao
movimente dinheiro duas vezes. O codigo de falha `ROLLBACK_SEM_SALDO` e
`BET_SEM_SALDO`, nao, sao regra de negocio e nao passam por aqui.

## Testes de integracao

```sh
make test-integration
```

Ou, com outro banco:

```sh
WAGER_TEST_POSTGRES_DSN="postgres://wager:wager@localhost:5432/wager?sslmode=disable" \
  go test -tags=integration -count=1 ./tests/integration/...
```

A suite viola cada constraint de proposito e exige que o banco recuse, incluindo
as de privilegio, que rodam como `wager_app`. Os erros sao conferidos por codigo
SQLSTATE e nunca por substring de mensagem: a mensagem do PostgreSQL muda entre
versoes, e um teste que casa com texto quebra na proxima versao sem que nada tenha
mudado no sistema.

Os testes precisam de `wager_app` existir, porque a migration 00004 o cria. Se a
suite falhar com `papel de runtime nao conecta`, rode `make migrations-up` antes.

## Quando algo falha

| Sintoma | Causa | O que fazer |
|---|---|---|
| `papel de runtime nao conecta` | migration 00004 nao aplicada | `make migrations-up` |
| `relation "wallets" does not exist` | schema nao migrado | `make migrations-up` |
| `permission denied for table` em teste de privilegio | o papel foi revogado a mao | `make migrations-down && make migrations-up` |
| migration falha no meio | DDL aplicado parcialmente | `make migrations-status`, depois `make migrations-down` e `make migrations-up` |
