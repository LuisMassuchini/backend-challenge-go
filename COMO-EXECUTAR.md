# Como executar

Guia de execucao do `wager-service`, a partir de um checkout limpo. O `README.md` da
raiz e o enunciado do desafio e fica intacto; este arquivo e a entrega da secao 15.

Cada secao aqui responde "como eu rodo isso", na ordem em que a pessoa encontra. As
decisoes **por que** estao em [`ARCHITECTURE.md`](ARCHITECTURE.md), e a matriz de
evidencias contra o enunciado esta em [`docs/auditoria.md`](docs/auditoria.md).

---

## 1. Pre-requisitos

| Item | Versao | Para que |
|---|---|---|
| Docker Engine | 29 ou superior | sobe o ambiente |
| Docker Compose | v2 ou superior (plugin `docker compose`) | le o `docker-compose.yml` |
| Go | 1.27.0 | compila e roda os testes no host |

O Go esta fixado em tres lugares que precisam concordar: `go.mod` define a linguagem, o
`Dockerfile` define a imagem, e o `Makefile` define o caminho de verificacao.

O Compose sozinho nao precisa de Go instalado -- a imagem compila dentro do container. Go
na maquina e necessario para rodar `make verify` e os testes.

No Windows, o GNU Make procura `sh.exe` para executar as receitas, e o Git for Windows
instala o shell sem coloca-lo no PATH. Sem `C:\Program Files\Git\usr\bin` no PATH,
nenhum alvo do Makefile roda.

---

## 2. Subir o ambiente

```sh
cp .env.example .env
docker compose up -d --build
```

O `.env` e opcional: o Compose tem default para cada variavel. Ele existe para que os
valores lidos pela aplicacao sejam exatamente os provisionados, e para que a
documentacao e o ambiente nao divirjam.

O `docker compose up -d` retorna **antes** do ambiente estar pronto. O sinal de que
esta pronto:

```sh
docker compose ps
```

```text
SERVICE         STATUS
keycloak        Up 2 minutes (healthy)
localstack      Up 2 minutes (healthy)
migrate         Exited (0)
postgres        Up 2 minutes (healthy)
wager-service   Up 2 minutes (healthy)
```

Quatro servicos de infraestrutura (PostgreSQL, LocalStack, Keycloak) e a aplicacao. O
quinto servico, `migrate`, tem que aparecer como `Exited (0)`: ele roda uma vez e sai.

| Servico | Endereco no host |
|---|---|
| wager-service | `http://localhost:8080` |
| PostgreSQL | `localhost:5432` |
| SQS | `http://localhost:4566` |
| Keycloak | `http://localhost:8081` |

### Por que a migration e um servico separado

Quem aplica migration tem direito de DDL e quem roda o servico nao. Se a aplicacao
aplicasse o schema na subida, ela teria de carregar o dono do banco em tempo de
execucao -- exatamente o privilegio que a migration `00004_runtime_role.sql` existe
para remover. Alem disso, tres instancias subindo juntas executariam tres migrations ao
mesmo tempo.

Por isso o `wager-service` declara `depends_on: migrate: service_completed_successfully`,
que e "espere ate o fim", e nao "espere ate o comeco". Um `service_started` iniciaria a
aplicacao junto com a migration, e a primeira conexao do processo cairia numa tabela que
ainda nao existe.

A aplicacao conecta com `wager_app`, o papel de menor privilegio criado pela migration
00004. Esse papel nao tem DELETE nem DDL, e e o que torna o ledger append-only uma
garantia do banco e nao apenas do codigo. A validacao de configuracao **recusa** um
`WAGER_POSTGRES_DSN` que aponte para o dono do schema, e a recusa acontece no start.

### Os dois enderecos do OIDC

```yaml
WAGER_OIDC_ISSUER: http://localhost:8081/realms/wager
WAGER_OIDC_URL_JWKS: http://keycloak:8080/realms/wager/protocol/openid-connect/certs
```

