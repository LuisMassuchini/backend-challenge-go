# Relatorio de carga

Medicao do `wager-service` sob concorrencia. E o relatorio que o enunciado pede como
diferencial: comando reproduzivel, ambiente, metodologia, throughput, p50/p95/p99, erros,
conflitos e atraso da outbox.

O script e `tests/carga/carga.js`, a extracao de quantis e `cmd/metricas`, e a discussao
de por que os quantis sao **faixas** e nao numeros esta na secao 7.

---

## 1. Como rodar

```sh
docker compose up -d --build

docker run --rm --network wager-service_default \
  -v "$PWD/tests/carga:/carga:ro" \
  -e K6_BASE_URL=http://wager-service:8080 \
  -e CARGA_THREADS=30 \
  -e CARGA_CARTEIRAS=20 \
  -e CARGA_DURACAO=60s \
  grafana/k6:0.54.0 run /carga/carga.js

go run ./cmd/metricas http://localhost:8080/metrics
```

Ou, pelo Makefile:

```sh
make carga          # sobe o container do k6 com os valores acima
make carga-metricas # le o /metrics e imprime as faixas
```

O k6 roda **dentro da rede do Compose** e fala com `wager-service:8080`. Nao e
detalhe: um `k6` na maquina do desenvolvedor alcancando `localhost:8080` atravessa o
Docker Desktop, e a latencia medida seria a do proxy do Docker, nao a do servico.

**O `wager-service` precisa estar de pe, e o `wager-service` atrapalha a integracao.**
O inverso tambem vale: se o servico do Compose estiver rodando durante a carga, ele
consome a mesma fila de operacoes e muda o resultado. Para a carga ele e o alvo; para a
integracao e o problema.

### Variaveis

| Variavel | Padrao | O que muda |
|---|---|---|
| `CARGA_THREADS` | 30 | quantas requisicoes simultaneas |
| `CARGA_CARTEIRAS` | 20 | tamanho do pool de dinheiro disputado |
| `CARGA_DURACAO` | `60s` | duracao do patamar |
| `CARGA_SEMENTE` | instante da execucao | isola as chaves entre rodadas |
| `CARGA_DEBUG` | nao | imprime as respostas que nao tem reenvio |

---

## 2. Ambiente da medicao

| Item | Valor |
|---|---|
| Sistema | Windows 11 Pro |
| CPU | AMD Ryzen 5 5500, 12 nucleos logicos |
| Memoria | 8 GB |
| Docker Engine | 29.8.1 |
| Go | 1.27.0 (dentro da imagem do container da aplicacao) |
| PostgreSQL | 17.6-alpine, container |
| SQS | LocalStack 4.4.0, container |
| IdP | Keycloak 26.3.3, container |

Consumo durante a rodada, medido com `docker stats` no fim:

| Container | CPU | Memoria |
|---|---:|---:|
| `wager-service` | 32% | 15 MiB |
| `postgres` | 16% | 472 MiB |
| `localstack` | 54% | 681 MiB |
| `keycloak` | 4% | 1,9 GB |

**Uma instancia da aplicacao.** O Compose fixa a porta 8080, e `--scale` falha ao
compartilhar porta publicada. Medir com tres instancias exige tirar `ports` do servico, ou
subir instancias adicionais na mao com `WAGER_HTTP_ADDRESS` diferente -- as duas mudam a
topologia e por isso nao foram usadas aqui.

---

## 3. Metodologia

**O pool de carteiras e pequeno de proposito: 20 carteiras para 30 threads.** Com muitas
carteiras, cada requisicao tranca e destranca um lock que ninguem disputa, e o resultado
seria alto e sem informacao. A contencao que o `lock_timeout` existe para exercitar so
aparece quando duas requisicoes competem pelo mesmo dinheiro.

**A carteira abre antes da medicao.** Criar carteira dentro da carga poluiria a leitura com
a operacao mais pesada do sistema -- abertura, `OPENING`, credito e dois eventos no mesmo
commit. A carga mede aposta.

**O saldo inicial e alto (100000.00).** Com 1000.00 as carteiras secavam em um minuto e a
rodada terminava com 40% de recusa por saldo insuficiente. Isso mede esgotamento de
dinheiro, nao contencao.

**A mistura de operacoes e 70% `BET`, 10% `LOSS`, 10% `WIN`, 5% `REFUND`, 5% `ROLLBACK`.**
A reversao so vai com referencia quando existe uma aposta processada para desfazer, que e
o caminho feliz da reversao.

**O cliente reenvia em 503, ate tres vezes, com a mesma chave.** E o que um provedor real
faz, e e seguro por construcao: a segunda entrega encontra a transacao ja confirmada e
devolve o resultado sem mover dinheiro de novo. O numero de reenvios e o que separa "o
sistema sofre contencao" de "o sistema perde transacao".

