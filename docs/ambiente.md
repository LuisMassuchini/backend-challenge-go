# Ambiente local

O ambiente local do `wager-service` tem tres servicos de verdade: PostgreSQL,
SQS em LocalStack e Keycloak como IdP. Nenhum deles e substituto de teste. O
enunciado proibe substituir PostgreSQL, SQS e IdP por mock, e a razao e
tecnica: lock por carteira, constraint de saldo nao negativa, inbox, FIFO,
redrive e validacao de token existem no comportamento do servidor, nao no de uma
biblioteca Go. Um teste contra mock so prova que o mock funciona.

## Pre-requisitos

| Item | Versao | Para que |
|---|---|---|
| Docker Engine | 29 ou superior | sobe o ambiente |
| Docker Compose | v2 ou superior (o plugin `docker compose`) | le o `docker-compose.yml` |
| Go | 1.27.0 | compila e roda a aplicacao no host |

A versao do Go tambem esta fixada no `Dockerfile` e no `go.mod`. Os tres precisam
concordar: o `go.mod` define a linguagem, o `Dockerfile` define a imagem e o
Makefile define o caminho de verificacao.

No Windows, o GNU Make procura `sh.exe` para executar as receitas, e o Git for
Windows instala o shell sem coloca-lo no PATH. Sem
`C:\Program Files\Git\usr\bin` no PATH, nenhum alvo do Makefile roda.

## Subir o ambiente

```sh
cp .env.example .env
docker compose up -d
```

O `.env` e opcional: o Compose tem default para cada variavel. Ele existe para
que os valores lidos pela aplicacao sejam exatamente os provisionados, e para
que a documentacao e o ambiente nao divirjam.

## Readiness

`docker compose up -d` retorna antes de o ambiente estar pronto. O sinal de que
esta pronto e o health de cada servico:

```sh
docker compose ps
```

```text
wager-service-keycloak-1     Up 2 minutes (healthy)
wager-service-localstack-1   Up 2 minutes (healthy)
wager-service-postgres-1     Up 2 minutes (healthy)
```

A espera real, quando o ambiente sobe do zero:

| Servico | Endereco de espera |
|---|---|
| PostgreSQL | `localhost:5432` |
| SQS | `http://localhost:4566/_localstack/health` |
| Keycloak | `http://localhost:8081/health/ready` com `KC_HEALTH_ENABLED=true` |

O healthcheck do Keycloak importa o realm, e nao apenas pergunta se a porta
responde: o `/health/ready` so fica `UP` depois que o `--import-realm` termina.
Um readiness que responde antes do import faz o primeiro teste de autenticacao
falhar por race, e o erro parece de configuracao de cliente.

## Servicos

### PostgreSQL

`postgres:17.6-alpine`, com `TZ` e `PGTZ` em UTC. O fuso do cluster e fixado
porque `timestamptz` depende do fuso do servidor quando o INSERT nao traz fuso
explicito: com o padrao do container, o mesmo INSERT grava valores diferentes em
maquinas diferentes.

O healthcheck consulta o cluster de verdade, e nao apenas `pg_isready`. Um
cluster em `recovering` responde "aceitando conexoes", e nesse instante a
primeira migration falha com um erro que parece defeito de codigo.

Os dados ficam em volume nomeado. Para recomecar do zero:

```sh
docker compose down -v
```

### SQS em LocalStack

`localstack/localstack:4.4.0`, somente com o servico `sqs`. Habilitar servico que
nao e usado atrasa o start e ocupa memoria sem agregar nada ao desafio.

As filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo`, com a politica
de redrive, sao provisionadas por codigo nos hooks de init, em
`deploy/localstack/init`. Nao e para criar fila no console: um ambiente que
depende de clique nao e reproduzivel, e o teste de integracao passaria a
depender de estado que ninguem versionou.

### Keycloak

`quay.io/keycloak/keycloak:26.3.3` em `start-dev`, com o realm importado de
`deploy/keycloak/wager-realm.json`.

O realm traz tres clientes em `client_credentials`, e o escopo de cada um e o que
distingue o papel:

| Cliente | Segredo | Escopos no token | Pode |
|---|---|---|---|
| `provider-a` | `provider-a-secret` | `wager:operacoes` | enviar operacoes externas de `provider-a` |
| `provider-b` | `provider-b-secret` | `wager:operacoes` | enviar operacoes externas de `provider-b` |
| `wager-service` | `wager-service-secret` | `wager:carteira:abertura`, `wager:reconciliacao` | abrir carteira e reconciliar, e nao enviar operacoes externas |

O segredo de cada cliente esta declarado no realm em vez de gerado. Secredo
gerado no import muda a cada recriacao de volume, e o teste de integracao passa a
falhar por causa do ambiente e nao do codigo.

A identidade do provedor vem do `azp` do token, que e o proprio `client_id`. Um
provedor recebe `provider-a` e nenhum outro, inclusive em replay e em consulta.
A decisao e documentada em `ARCHITECTURE.md`.

Para obter um token de teste:

```sh
curl -s -X POST \
  http://localhost:8081/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a \
  -d client_secret=provider-a-secret
```

## Credenciais

Nenhum valor versionado e segredo real. A senha do bootstrap admin do Keycloak e
as credenciais do PostgreSQL e do LocalStack sao de ambiente local de teste, e
estao no `.env.example` para que qualquer pessoa reproduza o ambiente sem
perguntar nada. O que nao entra no repositorio e chave de ambiente que aponte
para um servico real.

## Quando o ambiente nao sobe

| Sintoma | Causa provavel | O que fazer |
|---|---|---|
| `docker compose ps` mostra `unhealthy` no Keycloak | realm invalido | `docker compose logs keycloak` mostra o erro de import do JSON |
| `connection refused` no PostgreSQL logo apos o `up` | o Compose ainda nao terminou o `start_period` | esperar o healthcheck; o `start_period` do PostgreSQL e de 10s |
| porta 5432 ocupada | outra instancia de PostgreSQL na maquina | `WAGER_POSTGRES_PORT=5433` no `.env` |
| porta 8081 ocupada | outro servico, ou o proprio Keycloak de uma execucao anterior | `WAGER_KEYCLOAK_PORT=8082` no `.env` |
| o container do LocalStack reinicia em loop | hook de init com erro de sintaxe | `docker compose logs localstack` |