Sao perguntas diferentes. "O token foi emitido por este realm?" se responde comparando a
string `iss` com o Issuer, e `iss` precisa bater com o que o Keycloak anuncio -- que e o
endereco externo, `localhost:8081`. "Qual a chave publica?" exige uma chamada de rede que
sai do container, e `localhost:8081` la dentro e o proprio `wager-service`.

Sem a segunda linha o sintoma e `401 credencial_invalida` com o log mostrando `o token nao
passou na verificacao` e nada mais: a falha de rede na busca da chave volta como recusa
de token, que e o mesmo codigo de um token de verdade invalido.

---

## 3. Inicializacao das filas

As tres filas nascem provisionadas por codigo, em
`deploy/localstack/init/00-filas.sh`, executado pelo LocalStack no start.

| Fila | Papel |
|---|---|
| `wager-transactions.fifo` | entrada de `WagerTransactionRequested` |
| `wager-transactions-dlq.fifo` | cartao morto da fila de operacoes |
| `wager-events.fifo` | saida dos envelopes de evento do relay da outbox |

O destino e `init/ready.d` e nao `init/hooks`: o LocalStack 4 deixou de varrer o
diretorio antigo, e um script em `hooks` era **silenciosamente** ignorado -- o ambiente
subia sem fila e o primeiro teste de consumidor falhava com `NonExistentQueue`, sem
nenhuma pista de que o hook nem rodou.

**Ao mudar o init, recrear o container, nunca restartar:** o script so roda no start.

```sh
docker compose up -d --force-recreate localstack
```

Para ver as filas:

```sh
docker compose exec localstack awslocal sqs list-queues
```

---

## 4. Migrations

### Aplicar

Automaticamente quando o Compose sobe: o servico `migrate` roda `migrate up` e
dependencia disso e o `wager-service`. Para aplicar na mao:

```sh
make migrations-up
```

Ou, direto no container:

```sh
docker compose run --rm migrate up
```

### Reverter

Cada migration tem sua secao `-- +goose Down`. O comando reverte **uma** migration, a
ultima aplicada:

```sh
make migrations-down
docker compose run --rm migrate down
```

Reverter a `00008` remove `ck_transacoes_nota_requer_historia` e restaura
`ck_transacoes_nota_somente_em_espera`. Atencao: essa constraint antiga impede registrar
uma retomada bem-sucedida, que e um caminho legitimo -- a E18 descobriu isso. Ver
`docs/auditoria.md`, secao 6.

### Estado

```sh
make migrations-status
docker compose run --rm migrate status
```

O resultado esperado depois do `up` completo:

```text
goose: no migrations to run. current version: 8
```

Sao **oito migrations**. O detalhamento de cada uma esta em
[`docs/banco.md`](docs/banco.md).

---

## 5. Execucao da aplicacao

Pelo Compose, que e o caminho que o `docker compose up --build` do enunciado usa:

```sh
docker compose up -d --build wager-service
```

Pelo host, com a aplicacao fora do container e a infraestrutura no Compose:

```sh
make run
```

Nessa forma o `.env` precisa estar exportado no shell, porque `make run` nao le `.env` --
o Compose le, o shell nao. No PowerShell:

```powershell
$env:WAGER_POSTGRES_DSN = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"
$env:WAGER_SQS_ENDPOINT = "http://localhost:4566"
$env:WAGER_OIDC_ISSUER   = "http://localhost:8081/realms/wager"
$env:WAGER_OIDC_AUDIENCE = "wager-service"
make run
```

### Varias instancias

O Compose sobe uma. Para provar o comportamento distribuido, suba outras na mao com
portas diferentes e o mesmo banco:

```sh
WAGER_HTTP_ADDRESS=:8081 make run
WAGER_HTTP_ADDRESS=:8082 make run
```

Ou escale o servico:

```sh
docker compose up -d --scale wager-service=3
```

Com portas fixas no Compose, `--scale` falha ao fixar a porta; nesse caso remova
`ports` do servico ou suba as instancias na mao. A E2E faz isso por conta propria com
portas efemeras.