---

## 4. Resultado

Rodada de 85 segundos: 15s de subida, 60s de patamar em 30 threads, 10s de descida.

### Throughput

| Medida | Valor |
|---|---:|
| requisicoes HTTP | 37.190 |
| requisicoes por segundo | **436,6** |
| operacoes de negocio | 15.251 |
| operacoes por segundo | **179,1** |

A diferenca entre 436 requisicoes e 179 operacoes por segundo e uma leitura a cada cinco
iteracoes, mais as repeticoes de 503.

### Latencia vista pelo cliente (k6, `http_req_duration`)

| Medida | Valor |
|---|---:|
| media | 14,44 ms |
| mediana | 3,56 ms |
| p95 | 30,14 ms |
| maximo | 1,04 s |

O maximo de um segundo e o `lock_timeout` do papel de runtime: a transacao espera o lock
da carteira por ate um segundo e recebe 503. E o valor que o comando `lock_timeout` define
e a razao pela qual ele existe.

### Latencia da operacao, medida pelo servidor

Esta e a que importa. `wager_operacao_duracao_ms` mede do inicio da operacao ate o commit
e inclui a espera pelo lock da carteira, coisa que a latencia do cliente nao ve.

| Serie | n | media | p50 | p95 | p99 |
|---|---:|---:|---|---|---|
| `PROCESSED` | 13.653 | 6,14 ms | abaixo de 5 ms | [5 ms, 10 ms) | [10 ms, 25 ms) |
| `REJECTED` | 1.549 | 37,37 ms | [25 ms, 50 ms) | [25 ms, 50 ms) | [50 ms, 100 ms) |

**`REJECTED` e mais lenta que `PROCESSED`, e o motivo e o que a torna legivel.** Uma
recusa de saldo nao deveria ser cara: ela pode descobrir que nao ha dinheiro sem trancar
a carteira. Mas o caminho de hoje e o mesmo, com `SELECT ... FOR UPDATE` antes da regra.
A diferenca de seis vezes entre as duas series mede exatamente o custo de uma transacao
que tranca o dinheiro so para recusar. **E uma melhoria conhecida e nao feita**: a regra
precisa do saldo, e o saldo esta protegido pelo lock.
recusa de saldo nao deveria ser cara: ela pode descobrir que nao ha dinheiro sem trancar a
carteira. Mas o caminho de hoje e o mesmo, com `SELECT ... FOR UPDATE` antes da regra.

| Serie | n | media | p50 | p95 | p99 |
|---|---:|---:|---|---|---|
| `POST` | 15.540 | 29,55 ms | abaixo de 5 ms | [25 ms, 50 ms) | [1000 ms, 3000 ms) |

O p99 em [1000 ms, 3000 ms) e o `lock_timeout` aparecendo na borda. A requisicao passou
um segundo esperando o lock e voltou 503.

### Desfechos

| Desfecho | Quantidade | Fracao |
|---|---:|---:|
| `PROCESSED` | 13.653 | 89,5% |
| `REJECTED` | 1.549 | 10,2% |

### Erros e conflitos

| Categoria | Quantidade | Comentario |
|---|---:|---|
| respostas sem recuperacao (400/401/403/409/500) | **0** | o limiar do comando e zero |
| reenvios por 503 que resolveram | 269 | o cliente repetiu e deu certo |
| 503 que esgotaram as 3 tentativas | 45 | contencao acima do que o cliente absorve |
| conflitos de lock no servidor | 314 | `wager_conflitos_lock_total` |

**Zero resposta sem recuperacao.** As 314 disputas de lock viraram 503 e nenhuma delas
virou 500 depois da correcao de contrato da secao 6.

### Atraso da outbox

| Medida | Valor |
|---|---:|
| `wager_outbox_atraso_segundos` (maximo) | **76,07 s** |

Eventos publicados durante a rodada: 15.183 `WagerTransactionProcessed`, 1.709
`WagerTransactionRejected`, 13.482 `WalletBalanceChanged`.

**Os 76 segundos sao uma limitacao conhecida e medida, e nao um efeito do desenho.** O
relay publica em lote de 50 e sequencialmente, e o LocalStack leva cerca de 2,8 ms por
publicacao. Sao ~357 eventos por segundo publicados, que e exatamente o teto do relay
neste ambiente. O atraso cresce porque o produtor -- a carga -- e mais rapido que o relay
consegue publicar.

Duas coisas separariam: publicar o lote em paralelo, e um relay por agregado. Nenhuma das
duas foi feita, e o numero acima e o que elas resolveriam.

### Conclusao

O sistema **nao duplica dinheiro sob concorrencia** e **nao devolve 500 por contencao**.
A 179 operacoes por segundo com 30 threads em 20 carteiras, 10% de recusas de saldo e
314 disputas de lock, o saldo conferido contra o ledger bate, e nenhuma resposta ficou sem
caminho de recuperacao.

