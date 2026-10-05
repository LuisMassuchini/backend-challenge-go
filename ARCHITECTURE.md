# Arquitetura

Este arquivo registra as decisoes tecnicas com trade-off. Ele nao e um resumo do
codigo: e o lugar em que se explica **por que** cada escolha foi feita e o que ela
custa.

O enunciado original, que nao muda, esta em [`docs/challenge.md`](challenge.md).

As secoes sao acrescentadas na ordem em que a decisao e fechada, e nao na ordem
em que o codigo e lido. Uma decisao registrada tarde e uma decisao que ninguem
encontra.

---

## Observabilidade

**Decisao de produto, nao de arquitetura: o log correlacionado vem antes das metricas.**
O plano original da E16 punha metricas depois do log, e a ordem foi invertida a pedido
do usuario, para que o projeto fosse entregue mais rapido. E uma escolha legitima e
com razao: o log correlacionado e o que ajuda a diagnosticar problema de verdade --
o relay que republica em laco, o consumidor que reentrega -- e esses problemas
nasceram na E13 e na E15, que empacotaram codigo que so existe para ser objeto de
depuracao. Ter o log pronto ainda na E16 e o que permite depurar a E17 sem etapa extra.

O preco e conhecido e fica escrito: o log e codificado contra o que existe hoje. Um
ponto de log novo pode nao pegar a correlacao enquanto os pontos antigos continuarem
sem ela. A alternativa -- adiar para depois da E17 -- custaria etapas inteiras de
depuracao sem correlacao por cima de codigo que existe para ser depurado.

**O log carrega os cinco identificadores do enunciado mais o `eventId`.**
`correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`, e o
`eventId` porque o relay nao conhece a operacao: ele conhece o evento, e o `eventId` e
o identificador que sobrevive a republicacao e pelo qual o operador acompanha um
evento que falha ao sair.

**A correlacao e anexada uma vez, na borda, e viaja no `context.Context`.** Todo log
posterior le o valor com `obs.De(ctx)` sem que a camada passe o identificador adiante.
Um log que recebe a correlacao como parametro obrigatorio em cada funcao e um log que
perde a correlacao no primeiro `go` que esquece o parametro.

**`comCorrelacao` roda antes de `cronometrar`, e a ordem nao e um detalhe.** A
composicao e `externa(interna(proximo))`, entao `cronometrar` so recebe o `r` que
`comCorrelacao` ja modificou. Com a ordem antiga, o log de requisicao saia sem
correlacao em todas as linhas -- e isso nao apareceu em nenhum teste anterior, porque
ninguem tinha procurado pela correlacao no log.

**Campo ausente e diferente de campo vazio.** Um `transactionId: ""` em toda linha de
quem nao tem transacao polui a busca e faz o operador desconfiar do campo.

**Dinheiro nao entra no log.** Nenhum valor monetario, formatted ou em unidades
minimas, aparece nas linhas do caminho da requisicao. O que substitui o valor e o campo
`movimento`, que diz "houve debito" sem dizer quanto. A unica excecao e a divergencia
da reconciliacao, que grava os dois saldos em centavos -- e a justificativa e que a
reconciliacao e o alerta que existe justamente para divergir, e um alerta que nao diz
quanto divergiu obriga quem o recebe a abrir o banco.

**Identificador externo e truncado em 256 caracteres.** O volume de log tem de crescer
com o que o servico fez e nao com o que o cliente mandou.

**Erro vai sempre na chave `erro`.** Um agregador que indexa por chave encontra toda
falha no mesmo campo, e nao em `err`, `error` e `motivo`, que e como o log cresce
quando cada chamador inventa o nome.

**A divergencia de reconciliacao sai como erro.** As duas outras respostas do
reconciliador -- convergente e carteira sem lancamento -- sao Info ou silencio.

### Metricas

**Implementacao propria do formato de exposicao, sem `prometheus/client_golang`.** A
decisao foi do usuario entre as duas opcoes, e o motivo cabe numa frase: o projeto ja
recusou ORM e `sqlc` com razao registrada, e uma arvore de dependencias grande para
gerar texto e o mesmo tipo de decisao. O que o formato pede sao contadores, gauges e
histogramas com buckets -- cerca de duzentas linhas com um `RWMutex`. O preco esta
escrito: nao ha exemplares, nao ha `push`, e a agregacao entre processos nao existe.

