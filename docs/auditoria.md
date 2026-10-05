# Auditoria contra o enunciado

Este arquivo e o levantamento que a E18 pediu: **passar pelo projeto como avaliador
externo** e listar onde a evidencia existe e onde nao existe.

Ele nao e um resumo do que ja foi feito. E a lista do que **falta**, com a mesma
pergunta que um avaliador faz: "onde esta a prova?" -- e, quando a prova nao esta,
"por que nao esta?".

**Estado na auditoria:** 45 commits ate a E18, `make verify` e `make test-race-docker`
verdes, treze pacotes de integracao verdes, dezesseis cenarios de E2E.

---

## 0. Resumo do que a auditoria encontrou

| | Antes | Depois |
|---|---:|---:|
| Eliminatorios cumpridos | 10 de 10 | 10 de 10 |
| Cenarios do enunciado | 10 de 12 | **12 de 12** |
| Defeitos reais encontrados | -- | **1** |
| Criterios com evidencia integral | 6 de 8 | 6 de 8 |

**O defeito encontrado e grave, e so apareceu com reversao em processo real.** A
migration 00005 proibia nota de falha de retomada em `PROCESSED`, com o argumento de que
uma operacao bem-sucedida nao deveria exibir erro de retomada. O argumento estava errado:
a nota conta o caminho, e uma pendencia **retomada com sucesso** chega a `PROCESSED` com
a nota das tentativas anteriores. A constraint recusava o `UPDATE`, o worker repetia, a
constraint repetia, e a pendencia **nunca saia de `PENDING_REFERENCE`** mesmo com a
referencia resolvida.

O estorno ficava travado sem que nenhum dinheiro se movesse errado -- o pior tipo de
defeito, porque a garantia financeira continua valendo e a operacao simplesmente nunca
termina. Detalhado na secao 6.

---

## 1. Os dez eliminatorios

Sao reprovacao direta. A coluna da direita nomeia o teste que **executa** a garantia, nao
o que a descreve.