### Encerrar

```sh
docker compose down          # mantem os volumes
docker compose down -v       # apaga os volumes: recomeca do zero
```

O `SIGTERM` faz o servidor HTTP parar de aceitar requisicao nova, terminar as que ja
entraram, e so entao parar os workers. O prazo total vem de
`WAGER_HTTP_SHUTDOWN_TIMEOUT`.

---

## 6. Credenciais de teste

O realm entra por import no start, de `deploy/keycloak/wager-realm.json`. Os segredos
estao declarados no realm em vez de gerados: segredo gerado no import muda a cada
recriacao de volume, e o teste passa a falhar por causa do ambiente e nao do codigo.

| Cliente | Segredo | Pode |
|---|---|---|
| `wager-service` | `wager-service-secret` | abrir carteira, ler, reconciliar |
| `provider-a` | `provider-a-secret` | enviar operacoes externas de `provider-a` |
| `provider-b` | `provider-b-secret` | enviar operacoes externas de `provider-b` |

O bootstrap admin do Keycloak e `admin`/`admin` em `localhost:8081`.

Obter um token:

```sh
curl -s -X POST \
  http://localhost:8081/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a \
  -d client_secret=provider-a-secret
```

A identidade do provedor vem do `azp` do token, que e o proprio `client_id`. Um provedor
recebe `provider-a` e nenhum outro, inclusive em replay e em consulta.

---

## 7. Exemplos de chamadas

Os exemplos usam `sh` com `jq`, que e o que torna o `id` gerado visivel nos passos
seguintes. Sem `jq`, use o `id` que a resposta traz.

### Abrir uma carteira

O `wager-service` interno e quem abre carteira. O `id` da carteira **e gerado pelo
servico** -- o `playerId` vai no corpo e o `id` volta na resposta.

```sh
TOKEN_SERVICO=$(curl -s -X POST \
  http://localhost:8081/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=wager-service -d client_secret=wager-service-secret | jq -r .access_token)

CARTEIRA=$(curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN_SERVICO" \
  -H 'Content-Type: application/json' \
  -d '{
        "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
        "initialBalance": { "amount": "100.00", "currency": "BRL" }
      }')
echo "$CARTEIRA" | jq
WALLET_ID=$(echo "$CARTEIRA" | jq -r .id)
```

```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "100.00", "currency": "BRL" },
  "version": 1,
  "currency": "BRL"
}
```

### Enviar uma aposta

Operacao externa exige o token do **provedor**, e o `providerId` do corpo precisa bater
com o `azp` do token. Sem um dos dois a resposta e `403`, e o motivo e nomeado:
`operacao externa exige provedor` ou `provedor do comando diferente do autorizado`.

O header `Idempotency-Key` e obrigatorio.

```sh
TOKEN_PROVEDOR=$(curl -s -X POST \
  http://localhost:8081/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a -d client_secret=provider-a-secret | jq -r .access_token)

curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN_PROVEDOR" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d "{
        \"providerId\": \"provider-a\",
        \"externalTransactionId\": \"transaction-123\",
        \"playerId\": \"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1\",
        \"walletId\": \"$WALLET_ID\",
        \"roundId\": \"round-987\",
        \"gameId\": \"fortune-chimp\",
        \"kind\": \"BET\",
        \"money\": { \"amount\": \"50.00\", \"currency\": \"BRL\" }
      }" | jq
```

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "50.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

Repetir a chamada com a **mesma** chave devolve o mesmo resultado com
`idempotentReplay: true` e sem novo debito. Reusar a chave com conteudo diferente devolve
`409`.

### Reverter

Acrescente `referenceExternalTransactionId`. O valor precisa ser igual ao da operacao
referenciada, e a operacao e a referencia precisam concordar em provedor, jogador,
carteira, moeda e rodada.