**Nenhum identificador pode ser rotulo, e a lista e fechada em codigo.**
`nomesDeRotuloProibidos` recusa `walletId`, `playerId`, `transactionId`, `providerId`,
`messageId`, `eventId` e `correlationId`. O motivo e cardinalidade, e ele e serio: uma
serie por carteira faz o numero de series crescer com o numero de clientes, e o
Prometheus cai de forma silenciosa -- com um `/metrics` que responde 200 e nao carrega
mais nada. O que nao cabe em rotulo fica no log, que foi feito para guardar identidade.

**`/metrics` e publico, e essa decisao so e defensavel por causa da anterior.** Um
endpoint aberto que mostra volume de operacao por estado nao expoe dado de cliente; um
endpoint aberto que mostra volume por carteira expoe. O teste
`TestMetricasNaoExpoemIdentificadorDeCliente` confirma a promessa em vez de afirmar
ela, e ele e a razao de a decisao ser aceitavel em vez de descuidada.

**Rotulo com cardinalidade sem limite e recusado em vez de truncado.** Truncar o nome do
rotulo produziria uma serie que une carteiras diferentes, que e pior que nao ter a
serie: o painel mostraria um numero que ninguem pode interpretar.

**`/metrics` sem registro responde 404, e nao 200 com corpo vazio.** As duas respostas
sao indistinguiveis para quem scrapeia, e "sem dado" e "zero" sao coisas diferentes
para quem le.

**Contador nao tem metodo para subtrair, e valor negativo e ignorado.** Contador que
desce e contador quebrado: o painel mostraria menos operacoes do que aconteceram, e o
operador acreditaria. O que precisa descer e gauge, e existe.

**A exposicao e ordenada e estavel entre chamadas.** Duas chamadas iguais precisam dar
o mesmo texto, porque e o diff entre dois scrapes que revela uma metrica que sumiu -- e
ordem aleatoria transformaria isso em impossivel de distinguir de uma serie nova.

**O atraso da outbox e medido do `occurred_at` ate a confirmacao.** Medir a partir da
gravacao do registro daria sempre proximo de zero, que e o mesmo que nao medir.

**Um registro, nao um por componente.** O caso de uso, o relay, o consumidor e o
servidor HTTP medem no mesmo `Registro`, e sao os valores que `/metrics` expoe. Um
registro por componente daria quatro visoes que nenhum painel consegue junir.

**A duplicia que vale registrar:** o consumidor repete `MAX_RECEIVE` da politica de
redrive do broker, porque o `receive count` e a unica fonte que ele tem e a politica nao
vem na mensagem. O sintoma de a duplicia ficar errada e uma metrica de cartao morto que
conta na margem -- nem zero quando deveria, nem o valor da fila quando nao.

---

## Dinheiro

**Decisao:** `Money` e um value object imutavel com `int64` em unidade minima e
escala fixa de duas casas, e a moeda ISO 4217 vem sempre junto.

**Por que `int64` e nao `NUMERIC`:** o enunciado proibe `float32` e `float64` em
qualquer etapa, e o erro classico do `NUMERIC` no PostgreSQL e o driver devolver
string. Com `BIGINT` em unidade minima, o valor gravado e o valor lido, sem
conversao, e a constraint de saldo nao negativo e um `CHECK` sobre inteiro.

**Escala fixa em duas casas, e nao por moeda:** o contrato externo manda
`"25.00"`, e uma escala por moeda permitiria JPY sem casas e USD com duas. Cada
combinacao seria um caso especial no schema, no hash de idempotencia e no
contrato JSON. O preco e um teto de 92233720368547758.07 por operacao, que nenhum
saldo real se aproxima.

**Parsing estrito:** a gramatica aceita apenas `[-] digitos+ . digitos{1,2}`.
Entrada fora dessa forma e recusada, nunca arredondada. Um arredondamento
silencioso muda dinheiro sem ninguem ver. `1.000` e classificado como escala
excedente e nao como separador de milhar: com ponto como separador decimal, sao
tres casas, e adivinhar que o autor quis dizer `1000` seria aceitar em silencio
uma precisao que ele nao enviou.