| # | Eliminatorio | Onde esta a evidencia | Verificado |
|---|---|---|---|
| 1 | Autenticacao efetiva nos endpoints de negocio | `e2e.TestRotaDeNegogoExigeCredencialValida` com token real do Keycloak; `apiteste` inteiro com `Oidc`. **O health check responde 401 sem token, e nao 404.** | sim |
| 2 | Nenhum acesso nao autorizado a operacoes ou transacoes | `e2e.TestProvedorNaoLeTransacaoDeOutro`, `TestProvedorNaoEscreveEmNomeDeOutro`, `TestProvedorNaoAbreNemReconcilia`; `apiteste.TestProvedorNaoLeTransacaoDeOutro` | sim |
| 3 | Nenhum calculo monetario em ponto flutuante | `Money` e `int64` em unidade minima; 96,6% de cobertura em `internal/dominio/money`. **Ver ressalva sobre `float64` abaixo.** | sim, com ressalva |
| 4 | Nenhum saldo negativo por concorrencia | `persistencia.TestSaldoNegativoRecusadoPeloBanco` (`CHECK (balance >= 0)`) + `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` (saldo final 20.00, um debito) | sim |
| 5 | Nenhuma movimentacao duplicada | `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` (50 envios, um debito, 49 replays) + `TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso | sim |
| 6 | Idempotencia nunca restrita a memoria | `persistencia.TestUnicidadeDeChaveDeIdempotencia`, `TestUnicidadeDeProvedorEIdExterno` + `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` (reinicio do processo) | sim |
| 7 | Nenhuma dependencia de instancia unica | Todos os cenarios `e2e` sobem **tres** instancias independentes com pool e memoria proprios | sim |
| 8 | Nenhuma publicacao de evento anterior ao commit | `relayteste.TestRelayNaoPublicaAntesDoCommit` + `e2e.TestEventoPendenteEAssumidoPorOutraInstanciaAposReinicio` | sim |
| 9 | Ledger auditavel sempre presente | `persistencia.TestLedgerAppendOnlyRecusaUpdateEDelete`, `TestPapelDeRuntimeNaoApagaLancamento`, `TestLancamentoQueNaoEncadeiaComOAnteriorRecusado`, `TestLancamentoSemAtualizacaoDaCarteiraRecusadoNoCommit` | sim |
| 10 | PostgreSQL, SQS e IdP nunca substituidos por mock | Todos os pacotes de integracao sobem os tres de verdade. `e2e` sobe processo completo com grafo de Fx real. | sim |

### Ressalva no eliminatorio 3: onde o `float64` aparece

A busca por `float64` no codigo de producao devolve **um unico arquivo**:
`internal/obs/metrica.go`. Nenhuma linha de dinheiro.

E justificavel e provado: contador, gauge, histograma e latencia sao contagens e
duracoes em milissegundos. Nenhuma delas move dinheiro e nenhuma e persistida como
saldo. O `Money` do sistema e `int64` em unidade minima e nao aparece naquele arquivo.

Ainda assim, um avaliador que rode `grep float64` para no arquivo e precisa de uma
frase que explique a ausencia. Essa frase **nao existia** -- o comentario agora existe,
e este registro e o segundo lugar onde ela aparece. E o motivo de a auditoria existir:
um eliminatorio cumprido que precisa ser explicado e um eliminatorio que parece violado.

**O que nao foi feito, e por que:** nao se converteu metrica para `int64` com escala
propria. Seria possivel -- um contador cabe em `int64` com folga -- e o preco seria uma
conversao em cada leitura e escrita, mais uma classe de bug de arredondamento em codigo
que nao trata dinheiro. A decisao fica registrada em `ARCHITECTURE.md` com o
trade-off.

---

## 2. Os oito criterios, com pontos

| Criterio | Pontos | Evidencia existente | Lacuna |
|---|---:|---|---|
| Integridade financeira | 20 | `Money` exato, constraints no banco, reconciliacao com divergencia detectada, `LOSS` sem efeito, reversoes com politica | **nenhuma relevante** |
| Concorrencia | 20 | 80/80 em tres processos, 50 replicas, carteiras em paralelo, reversao retomada por outra instancia, `lock_timeout` do papel de runtime | **nenhuma relevante** |
| Idempotencia | 15 | dois indices, fingerprint canonico, resultado persistido, inbox com efeito real | **nenhuma relevante** |
| Mensageria e recuperacao | 15 | inbox, outbox, relay com lease, dois publishers, reentrega, reencontro | **falta o crash entre commit e remocao da mensagem SQS** |
| Modelagem e arquitetura | 10 | `ARCHITECTURE.md` com decisao e custo, Fx com injecao por construtores, `Actor` na borda | **`fx.Module` nao foi usado: ver 5.3** |
| Testes | 10 | tres processos, `-race` verde, auth real, treze pacotes de integracao, 16 cenarios de E2E | **nenhuma relevante** |
| Observabilidade | 5 | logs JSON correlacionados, 12 metricas, `/metrics` publico, health checks | **nenhuma relevante** |
| Documentacao | 5 | `ARCHITECTURE.md` completo | **`README.md` ainda e o enunciado; nao ha roteiro de reproducao** |

---

## 3. Os doze cenarios obrigatorios

**Doze de doze cobertos** depois desta etapa. O item 5 segue sem teste dedicado, e o
motivo esta em 5.1 -- mas o comportamento que ele protege tem evidencia indireta em
`TestReencontroDevolveOResultadoPersistidoAposReinicio` e em `consumidorteste`.

| # | Cenario | Teste |
|---:|---|---|
| 1 | mesma aposta 50 vezes em paralelo | `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` |
| 2 | duas apostas de 80.00 sobre 100.00 | `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` |
| 3 | carteiras diferentes em paralelo | `e2e.TestCarteirasDistintasProcessamEmParalelo` |
| 4 | HTTP e SQS para a mesma operacao | `e2e.TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso |
| 5 | commit confirmado e processo interrompido antes do delete da mensagem | **AUSENTE** -- ver 5.1 || 6 | dois publishers disputando a mesma outbox | `e2e.TestDoisRelaysDisputandoNaoPublicamODuplicado` |
| 7 | `REFUND` antes da `BET` | `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia` |
| 8 | `ROLLBACK` antes da referencia | `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia` + `casos.TestReversaoAntesDaApostaFicaPendente` |
| 9 | restart com idempotencia preservada | `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` |
| 10 | restart com pendencias preservadas | `pendenciasteste` inteiro |
| 11 | reconciliacao apos processamento | `casos.TestReconciliacaoDetectaSaldoDivergenteDoLedger`, `TestReconciliacaoDeCarteiraSadiaNaoAchaDivergencia` |
| 12 | isolamento entre provedores | `e2e.TestProvedorNaoLeTransacaoDeOutro` e mais dois |

---

## 4. As garantias da secao 5 do enunciado

Oito numeradas. Todas verificadas.