```sh
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN_PROVEDOR" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:refund-123' \
  -d "{
        \"providerId\": \"provider-a\",
        \"externalTransactionId\": \"refund-123\",
        \"playerId\": \"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1\",
        \"walletId\": \"$WALLET_ID\",
        \"roundId\": \"round-987\",
        \"gameId\": \"fortune-chimp\",
        \"kind\": \"REFUND\",
        \"money\": { \"amount\": \"50.00\", \"currency\": \"BRL\" },
        \"referenceExternalTransactionId\": \"transaction-123\"
      }" | jq
```

### Ler saldo e ledger

O ledger vem em `entries`, e nao em `ledger`. A paginacao usa cursor opaco.

```sh
curl -s http://localhost:8080/wallets/$WALLET_ID \
  -H "Authorization: Bearer $TOKEN_SERVICO" | jq

curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" \
  -H "Authorization: Bearer $TOKEN_SERVICO" | jq
```

```json
{
  "entries": [
    {
      "direction": "DEBIT",
      "amount": { "amount": "50.00", "currency": "BRL" },
      "balanceBefore": { "amount": "100.00", "currency": "BRL" },
      "balanceAfter": { "amount": "50.00", "currency": "BRL" },
      "createdAt": "2026-10-04T21:18:07.283Z"
    }
  ],
  "hasMore": false
}
```

### Reconciliar

```sh
curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation \
  -H "Authorization: Bearer $TOKEN_SERVICO" | jq
```

```json
{
  "storedBalance": { "amount": "50.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "50.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

### Health checks e metricas

Publicos, sem token. O `/metrics` responde em texto do Prometheus.

```sh
curl -s http://localhost:8080/health/live | jq
curl -s http://localhost:8080/health/ready | jq
curl -s http://localhost:8080/metrics | head -20
```

---

## 8. Publicar na fila

O consumidor le `wager-transactions.fifo` e usa `data.idempotencyKey` como chave,
com deduplicacao adicional pela inbox. HTTP e SQS compartilham o mesmo caso de uso e as
mesmas garantias de idempotencia.

```sh
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-body '{
    "messageId": "msg-123",
    "type": "WagerTransactionRequested",
    "occurredAt": "2026-10-04T12:00:00.000Z",
    "data": {
      "providerId": "provider-a",
      "externalTransactionId": "transaction-123",
      "idempotencyKey": "provider-a:transaction-123",
      "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
      "walletId": "'"$WALLET_ID"'",
      "roundId": "round-987",
      "gameId": "fortune-chimp",
      "kind": "BET",
      "money": { "amount": "50.00", "currency": "BRL" }
    }
  }' \
  --message-group-id provider-a \
  --message-deduplication-id msg-123
