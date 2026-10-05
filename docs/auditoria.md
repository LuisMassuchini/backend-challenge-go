# Auditoria contra o enunciado

Este arquivo e o levantamento que a E18 pediu: **passar pelo projeto como avaliador
externo** e listar onde a evidencia existe e onde nao existe.

Ele nao e um resumo do que ja foi feito. E a lista do que **falta**, com a mesma
pergunta que um avaliador faz: "onde esta a prova?" -- e, quando a prova nao esta,
"por que nao esta?".

**Estado na auditoria:** 49 commits ate a E20, `make verify` e `make test-race-docker`
verdes, treze pacotes de integracao verdes, dezesseis cenarios de E2E e dez de consumidor
contra a fila real.

---

## 0. Resumo do que a auditoria encontrou

| | Antes | Depois |
|---|---:|---:|
| Eliminatorios cumpridos | 10 de 10 | 10 de 10 |
| Cenarios do enunciado | 10 de 12 | **12 de 12** |
| Defeitos reais encontrados | -- | **3** |
| Criterios com evidencia integral | 6 de 8 | **8 de 8** |

**Os tres defeitos, e o que eles tinham em comum.**

O **primeiro** so apareceu com reversao em processo real. A migration 00005 proibia nota de
falha de retomada em `PROCESSED`, com o argumento de que uma operacao bem-sucedida nao
deveria exibir erro de retomada. O argumento estava errado: a nota conta o caminho, e uma
pendencia **retomada com sucesso** chega a `PROCESSED` com a nota das tentativas
anteriores. O `ROLLBACK` ficava travado em `PENDING_REFERENCE` para sempre, mesmo com a
referencia resolvida. Secao 6.1.

O **segundo** e mais silencioso. `Inbox.Concluir` nunca era chamado em producao, entao
`completed_at` ficava sempre nulo: a inbox nao distinguia "tratado" de "o processo morreu
no meio". O dinheiro nunca esteve errado -- a idempotencia nao depende dessa coluna -- mas
faltava uma garantia que o enunciado pede explicitamente. Secao 6.2.

O **terceiro** nao e bug, e uma decisao que ninguem tomou: a coluna `attempts` da inbox
documenta "conta as reentregas" e nunca e incrementada. Secao 6.3.

**O que eles tem em comum:** os tres estavam em lugares que **ninguem testava**. A
constraint nova nunca exercitou o caminho da retomada com sucesso. O `UPDATE` da inbox
nunca foi chamado, so o `INSERT`. A coluna `attempts` nao tem writer nem reader. **Nenhum
dos tres apareceria em revisao de codigo, e os tres apareceram assim que alguem olhou o
estado depois de um caminho feliz.**

O padrao que a auditoria registra: um teste que verifica o **estado** depois do caminho
feliz encontra defeitos que um teste que verifica o **resultado** nao encontra. O saldo
estava certo em todos os casos; o que nao existia era a distincao entre "tratado" e
"orfao".

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
| Mensageria e recuperacao | 15 | inbox, outbox, relay com lease, dois publishers, reentrega depois do commit, reencontro | **nenhuma relevante** |
| Modelagem e arquitetura | 10 | `ARCHITECTURE.md` com decisao e custo, Fx com injecao por construtores, `Actor` na borda | **`fx.Module` nao foi usado: ver 5.3** |
| Testes | 10 | tres processos, `-race` verde, auth real, treze pacotes de integracao, 16 cenarios de E2E | **nenhuma relevante** |
| Observabilidade | 5 | logs JSON correlacionados, 12 metricas, `/metrics` publico, health checks | **nenhuma relevante** |
| Documentacao | 5 | `ARCHITECTURE.md` completo, `COMO-EXECUTAR.md` com roteiro validado contra o Compose | **nenhuma relevante** |

---

## 3. Os doze cenarios obrigatorios

**Doze de doze cobertos, cada um com teste dedicado que o executa.** O item 5 -- a
reentrega depois do commit -- foi o ultimo a fechar, na E20, e ver 5.1.