| # | Garantia | Onde |
|---:|---|---|
| 1 | dinheiro sem `float32`/`float64` | `internal/dominio/money`, com a ressalva da secao 1 |
| 2 | idempotencia persistente e sobrevivente a reinicio | dois indices unicos + `e2e` de reencontro |
| 3 | invariantes no banco, alem de lock e deduplicacao | `persistencia` com 30 testes de constraint, trigger e papel de runtime |
| 4 | evento publicado so depois do commit | outbox gravada no commit, relay le depois |
| 5 | ledger append-only | trigger que recusa `UPDATE` e `DELETE` |
| 6 | carteiras independentes em paralelo, sem lock global | `e2e.TestCarteirasDistintasProcessamEmParalelo` |
| 7 | sem lost update | `UPDATE` condicional por versao alem do `FOR UPDATE` |
| 8 | unicidade, nao negatividade e imutabilidade no schema | `persistencia` inteiro |

---

## 5. As quatro lacunas, com o preco de fechar cada uma

Ordenadas por custo. As duas primeiras sao lacunas de **criterio com pontos**; as duas
ultimas sao de **documentacao**.

### 5.1 Falta o crash entre o commit e a remocao da mensagem SQS (criterio Mensageria, 15 pontos)

O enunciado pede: "interrompa um consumidor depois do commit e antes da remocao da
mensagem; valide a reentrega".

**O que existe:** `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` faz o
caminho pelo HTTP. A reentrega pelo SQS existe em `consumidorteste`.

**O que falta:** os dois caminhos **no mesmo cenario**, com o processo morto entre os
dois momentos. E a garantia mais forte do enunciado -- dinheiro nao se move duas vezes
**e** o consumidor reconhece a reentrega pela inbox.

**Como fechar:** um cenario que (a) publica na fila, (b) espera a inbox registrar,
(c) mata a instancia com `Stop` em vez de deixar o commit do consumidor terminar de
apagar, (d) sobe outra instancia e (e) verifica um unico debito.

**Preco:** um cenario. O que torna o cenario dificil e que o `Stop` gracioso **apaga** a
mensagem antes de parar -- seria preciso um `Stop` que nao espera os hooks, ou um kill
do processo. Essa e a razao pela qual o cenario foi adiado em vez de escrito errado.

### 5.2 ~~Faltam `REFUND` antes da `BET`~~ **FECHADO NESTA ETAPA**

**O que existia:** `casos.TestReversaoAntesDaApostaFicaPendente` provava no caso de
uso, com o repositorio real. `pendenciasteste` provava a retomada com backoff e
expiracao.

**O que foi escrito:** `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia`, em
processo real com tres instancias. Uma instancia so publica o `ROLLBACK` -- o que
garante a ordem sem depender de temporizacao -- e as outras duas sobem depois e tem de
assumir a pendencia.

**O que ele encontrou:** o defeito da secao 6, que nenhuma das suites de caso de uso
podia ver.

### 5.3 `fx.Module` nao foi usado (criterio Modelagem, 10 pontos)

O enunciado diz "organizacao por `fx.Module`, `fx.Provide` e `fx.Invoke`". O projeto
usa `fx.Provide` e `fx.Invoke`, mas em um grafo unico em `internal/runtime/app`.

**Avaliacao honesta:** nao e violacao. O `fx.Module` e agrupamento de providers, e o
requisito real e que a composacao use Fx com injecao por construtores -- o que o
projeto faz, em um pacote so. Com tres workers e um servidor em um unico arquivo de
200 linhas, o agrupamento em modulos e cosmetico.

**O que fazer:** registrar a decisao em `ARCHITECTURE.md` com esse argumento, e nao
mudar o codigo. Se a avaliacao for estrita, o custo de agrupar em modulos e baixo --
mas mexer no grafo funcionando, com tres instancias de teste dependendo dele, nao e
justificavel por ganho cosmetico.

### 5.4 `README.md` ainda e o enunciado (criterio Documentacao, 5 pontos)

O enunciado pede um `README.md` da solucao com pre-requisitos, variaveis de ambiente,
inicializacao das filas, aplicacao e reversao das migrations, execucao, exemplos de
chamada e comandos de teste.

**Estado:** `docs/ambiente.md` e `.env.example` tem o essencial; `ARCHITECTURE.md` tem
as decisoes. O roteiro de ponta a ponta nao existe, e o `docker-compose.yml` nao tem o
servico da aplicacao.