```

Os dois argumentos do final sao obrigatorios numa FIFO e nao sao decoration:

- `--message-group-id` e a chave de **particao**: mensagens do mesmo grupo sao entregues
  em ordem e nunca em paralelo. Duas operacoes da mesma carteira tem de ser do mesmo
  grupo, senao a ordem vai embora.
- `--message-deduplication-id` e por **mensagem**, dentro da janela de cinco minutos.
  Duas mensagens do mesmo grupo com a mesma chave fazem o broker **descartar a segunda**,
  e o sintoma engana: a mensagem nunca chega e o teste falha dizendo que a inbox nao
  registrou.

O relay da outbox publica em `wager-events.fifo`, com envelope de evento
(`eventId`, `eventType`, `aggregateId`, `correlationId`, `occurredAt`, `version`, `data`).
Nao ha consumidor de demonstracao dessa fila: ela existe para provar a publicacao, e o
que se observa nela e o `eventId` estavel entre republicacoes.

---

## 9. Comandos de teste

### Gate minimo, sem dependencia externa

```sh
make verify        # ASCII, gofmt, vet, build e testes de unidade
go test ./...
go vet ./...
```

`make verify` e o que tem que estar verde antes de qualquer commit. Rodar `gofmt -l .` e
`go vet ./...` na mao antes dele e o mesmo trabalho com passo a mais.

### Detector de corrida

```sh
make test-race-docker
```

Esse e o portao oficial de `-race`, e nao um atalho opcional: o detector exige cgo e um
compilador C, e uma maquina Windows sem `gcc` e sem `clang` falha com `-race requires
cgo`. A imagem oficial do Go tem gcc, entao o alvo roda o detector em qualquer maquina.
Com toolchain local completo:

```sh
make test-race
```

### Integracao

A integracao usa **containers reais** -- PostgreSQL, SQS e Keycloak -- e a tag
`integration`, que existe para que `go test ./...` continue sendo um portao rapido e sem
dependencia.

```sh
docker compose up -d
$env:WAGER_TEST_POSTGRES_DSN="postgres://wager:wager@localhost:5432/wager?sslmode=disable"   # PowerShell
make test-integration
```

No `sh`:

```sh
export WAGER_TEST_POSTGRES_DSN="postgres://wager:wager@localhost:5432/wager?sslmode=disable"
make test-integration
```

O `-p 1` do alvo nao e preferencia: os pacotes compartilham o mesmo banco e cada um
limpa as tabelas no inicio. Em paralelo, um pacote apaga o dado que o outro esta usando,
e a falha aparece em um teste que passou sozinho.

| Pacote | Tempo | Depende de container |
|---|---|---|
| `apiteste`, `casos`, `ciclo`, `obsteste`, `oidc`, `pendenciasteste`, `persistencia`, `relayteste`, `repositorios`, `e2e` | segundos a ~2,5 min | PostgreSQL, SQS, Keycloak |
| `consumidorteste` | ~5 min | PostgreSQL, SQS, Keycloak |

`consumidorteste` e `e2e` demoram porque **esperam a fila**: tem backoff e visibility
timeout reais, e o tempo e do broker, nao do codigo.

### O servico do Compose atrapalha a integracao

**Rode a integracao com o `wager-service` do Compose parado.** Os testes de integracao
publicam na mesma `wager-transactions.fifo` que o container consome, e o consumidor do
Compose disputa a mensagem com o processo do teste.

O sintoma e enganoso e vale descrever porque ele parece defeito de concorrencia:

```text
--- FAIL: TestVariasPendenciasSaoRetomadasNoMesmoCiclo
    pendencias restantes e 1, esperado 0
    saldo e 990.00, esperado 1000.00: as tres apostas foram estornadas
--- FAIL: TestDuasInstanciasNaoRetomamOMesmaPendencia
    Limpa: ERROR: deadlock detected (SQLSTATE 40P01)
```

Uma pendencia "nao retomada" e um `deadlock` na limpeza soam a bug de lock. Sao o
consumidor do container consumindo a mensagem do teste: quem processou foi outro processo,
e quem tentou limpar as tabelas disputou o lock com ele.

```sh
docker compose stop wager-service
make test-integration
docker compose start wager-service
```

**A E2E nao sofre disso**, porque ela sobe os proprios tres processos `fx.App` em portas
efemeras e nao depende do servico do Compose -- e ela **precisa** subir o Compose para ter
o PostgreSQL, o SQS e o Keycloak no ar. `migrate`, `postgres`, `localstack` e `keycloak`
podem ficar de pe durante a integracao; so `wager-service` atrapalha.

### Carga

O relatorio completo, com ambiente, metodologia e resultados medidos, esta em
[`docs/carga.md`](docs/carga.md).

```sh
docker compose up -d --build
make carga
make carga-metricas
```

O `k6` roda **dentro da rede do Compose**, falando com `wager-service:8080`. Nao e
detalhe: um `k6` na maquina alcancando `localhost:8080` atravessa o Docker Desktop, e a
latencia medida seria a do proxy do Docker, e nao a do servico.

Os parametros sao variaveis de ambiente:

```sh
make carga CARGA_THREADS=50 CARGA_CARTEIRAS=10 CARGA_DURACAO=120s
```

O pool de carteiras pequeno e deliberado: e a contensao pelo lock da carteira que o
`lock_timeout` existe para exercitar, e ela so aparece quando duas requisicoes disputam o
mesmo dinheiro.

O `make carga` exige o `wager-service` de pe. O contrario vale para a integracao, que
precisa dele **parado** para nao competir pela fila.

### Rodrigues de integracao

```sh
# Sobe o ambiente completo primeiro
docker compose up -d