**Magnitude em unsigned no parsing:** o menor valor do sistema, `-92233720368547758.08`,
nao seria alcancavel se a magnitude fosse convertida direto em `int64`. Ele existe
porque e o valor que a negacao precisa recusar, e um valor que o sistema declara
aceitar e nao pode ler seria um limiteMentiroso.

**Overflow explicito:** `Add`, `Sub`, `Neg` e o proprio parsing devolvem
`ErrOverflow`. A soma sem guarda ja devolvia `-92233720368547758.08` sem erro, e um
saldo negativo silencioso e pior que operacao recusada: passa como numero valido
para o banco e para o log.

**Limite documentado:** `Money.Decimal()` monta a magnitude em `uint64` e e o
unico lugar do sistema inteiro que precisa conhecer `MinInt64`.

---

## Identificadores

**Decisao:** todo identificador e um UUID v7, com tipo proprio `Identificador`.

**Por que tipo proprio e nao `string`:** string crua aceita `transacao-123` e
UUID com espaco no fim, que so falham na constraint do banco -- e o erro aparece
longe da origem. Comparar com string vazia tambem nao pega o UUID zerado, que e um
valor valido que ninguem deveria usar.

**Por que v7 e nao v4:** v7 e ordenado por tempo. A carteira e a tabela com mais
escrita do sistema, e chave primaria de UUID v4 em tabela alta e um ponto de
contencao por linha. O custo e que o identificador revela a ordem de criacao, o
que e aceitavel para este sistema e nao seria para um identificador de sessao.

---

## Concorrencia

**Decisao:** lock pessimista por carteira, com `SELECT ... FOR UPDATE` na linha da
carteira, e atualizacao condicional por versao.

**Por que por carteira e nao global:** o enunciado proibe lock global. A
coordenacao por carteira e o grano que contem a dispute real -- duas operacoes na
mesma carteira -- e deixa carteiras diferentes em paralelo, que e o outro
requisito.

**Por que tambem versao:** o lock protege a leitura; a versao protege a escrita
condicional. Quem escreve com `WHERE version = $n` e recebe zero linhas atualizadas
sabe que alguem mexeu na carteira entre a leitura e a escrita, e pode repetir em
vez de sobrescrever. E a garantia contra lost update quando o lock falha ou quando
duas instancias operam em pools diferentes.

**A versacao da carteira sobe em mudanca efetiva de saldo.** Uma operacao sem
efeito -- LOSS -- nao altera saldo, nao produz lancamento e nao sobe a versao. Se
subisse, a linha seria reescrita sem motivo e o update condicional acusaria
conflito sem que houvesse dinheiro em jogo.

**A garantia final e no banco:** `CHECK (balance >= 0)`, unicidade de
`(player_id, currency)`, unicidade de `(wallet_id, transaction_id)` no ledger e
trigger que proibe `UPDATE`/`DELETE` em lancamento. O banco e a autoridade; o lock
e otimizacao.

---

## Idempotencia

**Decisao:** dois indices unicos -- `UNIQUE (idempotency_key)` e
`UNIQUE (provider_id, external_transaction_id)` -- e um hash canonico dos campos de
negocio.

**Por que dois indices:** cada um cobre uma realidade diferente. A chave impede
reprocessar a mesma requisicao; o par provedor e id externo impede que a mesma
operacao financeira aparece com duas chaves diferentes, que e o jeito que uma
duplicidade entra quando o cliente muda o esquema da chave.

**Por que hash e nao apenas a chave:** mesma chave com conteudo diferente e
conflito, nao replay. O hash e calculado sobre JSON canonico com chaves ordenadas,
excluindo a chave de idempotencia e os metadados de transporte. Normalizacao
qualquer -- escala, ordem de campo, representacao de moeda -- acontece **antes** do
hash, e e documentada, para que HTTP e SQS produzam o mesmo hash da mesma
operacao.

**O resultado original e persistido.** Replay devolve o saldo observado no
reprocessamento original, e nao o saldo atual: recalcular devolveria a resposta de
hoje para uma operacao de ontem.