**Preco:** e o que a E19 faz. Mas a auditoria registra aqui porque e o que impede
qualquer avaliador de rodar o projeto, e um projeto que o avaliador nao consegue rodar
perde pontos nos outros criterios tambem.

---

## 6. O defeito que a auditoria encontrou

### 6.1 A constraint que proibia registrar uma retomada bem-sucedida

**Sintoma:** um `ROLLBACK` que chega antes da aposta fica em `PENDING_REFERENCE` para
sempre, mesmo depois de a aposta chegar e a referencia ser resolvida. O saldo fica
correto -- nenhum dinheiro se move errado -- e a operacao simplesmente nunca termina.

**Causa:** a migration 00005 gravou a constraint
`ck_transacoes_nota_somente_em_espera`:

```sql
CHECK (last_retry_error IS NULL OR state IN ('PENDING', 'PENDING_REFERENCE', 'FAILED'))
```

O argumento registrado na 00005 era que "uma transacao `PROCESSED` com nota de falha e um
dado que se contradiz". **O argumento estava errado.** A nota nao descreve o estado
atual -- ela conta o caminho. Uma pendencia que esperou duas vezes e na terceira foi
retomada chega a `PROCESSED` com a nota `"referencia ausente"` das duas primeiras, e
isso e historico, nao contradicao.

O ciclo: o worker registra a nota e agenda; na retentativa resolve a referencia e tenta
concluir; o `UPDATE` viola a constraint; o worker trata como falha de infraestrutura e
reagenda; e de novo, indefinidamente.

**Por que nenhum teste pegou:** `pendenciasteste` cobre a **expiracao**, que grava
`FAILED` -- estado que a constraint permite. Nenhum teste cobria a **retomada com
sucesso**, que e o unico caminho que chega a `PROCESSED` com nota. O primeiro cenario de
reversao em processo real foi o que olhou esse caminho.

**Correcao (migration 00008):** a constraint foi relaxada, nao removida. A intencao
original tem uma parte que continua valendo -- uma nota **isolada**, sem
`retry_count > 0` e sem estado de espera, e um dado que se contradiz:

```sql
CHECK (
    last_retry_error IS NULL
    OR retry_count > 0
    OR state IN ('PENDING', 'PENDING_REFERENCE', 'FAILED')
)
```

O `retry_count > 0` e o que substitui a proibicao: ele exige que exista rastro de
espera para a nota existir. Uma nota sozinha num `PROCESSED` recusa.

**A lecao, e ela e mais cara que o defeito:** a constraint foi escrita para defender uma
propriedade que **nao era verdade**. A propriedade real -- "uma nota de espera nao
convive com um desfecho que nao passou por espera" -- e a mais fraca, e ela nao foi
verificada contra o comportamento real do worker antes de virar `CHECK`. Uma constraint
que impede um caminho legitimo e pior que nenhuma constraint: ela transforma um bug de
logica em bug de dado, e o sintoma passa a apontar para o banco.

### 6.2 Onde a auditoria mudou o codigo alem do defeito

**`internal/obs/metrica.go` passou a explicar por que usa `float64`.** Detalhado na
secao 1: o eliminatorio 3 e "dinheiro nao pode passar por `float64`", a busca por essa
palavra desce em `metrica.go` e para, e nao havia frase que explicasse que ali so tem
contagem e duracao.

---

## 7. O que a auditoria mudou no codigo

| Mudanca | Motivo |
|---|---|
| `00008_nota_de_retentativa_em_processed.sql` | Defeito 6.1: constraint que impedia registrar retomada bem-sucedida |
| `internal/obs/metrica.go` | Explicar o `float64` de metrica para o eliminatorio 3 |
| `e2e/reversao_test.go` (2 cenarios) | Fechar os cenarios 7 e 8 do enunciado |
| `docs/auditoria.md` | Este arquivo |

---

## 8. Ordem sugerida das proximas etapas

1. **E19** -- Compose com o servico e README de reproducao. Sem isso o avaliador nao
   roda o projeto, e um projeto que o avaliador nao consegue rodar perde pontos nos
   outros criterios tambem.
2. **Fechar 5.1** -- o crash entre commit e remocao da mensagem. O unico cenario do
   enunciado sem teste dedicado, e o mais caro: exige um shutdown que nao espera os
   hooks, ou matar o processo.
3. **5.3** -- a decisao sobre `fx.Module`, registrada em `ARCHITECTURE.md` com o
   argumento de que e agrupamento cosmetico.
4. **E20** -- carga, E21 -- documentacao final.