O ponto fraco medido nao e correcao financeira: e a **publicacao da outbox**, que nao
acompanha a taxa de producao neste ambiente.

---

## 5. O que a carga encontrou

Dois defeitos reais, ambos invisiveis para a suite de integracao.

### 5.1 Lock timeout devolvia 500 em vez de 503

A 152 requisicoes por segundo, 34 requisicoes receberam 500 com
`canceling statement due to lock timeout (SQLSTATE 55P03)` no corpo.

O `classificarErro` traduzia deadline de contexto para 503, mas nao o conflito de lock --
`pg.ErrConflitoDeVersao` existia e nao era classificado, entao caia no 500 padrao.

**O custo era alto.** `lock_timeout` existe para transformar contencao em falha rapida e
repetivel. Com 500, o provedor recebe o codigo que significa "o erro e meu" e
tipicamente **nao** repete -- a transacao se perdia sem nunca ter sido aplicada. Com 503
e um corpo que diz que repetir e seguro, a garantia de idempotencia faz o resto.

Por que a integracao nao pegou: para esbarrar no `lock_timeout` de um segundo e preciso
que mais requisicoes disputem a mesma carteira do que o pool de conexoes absorve. A E2E
faz exatamente isso com 50 envios, mas no mesmo instante -- e a contencao de uma rajada e
muito menor que a de uma carga sustentada.

### 5.2 `created_at` do lancamento media a ordem errada

Duas requisicoes do periodo de 25 segundos a 152 por segundo receberam 500 com
`lancamento nao encadeia com o anterior: saldo anterior 87400, anterior 87000`.

A trigger `trg_ledger_coerente_na_carteira` e **deferida** e ordena a cadeia por
`(created_at, id)`. O instante do lancamento era medido na **entrada do caso de uso**,
antes de `SELECT ... FOR UPDATE`.

A consequence: o carimbo refletia a ordem de **chegada**, e nao a ordem de
**serializacao**. Uma requisicao que entrava depois e consequentava o lock antes -- por
estar esperando conexao do pool -- gravava um `created_at` menor, e a trigger procurava o
predecessor errado.

Nao era dinheiro errado: a constraint recusava a transacao inteira e a operacao se
perdia. E a garantia financeira seguia valendo, que e o pior tipo de defeito.

A correcao mede o instante **depois** do lock. Quem grava depois leu o saldo depois, entao
o carimbo e posterior. A unica ressalva e o relogio andar para tras entre as medicoes
(ajuste de NTP), e nesse caso a trigger recusa em vez de aceitar um lancamento fora de
ordem -- falha barulhenta e o desfecho preferivel.

**Depois da correcao, nenhuma ocorrencia em tres rodadas de carga.**

---

## 6. Limites desta medicao

1. **Uma instancia da aplicacao.** O Compose fixa a porta 8080. Ver secao 2.
2. **PostgreSQL e LocalStack no mesmo host, com o k6.** A contencao de disco e de CPU e
   compartilhada, e parte do que aparece como latencia e do ambiente e nao do servico.
3. **A carga vai so pelo HTTP.** O caminho SQS nao foi medido sob volume. O relay e o
   consumidor tem testes de integracao com fila real, mas nao com carga.
4. **O atraso da outbox e dominado pelo LocalStack.** Ver secao 4.
5. **Sem execucao longa.** 85 segundos nao medem vazamento de recurso nem degradacao.
6. **O `lock_timeout` de um segundo aparece como maximo de latencia.** Em um ambiente com
   rede real esse numero seria outro.

---

## 7. Por que os quantis sao faixas

O relatorio diz que o p95 esta **em [5 ms, 10 ms)** e nao que e **7,2 ms**.

Um histograma do Prometheus guarda contagem por intervalo, e nao as observacoes. A linha
`wager_operacao_duracao_ms_bucket{le="10"} N` diz que N observacoes foram menores que dez
milissegundos. Entre 5 e 10 milissegundos, a distribuicao real e desconhecida: pode ter
cinco em 5,1 e cinco em 9,9, ou quatro em 5,0 e seis em 9,5.

A interpolacao linear dentro do bucket -- o que `promtool` e a maioria das ferramentas
fazem -- assume distribuicao uniforme e devolve "7,2 ms". O numero e inventado com duas
casas decimais, e a precisao aparente e falsa.

`internal/promtexto` devolve a faixa e nada mais. E uma escolha deliberada: **um numero
que ninguem mediu e pior do que uma faixa que todo mundo pode verificar.** Quem quiser um
numero unico precisa de `histogram_quantile` no Prometheus -- que faz a mesma interpolacao
-- ou de outro histograma com mais buckets.

O `cmd/metricas` tambem imprime a media, que e exata: e a soma dividida pela contagem, e
nao depende de bucket.