### A corrida da idempotencia: por que `ON CONFLICT DO NOTHING`

Este foi o defeito mais caro do projeto, e ele **so apareceu com tres processos**.

A resolucao de idempotencia tem tres passos em sequencia: procurar a chave, procurar o
par provedor e identificador externo, e entao gravar. Entre a busca e a gravacao
existe uma janela, e com tres instancias independentes a janela e real: A e B passam
as duas buscas vendo a chave como nova, A grava, e B chega no `INSERT` e leva
violacao do indice unico.

**O que o codigo devolvia nessa hora:** o erro do `INSERT`, traduzido para
`ErrConflitoDeChave`, que a borda traduz para 409. O provedor recebia conflito numa
operacao que ele acabara de repetir sem mudar um byte do conteudo. A operacao nao
duplicava, o ledger estava certo, e mesmo assim o provedor ficava proibido de repetir
-- que e o eliminatorio "movimentacao duplicada" pela porta do lado oposto: o dinheiro
nao move duas vezes, mas o cliente nunca consegue concluir a aposta.

**Por que savepoint nao resolvia:** no PostgreSQL, um `INSERT` que viola indice unico
**aborta a transacao inteira**, e depois dele todo comando responde `current
transaction is aborted`. Nenhum savepoint evita isso: o savepoint protege contra erro
de *comando*, nao contra o estado de erro que o comando deixa. Tentar tratar o conflito
com savepoint chegou a `25P02` em quatro de cinquenta envios -- exatamente os que
perderam a corrida.

**A solucao:** `InserirSeNova` usa `ON CONFLICT DO NOTHING`, que nao aborta nada e
devolve zero linhas. Zero linhas e o conflito, uma linha e o caminho normal, e quem
chama distinguish os dois. O perdedor da corrida entao **rele o registro vencedor** e
compara o hash com o do comando: hash igual e reentrega e devolve o resultado
persistido; hash diferente e conflito de verdade e devolve 409.

O silencio do `DO NOTHING` nao esconde o conflito -- e a contagem de linhas que o
revela. E a razao de o `Inserir` original continuar sendo `INSERT` comum: ali conflito
e erro que o chamador precisa sentir, e transformar o unico `DO NOTHING` do
idempotencia em algo silencioso seria trocar um defeito conhecido por um pior.

---

## Transacoes e maquina de estados

**Decisao:** cinco estados -- `PENDING`, `PENDING_REFERENCE`, `PROCESSED`,
`REJECTED`, `FAILED` -- com tres terminais, e toda transacao nasce em `PENDING`.

**Rejeicao e falha sao coisas diferentes.** Rejeicao e regra de negocio: o cliente
pode corrigir a entrada e reenviar. Falha permanente e infraestrutura: reenviar nao
muda o resultado e o cliente nao tem o que corrigir. Tratar os dois como erro unico
faz o provedor reenviar para sempre uma operacao que nunca vai passar.

**A regra de coerencia entre estado e dados e por estado, e nao por "terminal".**
`PROCESSED` carrega resultado e nao carrega codigo de falha; `REJECTED` e `FAILED`
carregam codigo e nao carregam resultado. Com a regra por "terminal", um
`PROCESSED` legitimo seria recusado por nao ter codigo.

**`OPENING` e de origem interna e nasce em `PROCESSED`.** Nao ha referencia a
esperar e nao ha dependencia alem da propria carteira. Criar em `PENDING` e
depender de um segundo passo abriria uma janela em que a carteira existe sem a
transacao que a originou, e a reconciliacao executada nesse intervalo veria saldo
sem lancamento. A invariante e verificada nos dois sentidos: `Registrar` recusa
`OPENING` externo, e a reidratacao recusa `OPENING` com qualquer identidade externa.

**A politica de valor e aplicada na entrada.** `Registrar` exige valor maior que
zero para `BET`, `WIN`, `REFUND` e `ROLLBACK`, e zero exato para `LOSS`. Adiar essa
conferencia faria operacao com valor invalido virar registro duravel em `PENDING`
que outra instancia teria de rejeitar depois -- e o provedor receberia o codigo de
falha tarde demais para distinguir entrada incorreta de indisponibilidade.