| # | Cenario | Teste |
|---:|---|---|
| 1 | mesma aposta 50 vezes em paralelo | `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` |
| 2 | duas apostas de 80.00 sobre 100.00 | `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` |
| 3 | carteiras diferentes em paralelo | `e2e.TestCarteirasDistintasProcessamEmParalelo` |
| 4 | HTTP e SQS para a mesma operacao | `e2e.TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso |
| 5 | commit confirmado e processo interrompido antes do delete da mensagem | `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` |
| 6 | dois publishers disputando a mesma outbox | `e2e.TestDoisRelaysDisputandoNaoPublicamODuplicado` |
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

### 5.1 ~~Falta o crash entre commit e remocao~~ **FECHADO NESTA ETAPA (E20)**

O enunciado pede: "interrompa um consumidor depois do commit e antes da remocao da
mensagem; validate a reentrega".

**Por que estava em aberto.** O `Stop` gracioso cancela o contexto e **espera** o
goroutine terminar (`internal/runtime/app/consumidor.go`), e `tratar` ainda sai cedo
quando o contexto ja acabou. A janela real entre o commit (`internal/pg/unidade.go:204`)
e o `DeleteMessage` (`internal/consumidor/consumidor.go`) tem poucas linhas de log e
metrica no meio -- microsssegundos. Matar o processo nesse instante seria sorte.

**Como ficou resolvido.** O worker passou a depender de uma interface `Fila`, no mesmo
desenho que `outbox.Publicador` ja usava, e o teste injeta a falha exatamente onde ela
acontece. O estado duravel que sobra e o mesmo do crash: commit confirmado, mensagem
presente na fila.

**O que o teste prova, e o que ele nao finge.** Ele nao derruba processo nenhum, e o
doc comment do teste diz isso. A cadeia que ele exercita:

1. a primeira instancia consome e confirma o commit (saldo 100 -> 75);
2. a remocao da mensagem falha, e a mensagem continua na fila;
3. a primeira instancia e **parada**, para que quem reentregue nao seja ela;
4. quando o timeout de visibilidade expira, a segunda instancia recebe a mensagem;
5. a segunda reconhece a operacao como processada, devolve o resultado persistido e apaga.

O passo 3 e o que faz o teste valer. Sem ele, a propria primeira instancia reentregaria a
mensagem aos 60s e o teste passaria sem provar que **outro processo** retoma o trabalho.

**O defeito que ele encontrou -- e grave.** Ver secao 6.3: `Inbox.Concluir` nunca era
chamado em producao, entao `completed_at` ficava sempre nulo. O passo 5 do cenario so pode
ser verificado com a inbox concluida, entao o teste do cenario 5 foi justamente o que
descobriu que a inbox nunca foi concluida.

Custa cerca de 60s, que e o timeout de visibilidade da fila, esperando de verdade.

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

### 5.4 ~~Falta roteiro de reproducao~~ **FECHADO NESTA ETAPA (E19)**

O enunciado pede um `README.md` da solucao com pre-requisitos, variaveis de ambiente,
inicializacao das filas, aplicacao e reversao das migrations, execucao, exemplos de
chamada e comandos de teste.

**O que foi escrito:** `COMO-EXECUTAR.md`, na raiz, em onze secoes na ordem em que a
pessoa encontra. E o `docker-compose.yml` ganhou os dois servicos que faltavam.

O `README.md` continua sendo o enunciado, sem alteracao: o portao de ASCII o exclui de
proposito (ele tem acentuacao por ser texto do desafio), e sobrescrever o enunciado com a
solucao destruiria a referencia que a auditoria compara.

**O roteiro foi escrito contra o Compose rodando, nao contra o codigo.** Cada exemplo de
chamada desta secao foi executado contra o container de verdade, e tres coisas sairam
disso que a leitura do codigo nao teria dito:

- **A carteira devolve `id`, e nao `walletId`.** O `playerId` vai no corpo e o `id` e
  gerado. Usar o `walletId` enviado na abertura em uma operacao seguinte devolve
  `erro_interno` com `violates foreign key constraint "wager_transactions_wallet_id_fkey"`,
  que parece bug e e uso incorreto da API.
- **O `Idempotency-Key` e obrigatorio e o `providerId` do corpo tem de bater com o `azp`
  do token.** As duas recusas sao `403` com motivo nomeado, e o roteiro mostra qual token
  usar em cada operacao.
- **O `WAGER_OIDC_URL_JWKS` precisa de endereco interno.** Com o issuer em
  `localhost:8081` e sem essa variavel, toda chamada autenticada responde `401
  credencial_invalida` -- a falha de rede na busca da chave volta com o codigo de um
  token invalido de verdade. E o motivo de `WAGER_OIDC_URL_JWKS` existir separada do
  issuer.

**A imagem final e distroless, e isso custou um subcomando.** O `healthcheck` do Compose
precisa de um `CMD`, e nao ha curl, nem wget, nem shell na imagem. Em vez de um script,
o proprio binario ganhou o subcomando `healthcheck`, que consulta o proprio
`/health/live`. A alternativa -- `CMD-SHELL` com curl -- daria `exec: curl: not found`
no primeiro start, e o sintoma (servico `unhealthy` logo apos subir, sem erro visivel)
aponta para a imagem, nao para o Compose.

**O `migrate` e servico separado e depende de `service_completed_successfully`.** Quem
aplica migration tem DDL e quem roda o servico nao; misturar obrigaria a aplicacao a
carregar o dono do banco em tempo de execucao, que e o privilegio que a migration 00004
existe para remover. Esperar "ate o comeco" em vez de "ate o fim" faria a primeira
conexao do processo cair numa tabela que ainda nao existe.

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

### 6.2 A inbox nunca era concluida

**Sintoma:** `completed_at` de `inbox_messages` era **sempre nulo** em producao. Quem
consultasse a inbox nao distinguia "esta mensagem foi tratada" de "o processo morreu no
meio" -- as duas coisas tinham o mesmo formato, e a segunda e exatamente a que o
operador precisa identificar.

**Causa:** `RepositorioInbox.Concluir` existe, esta testado no nivel do repositorio
(`tests/integration/repositorios/mensageria_test.go`), e **nenhum codigo de producao
chamava**. O `INSERT` do registro acontecia; o `UPDATE` da conclusao, nunca. O
enunciado pede justamente as duas coisas -- "o registro da inbox **e a conclusao duravel**
do tratamento devem compartilhar a transacao SQL" -- e so a primeira estava feita.

**Por que a idempotencia funcionava mesmo assim.** Porque ela nao depende de
`completed_at`. A garantia de dinheiro vem da chave de idempotencia e do fingerprint, e
nao da coluna. Ou seja: **o sistema nunca esteve errado sobre dinheiro**, e mesmo assim
tinha uma garantia a menos do que o enunciado pede, sem nenhum teste que reclamasse. O
defeito era de evidencia e de operabilidade, nao de saldo.

**Como foi encontrado.** Pelo teste do cenario 5. O passo final do cenario so pode ser
verificado com a inbox concluida, entao a primeira versao do teste reprovou com "a inbox
tem 0 concluidas e 1 registrada". Nenhuma suite anterior verificava `completed_at`, porque
o `INSERT` funcionava e o `UPDATE` nao era exercitado.

**Correcao:** `concluirInbox` em `internal/app/processar_operacao.go`, chamada depois de
`confirmar` e dentro da mesma unidade. Fora de `confirmar` porque `confirmar` cuida do
desfecho da OPERACAO e o inbox pertence a ENTREGA da mensagem -- a mesma operacao chega
por HTTP e nao tem mensagem nenhuma para concluir. E depois de `confirmar` porque os tres
desfechos dele -- `PROCESSED`, `REJECTED` e `PENDING_REFERENCE` -- sao duraveis e nos tres
a mensagem foi tratada; fechar so no caminho feliz deixaria a recusa de saldo com a linha
da inbox pendente para sempre. O caminho do replay registra e conclui na mesma unidade,
porque deixar `completed_at` nulo ali seria afirmar o oposto do que aconteceu.

### 6.3 A coluna `attempts` da inbox e morta

`attempts` foi criada com o comentario "conta as reentregas" (`00001_schema_inicial.sql`),
mas `Inbox.Registrar` usa `ON CONFLICT DO NOTHING` e **nunca a incrementa**. O valor fica
sempre em 1, que e o default da coluna.

Nao foi corrigido nesta etapa, e a decisao e registrar em vez de adivinhar: `attempts`
precisa de semantica que o codigo ainda nao tem. Incrementar so quando o INSERT conflita
exige um `RETURNING` ou um `UPDATE` separado, e incrementar em toda entrega -- inclusive na
primeira -- mudaria o significado da coluna. E o dado ja existe em outro lugar: o
`ApproximateReceiveCount` da propria fila. Duas fontes para o mesmo fato, uma delas morta.

Enquanto a coluna estiver morta, **ninguem deve le-la** para decidir se uma mensagem
precisa de reprocessamento: ela diz que houve uma entrega quando houve duas.

### 6.4 Onde a auditoria mudou o codigo alem dos defeitos

**`internal/obs/metrica.go` passou a explicar por que usa `float64`.** Detalhado na
secao 1: o eliminatorio 3 e "dinheiro nao pode passar por `float64`", a busca por essa
palavra desce em `metrica.go` e para, e nao havia frase que explicasse que ali so tem
contagem e duracao.

---

## 7. O que mudou no codigo

| Mudanca | Motivo |
|---|---|
| `00008_nota_de_retentativa_em_processed.sql` | Defeito 6.1: constraint que impedia registrar retomada bem-sucedida |
| `internal/obs/metrica.go` | Explicar o `float64` de metrica para o eliminatorio 3 |
| `e2e/reversao_test.go` (2 cenarios) | Fechar os cenarios 7 e 8 do enunciado |
| `docker-compose.yml`: servicos `migrate` e `wager-service` | Fechar 5.4: sem o servico, o avaliador nao roda o projeto |
| `Dockerfile`: binario `migrate` na mesma imagem | O `migrate` roda em container separado e nao pode depender de volume de codigo |
| `internal/runtime/healthcheck/` + subcomando `healthcheck` | Imagem distroless nao tem curl, nem wget, nem shell para o probe |
| `COMO-EXECUTAR.md` | Fechar 5.4: roteiro de ponta a ponta, validado contra o Compose |
| `internal/consumidor/consumidor.go`: interface `Fila` | Tornar observavel a janela entre o commit e o apagamento |
| `internal/app/processar_operacao.go`: `concluirInbox` | Defeito 6.2: `completed_at` nunca era preenchido em producao |
| `internal/sqs/sqs.go` e `internal/runtime/app/consumidor.go`: remocao de `EsperaMaxima` | Campo morto que prometia controlar o long polling e nao controlava |
| `consumidorteste/consumidor_test.go`: cenario 5 | Fechar 5.1: reentrega depois do commit |
| `tests/integration/ciclo/ciclo_test.go`: comentario orfao removido | O arquivo terminava no meio de um doc comment sem funcao |
| `docs/auditoria.md` | Este arquivo |

---

## 8. Ordem sugerida das proximas etapas

**As quatro lacunas de 5.1 a 5.4 estao fechadas.** Restam duas perguntas e duas etapas:

1. **5.3** -- a decisao sobre `fx.Module`, ja registrada em `ARCHITECTURE.md` com o
   argumento de que e agrupamento cosmetico; falta so decidir se o avaliador e estrito
   o bastante para reprovar por isso.
2. **A coluna `attempts` da inbox** -- ver 6.3. Decisao a tomar, nao bug a corrigir: ou
   ganha semantica de reentrega, ou sai do schema. A segunda opcao e mais honesta ate
   que alguem precise do dado, porque `ApproximateReceiveCount` da fila ja conta entregas.
3. **E20** -- carga com k6.
4. **E21** -- documentacao final.

**Ao rodar a integracao, pare o `wager-service` do Compose**: ele consome a mesma fila dos
testes e faz `pendenciasteste` falhar com "pendencia nao retomada" e `deadlock`, que imita
bug de concorrencia sem ser um.

### O que a E20 custou em correcao, e nao em escrita

O cenario 5 foi escrito para provar que a reentrega nao move dinheiro duas vezes, e o
primeiro resultado foi `a inbox tem 0 concluidas e 1 registrada`. **A garantia que o teste
ia verificar dependia de uma coluna que ninguem preenchia.** Nenhum dos vinte e um
cenarios anteriores verificava `completed_at`, porque o `INSERT` funcionava e o `UPDATE`
nao era exercitado por ninguem.

Vale o padrao: um teste que verifica o **estado** depois de um caminho feliz encontra
defeitos que um teste que verifica o **resultado** nao encontra. O saldo estava certo em
todo mundo; o que nao existia era a distincao entre "tratado" e "orfao".

### O que a E19 custou em correcao, e nao em escrita

O roteiro foi escrito depois de rodar o Compose, e tres coisas apareceram que a leitura do
codigo nao apontava: o `id` gerado da carteira, o `Idempotency-Key` obrigatorio com
`providerId` batendo no `azp`, e o JWKS precisando de endereco interno. **Nenhuma das tres
e defeito** -- sao contratos que existem e funcionam. Mas uma pessoa seguindo um roteiro
escrito so pela leitura do codigo erraria as tres, e a primeira delas devolve um
`erro_interno` com nome de constraint, que parece defeito.

E o padrao que vale para as proximas etapas: **documentacao de execucao se escreve
rodando, nao escrevendo.** Um exemplo de `curl` que nunca foi executado e uma afirmacao
sobre o que o programa faz, e as duas coisas divergem.