# so a autenticacao contra o Keycloak
make test-integration-oidc

# as 16 cenas de E2E, com saida por teste
go test -tags=integration -p 1 -count=1 -v -timeout 900s ./tests/integration/e2e/

# um cenario especifico
go test -tags=integration -p 1 -count=1 -v -timeout 600s \
  -run 'ReversaoAntesDaAposta|DisputaDuasApostas' ./tests/integration/e2e/
```

A E2E sobe ela propria tres processos `fx.App` com portas efemeras, entao ela **nao**
depende do servico `wager-service` do Compose estar de pe. Isso e deliberado: um teste que
depende do ambiente ja levantado mede o ambiente, e nao o codigo.

### Simulacao de falha

```sh
# fila para o cartao morto depois das tentativas
docker compose stop localstack      # o consumidor volta a fila em vez de perder a mensagem
docker compose start localstack

# parar a aplicacao no meio
docker compose stop wager-service
docker compose start wager-service
```

O que cada parada prova esta em `tests/integration/e2e/recuperacao_test.go`, e o
reprocessamento depois da queda e garantido pelo `visibility timeout` de 60s: enquanto o
processo esta fora, a mensagem fica invisivel para os demais e volta depois.

---

## 10. Quando o ambiente nao sobe

| Sintoma | Causa provavel | O que fazer |
|---|---|---|
| `wager-service` fica em `Restarting` e o log mostra `WAGER_POSTGRES_DSN ausente` | o `migrate` nao rodou | `docker compose logs migrate` |
| `401 credencial_invalida` em toda chamada autenticada | falta `WAGER_OIDC_URL_JWKS` apontando para `keycloak:8080` | ver secao 2 |
| `unhealthy` no Keycloak | realm invalido | `docker compose logs keycloak` mostra o erro de import |
| `NonExistentQueue` no consumidor | o init nao rodou | `docker compose up -d --force-recreate localstack` |
| porta 5432 ocupada | outro PostgreSQL na maquina | `WAGER_POSTGRES_PORT=5433` no `.env` |
| porta 8081 ocupada | outro servico | `WAGER_KEYCLOAK_PORT=8082` no `.env` |
| `connection refused` no PostgreSQL logo apos o `up` | o Compose ainda nao passou o `start_period` | esperar o healthcheck |
| `erro_interno` com `violates foreign key constraint "wager_transactions_wallet_id_fkey"` | o `walletId` do corpo nao e o `id` devolvido na abertura | usar `$WALLET_ID` da resposta |
| `403 nao_autorizado: operacao externa exige provedor` | token do `wager-service` em operacao externa | usar token de `provider-a` |
| `403 provedor_divergente` | `providerId` do corpo diferente do `azp` do token | alinhar os dois |
| pendencia "nao retomada" ou `deadlock` na limpeza, so nos testes | o `wager-service` do Compose consumindo a fila dos testes | `docker compose stop wager-service` |

---

## 11. Mapa dos arquivos

| Arquivo | Para que |
|---|---|
| `ARCHITECTURE.md` | decisoes com por que e custo |
| `docs/carga.md` | relatorio de carga: ambiente, metodologia e resultado medido |
| `docs/auditoria.md` | matriz de evidencia contra o enunciado, e as lacunas |
| `docs/ambiente.md` | detalhe do ambiente, credenciais e diagnostico |
| `docs/banco.md` | as oito migrations, uma a uma |
| `docs/challenge.md` | copia do enunciado |
| `docs/convencoes.md` | convencoes de codigo do projeto |
| `.env.example` | variaveis com valores locais de exemplo |
| `deploy/keycloak/wager-realm.json` | realm, clientes e escopos |
| `deploy/localstack/init/00-filas.sh` | filas FIFO e politica de redrive |