**Saldo inicial zero nao cria `OPENING`,** nem lancamento, nem evento. Criar a
transacao assim daria um `OPENING` por jogador sem nenhum movimento por tras.

---

## Reversoes: `REFUND` e `ROLLBACK`

Esta era a decisao que o plano mandava fechar antes de codar.

**Decisao:** uma `BET` e neutralizada **uma unica vez**, por `REFUND` **ou** por
`ROLLBACK`, nunca pelas duas.

- `REFUND` referencia apenas `BET`. Devolve o debito da aposta.
- `ROLLBACK` referencia `BET`, `WIN` ou `REFUND`, e faz o contrario do que a
  referencia fez.
- Depois de um `REFUND` bem-sucedido sobre uma `BET`, um `ROLLBACK` sobre a mesma
  `BET` e recusado com `REVERSAO_JA_APLICADA`.
- Depois de um `ROLLBACK` bem-sucedido sobre uma `BET`, um `REFUND` sobre a mesma
  `BET` e recusado pelo mesmo codigo.
- Um `WIN` e desfeito uma unica vez, por `ROLLBACK`. Um `REFUND` tambem e desfeito
  uma unica vez, por `ROLLBACK` -- e o caminho para anular uma devolucao.
- Nao existe caminho para desfazer um `ROLLBACK`. Desfazer o contrario de uma
  operacao e refazer a operacao original, que o provedor deve enviar de novo com a
  chave dele.

**Por que as duas nao podem ser somadas:** `REFUND` de uma `BET` credita o valor
do debito, e `ROLLBACK` da mesma `BET` credita o mesmo valor. Aceitar as duas em
sequencia creditaria duas vezes o mesmo dinheiro, e o saldo da carteira deixaria de
corresponder ao ledger. Como o enunciado exige que a soma do ledger reproduza o
saldo, e o ledger e append-only, a duplicidade nao seria corrigivel depois: seria um
lancamento novo que contradiz dois anteriores.

**Por que o inverso e decidido pelo efeito e nao pelo tipo:** `BET` e debito, e o
contrario de um debito e `WIN`; `WIN` e `REFUND` sao credito, e o contrario de um
credito e `BET`. Decidir pelo tipo erra no `REFUND`, que e credito: um
`ROLLBACK` de `REFUND` que creditasse em vez de debitar devolveria o dinheiro duas
vezes.

**Conferencia de identidade:** operacao e referencia precisam concordar em
provedor, jogador, carteira, moeda e rodada. A rodada e a mais relevante, porque
uma reversao que atravessa rodada devolve dinheiro de uma aposta para outra e o
provedor nao tem como conferir.

**Valor:** o valor da reversao tem de ser igual ao valor referenciado. Reversao
parcial esta fora do desafio, e aceita-la exigiria uma segunda regra que ninguem
pediu: o que fazer com a parte nao devolvida.

**`ROLLBACK` sem saldo tem codigo proprio,** `ROLLBACK_SEM_SALDO`, diferente de
`BET_SEM_SALDO`. No primeiro o jogador nao tem saldo para o movimento inverso; no
segundo nao tinha saldo para a aposta. O erro carrega as duas informacoes: a causa
`wallet.ErrSaldoInsuficiente` para o caso de uso tratar saldo insuficiente como
saldo insuficiente, e o codigo estavel para o provedor tratar os dois problemas
como diferentes.

---

## Ledger

**Decisao:** `WalletLedgerEntry` imutavel, com `balanceBefore` e `balanceAfter`
gravados na propria linha, e validacao no construtor de que
`balanceAfter = balanceBefore +- money` conforme a direcao.

**Por que os dois saldos na linha:** a reconciliacao reconstrui o saldo a partir do
ledger e compara com o saldo armazenado. Com o saldo anterior em cada lancamento, a
reconstrucao e uma soma com deteccao de salto, e nao uma confianca no saldo de
hoje.

**Lancamento de valor zero nao existe.** Ele nao muda o saldo, mas entraria na soma
do ledger e na contagem de lancamentos da reconciliacao como se fosse movimentacao.
`LOSS` e a operacao sem efeito: existe, e confirmada e auditada, sem lancamento.

**Imutabilidade e no banco:** trigger que proibe `UPDATE` e `DELETE` em
`wallet_ledger_entry`, mais `UNIQUE (wallet_id, transaction_id)`. Correcao
financeira e lancamento novo, nunca edicao.

---

## Eventos

**Decisao:** o envelope leva `eventId`, `eventType`, `aggregateId`, `correlationId`,
`causationId` opcional, `occurredAt` em UTC, `version` e `data` tipado. Tipo e
versao sao definidos pelo construtor do evento.

**`occurredAt` e sempre UTC em RFC 3339** porque o envelope e snapshot imutavel
guardado na outbox e pode ser lido em outra regiao, com outro fuso.

**`eventId` e estavel entre republicacoes.** O relay pode publicar o mesmo registro
da outbox mais de uma vez -- depois de publicar e antes de confirmar -- e o
consumidor precisa poder reconhecer que e o mesmo evento. Por isso o `eventId` e
gerado na escrita da outbox e nao na publicacao.

**Os quatro eventos:** `WagerTransactionProcessed` (inclui `LOSS`),
`WagerTransactionRejected`, `WalletBalanceChanged` (so quando o saldo muda de
verdade) e `WagerTransactionPendingReference`.

---

## Autenticacao e autorizacao

**Decisao:** Keycloak com `client_credentials`. A identidade do provedor vem do
`azp` do token, que e o proprio `client_id`, e a autorizacao e por escopo atribuido
por cliente.

- O token de um provedor carrega `wager:operacoes` e nada mais.
- O token interno `wager-service` carrega `wager:carteira:abertura` e
  `wager:reconciliacao`, e nunca `wager:operacoes`.

**Por que `azp` e nao um claim de role:** o `client_id` do provedor ja e a
identidade dele, e o realm de testeprovisiona um cliente por provedor. Um claim de
role exigiria que o `client_credentials` carregasse role, e o token de
`client_credentials` nao tem usuario: nao ha quem carregue a role. Introduzir um
claim customizado seria uma segunda fonte de identidade, e as duas divergem.

**Por que escopo e nao so `azp`:** sem escopo, "provedor tem tudo" seria o
resultado, e abertura de carteira e reconciliacao ficariam acessiveis a qualquer
provedor. O escopo separa o que e do provedor do que e do servico interno, e a
separacao esta no IdP, e nao em codigo da aplicacao.

**Por que o segredo do cliente esta no realm versionado:** segredo gerado no import
muda a cada recriacao de volume, e o teste de integracao passa a falhar por causa
do ambiente. O segredo de ambiente local de teste e publico por desenho; o que nao
entra no repositorio e chave de ambiente que aponte para servico real.

---

## Persistencia

**Decisao:** `pgx` com SQL explicito, sem ORM e sem `sqlc`, e `goose` nas
migrations.

**Por que sem ORM:** transacao, lock e constraint precisam permanecer explicitos e
verificaveis, que e o que o enunciado pede. Um ORM esconde a transacao que segura o
`SELECT ... FOR UPDATE` junto com o `INSERT` do lancamento, e e exatamente essa
atomicidade que precisa ser lida no codigo.

**Por que sem `sqlc`:** `sqlc` ajuda a reduzir repeticao de query, mas a consulta
financial deste sistema nao repete: cada uma tem lock, condicao de versao ou
`INSERT ... ON CONFLICT` proprio, e o que se quer e ler isso inteiro, nao
economizar linha.

**A unidade transacional e da aplicacao, nao do repositorio.** O caso de uso abre a
transacao SQL e passa o handle para os repositorios. Um repositorio que abre a
propria transacao impediria o `FOR UPDATE` e o lancamento de compartilharem o mesmo
commit -- que e o que torna a operacao atomica.

---

## Inbox e outbox

**Decisao:** na entrada por SQS, o registro da inbox e a conclusao duravel do
tratamento compartilham a transacao SQL das alteracoes de dominio, do ledger e dos
eventos. A publicacao nunca acontece dentro da transacao financeira.

**Fluxo da saida:** alteracao financeira, transacao, ledger e inbox/outbox no mesmo
commit; o relay le a outbox, publica e confirma a publicacao. Republicacao
preserva o `eventId`.

**A mensagem so e removida da fila depois do commit do tratamento.** Uma falha
entre o commit e o `delete` resulta em reentrega, e a inbox transforma a reentrega
em replay do resultado persistido.

**Disputa por registros da outbox:** `FOR UPDATE SKIP LOCKED`, com lease e
`next_attempt_at` para backoff. `SKIP LOCKED` permite varios publishers sem que um
espero o outro, e e o que permite recuperar trabalho abandonado depois de uma
interrupcao.

**A chave de particao da saida e o `aggregateId` do evento.** Esta era uma das duas
decisoes que o plano mandava fechar antes de codar o relay, e ela e do agregado e
nao da carteira nem do provedor pelo motivo seguinte: o agregado ja e o dado que
ordena os fatos. `WagerTransactionProcessed` tem agregado igual ao
`transactionId`, e `WalletBalanceChanged` tambem -- os dois eventos de uma operacao
tem o mesmo agregado, entao a FIFO garante que o consumidor ve "operacao processada"
antes de "saldo alterado", que e a ordem em que os fatos aconteceram.

Escolher a carteira exigiria denormalizar `wallet_id` na `outbox_events` e preenche-la
em todo `INSERT`, para chegar ao mesmo resultado por um caminho mais longo: a carteira
e a mesma para todos os eventos do agregado, entao a chave por agregado e mais estreita
e nao perde nenhuma ordem que a chave por carteira conservaria. Escolher a fila
inteira serializaria todas as publicacoes do sistema em uma so.

**A ordem entre agregados diferentes nao e preservada, e isso e uma decisao, nao uma
lacuna.** Com `SKIP LOCKED`, dois relays pegam lotes disjuntos em paralelo e a ordem
entre eles depende de qual-pega-o-que. Um `WalletBalanceChanged` da carteira A pode
chegar ao consumidor depois do `WagerTransactionProcessed` da carteira B. Isso e
aceitavel porque os dois eventos descrevem agregados independentes: o consumidor que
mantem estado por agregado precisa so de ordem *dentro* do agregado, que a chave de
particao garante. Um consumidor que Precisa de uma ordem global entre carteiras
precisa de um sequenciador, e o `occurredAt` do envelope e o que existe para isso.

**A deduplicacao do broker nao e o que garante idempotencia do evento.** A
`MessageDeduplicationId` leva o `eventId`, e a deduplicacao por conteudo fica
desligada como na fila de entrada. A razao e a mesma: a janela do SQS e de cinco
minutos, e uma republicacao por falha entre publicar e confirmar pode acontecer muito
depois. O `eventId` e gerado na escrita da outbox e sobrevive a republicacao, entao o
consumidor reconhece o mesmo evento pelo contrato -- e nao por uma janela do broker que
ele nao controla.

**A fila de saida e separada da fila de entrada.** `wager-events.fifo` recebe envelope
de evento; `wager-transactions.fifo` recebe comando de jogo. Publicar evento na fila
de operacoes entregaria ao consumidor de operacoes algo que ele nao sabe ler, e a
falha apareceria como mensagem malformada em vez de como problema de topologia.

**Um evento que esgota as tentativas nao e apagado: e marcado como falha permanente**,
com `failed_at` e `failure_reason`, e deixa de ser retomado. Descartar resolveria o
laco, mas apagar evento perderia a evidencia de que ele existiu e nao foi publicado --
e o papel de runtime nao tem `DELETE`, a mesma garantia que protege o ledger. O
registro continua no banco e o motivo fica legivel para o operador decidir. E a mesma
forma de desfecho que a referencia que nunca chega, na transacao.

---

## Pendencias registradas

As decisoes abaixo ainda nao foram fechadas e precisam ser registradas aqui no
momento em que forem:

- `statement_timeout` e `lock_timeout` do pool: fechados na E7, 3s e 1s no papel de
  runtime.
- `MessageGroupId`, `MessageDeduplicationId`, visibility timeout, receive count e
  limite de tentativas: fechados na E13 para a fila de entrada e na E15 para a fila de
  saida. Registrados nas secoes "Inbox e outbox" e "Mensageria".
- Chave de particionamento e ordenacao da outbox: fechada na E15, e o `aggregateId`.
  Ver a secao "Inbox e outbox".
